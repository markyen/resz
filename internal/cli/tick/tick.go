// Package tick implements the `resz tick` subcommand: a single cron
// entrypoint that runs config housekeeping (ID backfill, past
// blackout-date prune) and then invokes check followed by snipe.
//
// Intended to be wired into cron every ~30 minutes. Check runs first
// because it's fast; snipe runs second because it may sleep up to ~31
// minutes waiting for a venue's target_time.
package tick

import (
	"flag"
	"fmt"
	"os"
	"time"

	"resz/internal/cli/check"
	"resz/internal/cli/snipe"
	"resz/internal/reslog"
	"resz/internal/resyapi"
	"resz/internal/venueconfig"
)

// Main runs the tick subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz tick", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFlag := fs.String("c", "", "venues config path (default <workspace>/config/venues.json)")
	verboseFlag := fs.Bool("v", false, "echo log output to stderr as well as the log file")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz tick [-c PATH] [-v]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	lg, lf, err := reslog.Open("tick", *verboseFlag)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log open:", err)
		return 1
	}
	defer lf.Close()

	cfgPath := *configFlag
	if cfgPath == "" {
		p, err := venueconfig.DefaultPath()
		if err != nil {
			lg.Printf("FATAL config path: %v", err)
			return 1
		}
		cfgPath = p
	}

	cfg, err := venueconfig.Load(cfgPath)
	if err != nil {
		lg.Printf("FATAL load %s: %v", cfgPath, err)
		return 1
	}
	if housekeep(lg, cfg, time.Now()) {
		if err := venueconfig.Save(cfgPath, cfg); err != nil {
			lg.Printf("housekeeping save: %v", err)
		} else {
			lg.Printf("wrote updated config to %s", cfgPath)
		}
	}

	lg.Printf("invoking check")
	checkRC := check.Main(nil)
	lg.Printf("check returned %d; invoking snipe", checkRC)
	snipeRC := snipe.Main(nil)
	lg.Printf("snipe returned %d", snipeRC)

	if checkRC != 0 {
		return checkRC
	}
	return snipeRC
}

// housekeep performs two idempotent cleanups on the loaded config:
// backfilling any venue's missing ID via Resy search, and dropping
// blackout_dates strictly before today. Returns true if anything
// changed.
func housekeep(lg interface{ Printf(string, ...any) }, cfg *venueconfig.Config, now time.Time) bool {
	dirty := false
	for i, e := range cfg.Venues {
		if e.ID != "" || e.Name == "" {
			continue
		}
		citySlug := e.City
		if citySlug == "" {
			citySlug = resyapi.DefaultCity
		}
		city, ok := resyapi.LookupCity(citySlug)
		if !ok {
			lg.Printf("entry %q: unknown city %q (skipping backfill)", e.Name, citySlug)
			continue
		}
		hits, err := resyapi.Search(e.Name, city.Lat, city.Long)
		if err != nil {
			lg.Printf("search %q in %s: %v", e.Name, city.Slug, err)
			continue
		}
		if len(hits) == 0 {
			lg.Printf("search %q in %s: no results", e.Name, city.Slug)
			continue
		}
		cfg.Venues[i].ID = hits[0].ID.Resy.String()
		lg.Printf("resolved %q -> id %s (matched %q in %s)", e.Name, cfg.Venues[i].ID, hits[0].Name, city.Slug)
		dirty = true
	}
	today := now.Format("2006-01-02")
	n := 0
	for _, d := range cfg.BlackoutDates {
		if d >= today {
			cfg.BlackoutDates[n] = d
			n++
			continue
		}
		lg.Printf("pruned past blackout date %s", d)
		dirty = true
	}
	cfg.BlackoutDates = cfg.BlackoutDates[:n]
	return dirty
}
