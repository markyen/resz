// Package availability implements the `resz availability` subcommand.
package availability

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

func vlog(msg string) {
	if verbose {
		fmt.Fprintln(os.Stderr, msg)
	}
}

func vlogger() func(string) {
	if verbose {
		return vlog
	}
	return nil
}

// loadConfig reads the snipe config from path, or the default location
// if path is empty. Missing or unparsable files yield (nil, err) and a
// stderr line — the caller decides whether that's fatal.
func loadConfig(path string) (*venueconfig.Config, error) {
	if path == "" {
		p, err := venueconfig.DefaultPath()
		if err != nil {
			return nil, err
		}
		path = p
		fmt.Fprintf(os.Stderr, "Using default config path: %s\n", path)
	}
	return venueconfig.Load(path)
}

// Main runs the availability subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz availability", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	partySizeFlag := fs.Int("s", 2, "party size (number of seats; per-venue config value takes priority)")
	interactive := fs.Bool("i", false, "interactively choose a slot (single-venue mode only)")
	name := fs.String("n", "", "restaurant name to look up via search (instead of a venue id)")
	cityFlag := fs.String("city", resyapi.DefaultCity, "city slug to bias name search (only used with -n)")
	platformFlag := fs.String("platform", string(platform.Default), "platform of the given venue id: "+strings.Join(platform.Names(), ", "))
	verboseFlag := fs.Bool("v", false, "print URLs and full response bodies on errors")
	configFlag := fs.String("c", "", "snipe config file (provides venues and blackout_dates)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz availability [-s N] [-i] [-v] [--platform NAME] [--city SLUG] [-c CONFIG] (-n <name> | <venue_id> | (config venues))\n")
		fmt.Fprintln(os.Stderr, "  With no name or venue id, checks all venues in the config file.")
		fmt.Fprintln(os.Stderr, "  A venue id is numeric on Resy and a booking-URL slug on SevenRooms;")
		fmt.Fprintln(os.Stderr, "  a full SevenRooms reservation URL works too.")
		cliflag.PrintDefaults(fs)
	}
	if err := fs.Parse(cliflag.Expand(fs, args)); err != nil {
		return 2
	}
	verbose = *verboseFlag
	if *partySizeFlag < 1 {
		fmt.Fprintln(os.Stderr, "party size must be >= 1")
		return 2
	}

	cfg, _ := loadConfig(*configFlag)

	var blackout map[string]bool
	if cfg != nil && len(cfg.BlackoutDates) > 0 {
		blackout = venueconfig.BlackoutSet(cfg.BlackoutDates)
	}

	// Config mode: no name or venue id given — check all venues from config.
	if *name == "" && fs.NArg() == 0 {
		if cfg == nil || len(cfg.Venues) == 0 {
			fs.Usage()
			return 2
		}
		for _, v := range cfg.Venues {
			if v.ID == "" {
				fmt.Fprintf(os.Stderr, "[%s] no id in config, skipping\n", v.Name)
				continue
			}
			p, err := v.Platform()
			if err != nil {
				fmt.Fprintf(os.Stderr, "[%s] %v, skipping\n", v.Name, err)
				continue
			}
			partySize := v.PartySize
			if partySize == 0 {
				partySize = *partySizeFlag
			}
			fmt.Fprintf(os.Stderr, "=== %s (%s id %s, party %d) ===\n", v.Name, p, v.ID, partySize)
			checkVenue(p, v.ID, v.City, partySize, blackout)
		}
		return 0
	}

	// Single-venue mode.
	if *name != "" && fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "specify -n NAME or a venue id, not both")
		return 2
	}

	city, ok := resyapi.LookupCity(*cityFlag)
	if !ok {
		fmt.Fprintf(os.Stderr, "unknown city %q\n", *cityFlag)
		return 2
	}

	var (
		venueID string
		p       platform.Platform
	)
	switch {
	case *name != "":
		hit, err := resolveByName(*name, city)
		if err != nil {
			fmt.Fprintln(os.Stderr, "search:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "resolved %q to %s (%s id %s)\n", *name, hit.Name, hit.Platform, hit.ID)
		venueID, p = hit.ID, hit.Platform
	case fs.NArg() == 1:
		venueID = fs.Arg(0)
		parsed, err := platform.Parse(*platformFlag)
		if err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 2
		}
		p = parsed
		if p == platform.Resy {
			if _, err := strconv.Atoi(venueID); err != nil {
				fmt.Fprintln(os.Stderr, "a Resy venue_id must be numeric (use --platform sevenrooms for a SevenRooms slug)")
				return 2
			}
		}
	default:
		fs.Usage()
		return 2
	}

	vc, err := bookflow.Open(p, nil, venueID, city.Slug, vlogger())
	if err != nil {
		fmt.Fprintln(os.Stderr, "venue:", err)
		return 1
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "venue: platform=%s id=%s name=%q timezone=%s\n",
			vc.Platform(), vc.ID(), vc.Name(), vc.Timezone())
	}

	dates, err := bookflow.FilteredDates(vc, *partySizeFlag, blackout, vlogger())
	if err != nil {
		fmt.Fprintln(os.Stderr, "calendar:", err)
		return 1
	}
	if len(dates) == 0 {
		fmt.Fprintln(os.Stderr, "no dates with availability")
		return 1
	}

	if *interactive {
		return runInteractive(vc, *partySizeFlag, dates)
	}

	date, slot, ok, err := bookflow.BestBookableSlot(vc, *partySizeFlag, dates, nil, vlogger())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "no slots in target window on any available date")
		return 1
	}
	fmt.Fprintf(os.Stderr, "selected %s at %s\n", date, slot.StartLabel())
	fmt.Println(slot.Ref)
	return 0
}

