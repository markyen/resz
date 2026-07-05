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
	"resz/internal/venueconfig"
)

// Main runs the search subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	interactive := fs.Bool("i", false, "interactively choose a result by name")
	cityFlag := fs.String("city", resyapi.DefaultCity, "city slug to bias search (use -list-cities to see all)")
	listCities := fs.Bool("list-cities", false, "print supported city slugs and exit")
	addFlag := fs.Bool("a", false, "add the resolved venue to the config with an empty check block (instead of printing its id)")
	configFlag := fs.String("c", "", "config file path for -a (defaults to <workspace>/config/venues.json)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz search [-i] [-a [-c PATH]] [-city SLUG] <restaurant name>\n")
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

	hit := hits[idx]
	if *addFlag {
		return addToConfig(*configFlag, hit, city)
	}
	fmt.Println(hit.ID.Resy.String())
	return 0
}

// addToConfig appends hit to the venues config at path (default location if
// empty), with an empty check block so `resz check` picks it up. A missing
// config file is created; a venue whose id is already present is left alone.
func addToConfig(path string, hit resyapi.Hit, city *resyapi.City) int {
	if path == "" {
		p, err := venueconfig.DefaultPath()
		if err != nil {
			fmt.Fprintln(os.Stderr, "config path:", err)
			return 1
		}
		path = p
	}
	cfg, err := venueconfig.Load(path)
	if err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintln(os.Stderr, "load config:", err)
			return 1
		}
		cfg = &venueconfig.Config{}
	}
	id := hit.ID.Resy.String()
	for _, v := range cfg.Venues {
		if v.ID == id {
			fmt.Fprintf(os.Stderr, "%s (id %s) is already in %s\n", hit.Name, id, path)
			return 0
		}
	}
	cfg.Venues = append(cfg.Venues, venueconfig.ConfigEntry{
		Name:  hit.Name,
		ID:    id,
		City:  city.Slug,
		Check: &venueconfig.CheckConfig{},
	})
	if err := venueconfig.Save(path, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "save config:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "added %s (id %s) to %s\n", hit.Name, id, path)
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
