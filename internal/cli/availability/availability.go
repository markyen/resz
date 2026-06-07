// Package availability implements the `resz availability` subcommand.
package availability

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"

	"resz/internal/resyapi"
	"resz/internal/resyflow"
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
	name := fs.String("n", "", "restaurant name to look up via search (instead of a venue_id)")
	cityFlag := fs.String("city", resyapi.DefaultCity, "city slug to bias name search (only used with -n)")
	verboseFlag := fs.Bool("v", false, "print URLs and full response bodies on errors")
	configFlag := fs.String("c", "", "snipe config file (provides venues and blackout_dates)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz availability [-s N] [-i] [-v] [-city SLUG] [-c CONFIG] (-n <name> | <venue_id> | (config venues))\n")
		fmt.Fprintln(os.Stderr, "  With no name or venue_id, checks all venues in the config file.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
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

	// Config mode: no name or venue_id given — check all venues from config.
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
			partySize := v.PartySize
			if partySize == 0 {
				partySize = *partySizeFlag
			}
			fmt.Fprintf(os.Stderr, "=== %s (id %s, party %d) ===\n", v.Name, v.ID, partySize)
			checkVenue(v.ID, partySize, blackout)
		}
		return 0
	}

	// Single-venue mode.
	if *name != "" && fs.NArg() > 0 {
		fmt.Fprintln(os.Stderr, "specify -n NAME or a venue_id, not both")
		return 2
	}

	var venueID string
	switch {
	case *name != "":
		city, ok := resyapi.LookupCity(*cityFlag)
		if !ok {
			fmt.Fprintf(os.Stderr, "unknown city %q\n", *cityFlag)
			return 2
		}
		id, resolved, err := resolveByName(*name, city)
		if err != nil {
			fmt.Fprintln(os.Stderr, "search:", err)
			return 1
		}
		fmt.Fprintf(os.Stderr, "resolved %q to %s (id %s)\n", *name, resolved, id)
		venueID = id
	case fs.NArg() == 1:
		venueID = fs.Arg(0)
		if _, err := strconv.Atoi(venueID); err != nil {
			fmt.Fprintln(os.Stderr, "venue_id must be numeric")
			return 2
		}
	default:
		fs.Usage()
		return 2
	}

	dates, err := resyflow.FilteredDates(nil, venueID, *partySizeFlag, blackout, vlogger())
	if err != nil {
		fmt.Fprintln(os.Stderr, "calendar:", err)
		return 1
	}
	if len(dates) == 0 {
		fmt.Fprintln(os.Stderr, "no dates with availability")
		return 1
	}

	venue, err := resyapi.GetVenue(nil, venueID)
	if err != nil {
		fmt.Fprintln(os.Stderr, "venue:", err)
		return 1
	}
	if verbose {
		fmt.Fprintf(os.Stderr, "venue location: code=%s lat=%s long=%s\n",
			venue.Location.Code, venue.LatString(), venue.LongString())
	}

	if *interactive {
		return runInteractive(venueID, *partySizeFlag, dates, venue)
	}

	date, slot, ok, err := resyflow.BestBookableSlot(nil, venueID, *partySizeFlag, venue, dates, nil, vlogger())
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "no slots in target window on any available date")
		return 1
	}
	fmt.Fprintf(os.Stderr, "selected %s at %s\n", date, slot.Date.Start)
	fmt.Println(slot.Config.Token)
	return 0
}

// checkVenue prints the best available slot for a venue (config mode).
func checkVenue(venueID string, partySize int, blackout map[string]bool) {
	dates, err := resyflow.FilteredDates(nil, venueID, partySize, blackout, vlogger())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  calendar: %v\n", err)
		return
	}
	if len(dates) == 0 {
		fmt.Fprintln(os.Stderr, "  no dates with availability")
		return
	}
	venue, err := resyapi.GetVenue(nil, venueID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  venue lookup: %v\n", err)
		return
	}
	date, slot, ok, err := resyflow.BestBookableSlot(nil, venueID, partySize, venue, dates, nil, vlogger())
	if err != nil {
		fmt.Fprintf(os.Stderr, "  %v\n", err)
		return
	}
	if !ok {
		fmt.Fprintln(os.Stderr, "  no slots in target window")
		return
	}
	fmt.Fprintf(os.Stderr, "selected %s at %s\n", date, slot.Date.Start)
	fmt.Println(slot.Config.Token)
}

const maxInteractiveOptions = 50

func runInteractive(venueID string, partySize int, dates []string, venue *resyapi.Venue) int {
	type listed struct {
		date string
		s    resyapi.Slot
	}
	var all []listed
	truncated := false
collect:
	for _, date := range dates {
		slots, err := resyapi.SlotsInWindow(nil, venueID, date, partySize, venue, nil, vlogger())
		if err != nil {
			fmt.Fprintf(os.Stderr, "find %s: %v\n", date, err)
			continue
		}
		for _, s := range slots {
			if len(all) == maxInteractiveOptions {
				truncated = true
				break collect
			}
			all = append(all, listed{date, s})
		}
	}
	if len(all) == 0 {
		fmt.Fprintln(os.Stderr, "no slots in target window on any available date")
		return 1
	}

	for i, l := range all {
		label := l.s.Config.Type
		if label != "" {
			label = " (" + label + ")"
		}
		fmt.Fprintf(os.Stderr, "[%d] %s%s\n", i+1, l.s.Date.Start, label)
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
	fmt.Println(all[n-1].s.Config.Token)
	return 0
}

func resolveByName(name string, city *resyapi.City) (id, resolved string, err error) {
	hits, err := resyapi.Search(name, city.Lat, city.Long)
	if err != nil {
		return "", "", err
	}
	if len(hits) == 0 {
		return "", "", fmt.Errorf("no results for %q", name)
	}
	return hits[0].ID.Resy.String(), hits[0].Name, nil
}
