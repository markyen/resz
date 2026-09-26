// Package search implements the `resz search` subcommand.
package search

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"resz/internal/bookflow"
	"resz/internal/cliflag"
	"resz/internal/platform"
	"resz/internal/resyapi"
	"resz/internal/venueconfig"
)

var verbose bool

func vlogger() func(string) {
	if !verbose {
		return nil
	}
	return func(msg string) { fmt.Fprintln(os.Stderr, msg) }
}

// Main runs the search subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz search", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	interactive := fs.Bool("i", false, "interactively choose a result by name")
	cityFlag := fs.String("city", resyapi.DefaultCity, "city slug to bias the Resy search (use --list-cities to see all)")
	listCities := fs.Bool("list-cities", false, "print supported city slugs and exit")
	addFlag := fs.Bool("a", false, "add the resolved venue to the config with an empty check block (instead of printing its id)")
	configFlag := fs.String("c", "", "config file path for -a (defaults to <workspace>/config/venues.json)")
	partySizeFlag := fs.Int("s", 2, "party size to store on the added venue (only used with -a)")
	platformFlag := fs.String("platform", "all", "platform to search: all, "+strings.Join(platform.Names(), ", "))
	verboseFlag := fs.Bool("v", false, "print request URLs")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz search [-i] [-v] [-a [-c PATH] [-s N]] [--platform NAME] [--city SLUG] <restaurant name>\n")
		fmt.Fprintln(os.Stderr, "  Searches Resy and SevenRooms by default. SevenRooms publishes no venue")
		fmt.Fprintln(os.Stderr, "  directory, so its results are found by guessing the booking-URL slug;")
		fmt.Fprintln(os.Stderr, "  pasting a venue's reservation URL always resolves it exactly.")
		cliflag.PrintDefaults(fs)
	}
	if err := fs.Parse(cliflag.Expand(fs, args)); err != nil {
		return 2
	}
	verbose = *verboseFlag
	// Only persist party size on -a when it was explicitly given;
	// otherwise leave it unset (0) so the commands apply their own default.
	sSet := false
	fs.Visit(func(f *flag.Flag) {
		if f.Name == "s" {
			sSet = true
		}
	})
	if sSet && *partySizeFlag < 1 {
		fmt.Fprintln(os.Stderr, "party size must be >= 1")
		return 2
	}
	partySize := 0
	if sSet {
		partySize = *partySizeFlag
	}

	if *listCities {
		for _, s := range resyapi.CitySlugs() {
			fmt.Println(s)
		}
		return 0
	}

	platforms, err := parsePlatforms(*platformFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	city, ok := resyapi.LookupCity(*cityFlag)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown city %q (use --list-cities to see supported)\n", *cityFlag)
		return 2
	}

	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	query := strings.Join(fs.Args(), " ")

	hits, errs := bookflow.Search(nil, platforms, query, city.Lat, city.Long, vlogger())
	for _, p := range platforms {
		if err := errs[p]; err != nil {
			fmt.Fprintf(os.Stderr, "%s search failed: %v\n", p, err)
		}
	}
	if len(hits) == 0 {
		if len(errs) == len(platforms) {
			return 1
		}
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
		return addToConfig(*configFlag, hit, city, partySize)
	}
	// Without -i the platform isn't obvious from a bare id, so name it.
	fmt.Fprintf(os.Stderr, "%s: %s\n", hit.Platform, hit.Name)
	fmt.Println(hit.ID)
	return 0
}

// parsePlatforms resolves the --platform flag, where "all" means every
// supported platform.
func parsePlatforms(s string) ([]platform.Platform, error) {
	if strings.EqualFold(strings.TrimSpace(s), "all") {
		return platform.All(), nil
	}
	p, err := platform.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("%v (or \"all\")", err)
	}
	return []platform.Platform{p}, nil
}

// addToConfig appends hit to the venues config at path (default location if
// empty), with an empty check block so `resz check` picks it up. partySize
// is stored on the entry when non-zero; zero leaves it unset so the
// commands apply their default. A missing config file is created; a venue
// already present on the same platform is left alone.
func addToConfig(path string, hit platform.SearchHit, city *resyapi.City, partySize int) int {
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
	// Ids are only unique within a platform, so identity is the pair.
	for _, v := range cfg.Venues {
		if p, _ := v.Platform(); p == hit.Platform && v.ID == hit.ID {
			fmt.Fprintf(os.Stderr, "%s (%s id %s) is already in %s\n", hit.Name, hit.Platform, hit.ID, path)
			return 0
		}
	}
	entry := venueconfig.ConfigEntry{
		Name:      hit.Name,
		ID:        hit.ID,
		PartySize: partySize,
		Check:     &venueconfig.CheckConfig{},
	}
	// Leave `platform` off Resy entries so existing configs keep their
	// shape, and leave `city` off non-Resy ones: the slug is a Resy
	// market, and SevenRooms reports its own timezone.
	if hit.Platform != platform.Default {
		entry.Plat = string(hit.Platform)
	} else {
		entry.City = city.Slug
	}
	cfg.Venues = append(cfg.Venues, entry)
	if err := venueconfig.Save(path, cfg); err != nil {
		fmt.Fprintln(os.Stderr, "save config:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "added %s (%s id %s) to %s\n", hit.Name, hit.Platform, hit.ID, path)
	return 0
}

const maxInteractiveOptions = 50

func chooseHit(hits []platform.SearchHit) (int, error) {
	truncated := false
	if len(hits) > maxInteractiveOptions {
		hits = hits[:maxInteractiveOptions]
		truncated = true
	}
	for i, h := range hits {
		where := h.City
		if where != "" {
			where = ", " + where
		}
		fmt.Fprintf(os.Stderr, "[%d] %s%s (%s)\n", i+1, h.Name, where, h.Platform)
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