// checkVenue prints the best available slot for a venue (config mode).
func checkVenue(p platform.Platform, venueID, citySlug string, partySize int, blackout map[string]bool) {
	vc, err := bookflow.Open(p, nil, venueID, citySlug, vlogger())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  venue lookup: %v\n", err)
		return
	}
	dates, err := bookflow.FilteredDates(vc, partySize, blackout, vlogger())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  calendar: %v\n", err)
		return
	}
	if len(dates) == 0 {
		fmt.Fprintln(os.Stderr, "  no dates with availability")
		return
	}
	date, slot, ok, err := bookflow.BestBookableSlot(vc, partySize, dates, nil, vlogger())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %v\n", err)
		return
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "  no slots in target window")
		return
	}
	fmt.Fprintf(os.Stderr, "selected %s at %s\n", date, slot.StartLabel())
	fmt.Println(slot.Ref)
}

const maxInteractiveOptions = 50

func runInteractive(vc platform.VenueClient, partySize int, dates []string) int {
	var all []platform.Slot
	truncated := false
collect:
	for _, date := range dates {
		slots, err := bookflow.SlotsInWindow(vc, date, partySize, nil, vlogger())
		if err != nil {
			fmt.Fprintf(os.Stderr, "%v\n", err)
			continue
		}
		for _, s := range slots {
			if len(all) == maxInteractiveOptions {
				truncated = true
				break collect
			}
			all = append(all, s)
		}
	}
	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "no slots in target window on any available date")
		return 1
	}

	for i, s := range all {
		label := s.Label
		if label != "" {
			label = " (" + label + ")"
		}
		fmt.Fprintf(os.Stderr, "[%d] %s%s\n", i+1, s.StartLabel(), label)
	}
	if truncated {
		fmt.Fprintf(os.Stderr, "(truncated to first %d slots; more are available)\n", maxInteractiveOptions)
	}
	fmt.Fprintf(os.Stderr, "select [1-%d]: ", len(all))

	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() {
		fmt.Fprintln(os.Stderr, "no input")
		return 1
	}
	n, err := strconv.Atoi(strings.TrimSpace(scanner.Text()))
	if err != nil || n < 1 || n > len(all) {
		fmt.Fprintln(os.Stderr, "invalid selection")
		return 1
	}
	fmt.Println(all[n-1].Ref)
	return 0
}

// resolveByName searches every platform and takes the first hit.
func resolveByName(name string, city *resyapi.City) (platform.SearchHit, error) {
	hits, errs := bookflow.Search(nil, platform.All(), name, city.Lat, city.Long, vlogger())
	if len(hits) == 0 {
		for _, p := range platform.All() {
			if err := errs[p]; err != nil {
				return platform.SearchHit{}, err
			}
		}
		return platform.SearchHit{}, fmt.Errorf("no results for %q", name)
	}
	return hits[0], nil
}
