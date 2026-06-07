// Package search implements the `resz search` subcommand.
package search

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"resz/internal/resyapi"
)

// Main runs the search subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	interactive := fs.Bool("i", false, "interactively choose a result by name")
	cityFlag := fs.String("city", resyapi.DefaultCity, "city slug to bias search (use -list-cities to see all)")
	listCities := fs.Bool("list-cities", false, "print supported city slugs and exit")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz search [-i] [-city SLUG] <restaurant name>\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	if *listCities {
		for _, s := range resyapi.CitySlugs() {
			fmt.Println(s)
		}
		return 0
	}

	city, ok := resyapi.LookupCity(*cityFlag)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown city %q (use -list-cities to see supported)\n", *cityFlag)
		return 2
	}

	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	query := strings.Join(fs.Args(), " ")

	hits, err := resyapi.Search(query, city.Lat, city.Long)
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	if len(hits) == 0 {
		fmt.Fprintln(os.Stderr, "no results")
		return 1
	}

	idx := 0
	if *interactive {
		idx, err = chooseHit(hits)
		if err != nil {
			fmt.Fprintln(os.Stderr, "error:", err)
			return 1
		}
	}

	fmt.Println(hits[idx].ID.Resy.String())
	return 0
}

const maxInteractiveOptions = 50

func chooseHit(hits []resyapi.Hit) (int, error) {
	truncated := false
	if len(hits) > maxInteractiveOptions {
		hits = hits[:maxInteractiveOptions]
		truncated = true
	}
	for i, h := range hits {
		fmt.Fprintf(os.Stderr, "[%d] %s\n", i+1, h.Name)
	}
	if truncated {
		fmt.Fprintf(os.Stderr, "(truncated to first %d results; refine the query for more)\n", maxInteractiveOptions)
	}
	fmt.Fprintf(os.Stderr, "select [1-%d]: ", len(hits))

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		return 0, fmt.Errorf("no input")
	}
	n, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || n < 1 || n > len(hits) {
		return 0, fmt.Errorf("invalid selection")
	}
	return n - 1, nil
}
