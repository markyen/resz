// Package snipe implements the `resz snipe` subcommand.
package snipe

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log"
	"net/http"
	"os"
	"sync"
	"time"

	"resz/internal/notify"
	"resz/internal/resyapi"
	"resz/internal/resyflow"
	"resz/internal/venueconfig"
)

const defaultPartySize = 2

var (
	verbose bool
	// logger timestamps every line with UTC date/time, matching the
	// reslog convention used by check and tick.
	logger = log.New(os.Stderr, "resz-snipe ", log.LstdFlags|log.LUTC)
)

func vlog(msg string) {
	if verbose {
		logger.Println(msg)
	}
}

func logf(format string, a ...any) {
	logger.Printf(format, a...)
}

// Main runs the snipe subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz snipe", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFlag := fs.String("c", "", "config file path (defaults to <workspace>/config/venues.json)")
	verboseFlag := fs.Bool("v", false, "print URLs and verbose progress")
	emailFlag := fs.String("e", os.Getenv("RESY_EMAIL"), "email for token refresh (defaults to $RESY_EMAIL)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz snipe [-c PATH] [-v] [-e EMAIL]\n")
		fmt.Fprintln(os.Stderr, "Reads config, snipes any venues whose target_time is 1-31 min from now.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	verbose = *verboseFlag

	configPath := *configFlag
	if configPath == "" {
		p, err := venueconfig.DefaultPath()
		if err != nil {
			logf("config path: %v", err)
			return 1
		}
		configPath = p
	}

	cfg, err := venueconfig.Load(configPath)
	if err != nil {
		logf("load config %s: %v", configPath, err)
		return 1
	}
	if len(cfg.Venues) == 0 {
		logf("config %s has no venues", configPath)
		return 0
	}

	blackout := venueconfig.BlackoutSet(cfg.BlackoutDates)

	now := time.Now()
	logf("now=%s", now.Format("2006-01-02 15:04:05.000 MST"))

	// Keep only entries with a snipe block whose target_time is in the
	// half-open window [1min, 31min) from now. The window is half-open so
	// that, with a ~30-min cron cadence, exactly one tick ever schedules a
	// given target_time (a closed [1,31] window double-fires at the
	// boundary, e.g. a target 31 min after one tick and 1 min after the
	// next).
	type scheduled struct {
		entry      venueconfig.ConfigEntry
		targetTime time.Time
	}
	var todo []scheduled
	for _, e := range cfg.Venues {
		if e.Snipe == nil {
			continue
		}
		if e.ID == "" {
			continue
		}
		loc := locFromCity(e.City)
		tt, err := nextTargetTime(e.Snipe.TargetTime, now, loc)
		if err != nil {
			logf("entry %q: bad target_time %q: %v", e.Name, e.Snipe.TargetTime, err)
			continue
		}
		delta := tt.Sub(now)
		if delta < 1*time.Minute || delta >= 31*time.Minute {
			vlog(fmt.Sprintf("skipping %q: target_time %s is %s away (outside [1,31) min)", e.Name, tt.In(loc).Format("15:04:05 MST"), delta.Round(time.Second)))
			continue
		}
		todo = append(todo, scheduled{e, tt})
	}
	if len(todo) == 0 {
		logf("no venues scheduled within the next 1-31 minutes; exiting")
		return 0
	}
	for _, s := range todo {
		loc := s.targetTime.Location()
		logf("scheduled: %s at %s (in %s)", s.entry.Name, s.targetTime.In(loc).Format("15:04:05 MST"), time.Until(s.targetTime).Round(time.Second))
	}

	client := newSharedClient()

	auth, err := resyapi.EnsureAuth(*emailFlag, os.Getenv("RESY_PASSWORD"), vloggerFunc())
	if err != nil {
		logf("auth: %v", err)
		return 1
	}

	// Per-venue warmup prep (parallel).
	prepared := make([]struct {
		s               scheduled
		venue           *resyapi.Venue
		currentConfigID string
	}, len(todo))
	var prepWg sync.WaitGroup
	for i, s := range todo {
		prepWg.Add(1)
		go func(i int, s scheduled) {
			defer prepWg.Done()
			v, err := resyapi.GetVenue(client, s.entry.ID)
			if err != nil {
				logf("[%s] GetVenue: %v", s.entry.Name, err)
				return
			}
			partySize := s.entry.PartySize
			if partySize == 0 {
				partySize = defaultPartySize
			}
			cfgID := warmupConfigID(client, s.entry.ID, partySize, v, blackout)
			if cfgID != "" {
				logf("[%s] warmup config_id available: %s", s.entry.Name, cfgID)
			} else {
				vlog(fmt.Sprintf("[%s] no current availability for warmup", s.entry.Name))
			}
			prepared[i].s = s
			prepared[i].venue = v
			prepared[i].currentConfigID = cfgID
		}(i, s)
	}
	prepWg.Wait()

	// Per-venue snipe goroutines. Successful bookings land in `booked`.
	type bookedResult struct {
		venueID string
		name    string
		slot    resyapi.Slot
	}
	var (
		bookedMu sync.Mutex
		booked   []bookedResult
	)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var wg sync.WaitGroup
	for _, p := range prepared {
		if p.venue == nil {
			continue
		}
		partySize := p.s.entry.PartySize
		if partySize == 0 {
			partySize = defaultPartySize
		}
		wg.Add(1)
		go func(p struct {
			s               scheduled
			venue           *resyapi.Venue
			currentConfigID string
		}, partySize int) {
			defer wg.Done()
			slot, ok := snipeVenue(ctx, client, auth, *emailFlag, os.Getenv("RESY_PASSWORD"),
				p.s.entry, p.venue, p.currentConfigID, p.s.targetTime, partySize, blackout, logf)
			if !ok {
				return
			}
			bookedMu.Lock()
			booked = append(booked, bookedResult{venueID: p.s.entry.ID, name: p.s.entry.Name, slot: slot})
			bookedMu.Unlock()
			notifyBooking(p.s.entry.Name, slot)
		}(p, partySize)
	}
	wg.Wait()

	// Remove successfully booked venues from the config, block the
	// booked dates from future attempts, and save once.
	if len(booked) > 0 {
		bookedIDs := make(map[string]bool, len(booked))
		for _, b := range booked {
			bookedIDs[b.venueID] = true
			if t, err := time.Parse("2006-01-02 15:04:05", b.slot.Date.Start); err == nil {
				if d := t.Format("2006-01-02"); cfg.AddBlackout(d) {
					logf("[%s] added %s to blackout_dates", b.name, d)
				}
			}
		}
		out := cfg.Venues[:0]
		for _, v := range cfg.Venues {
			if !bookedIDs[v.ID] {
				out = append(out, v)
			}
		}
		cfg.Venues = out
		if err := venueconfig.Save(configPath, cfg); err != nil {
			logf("save config after booking: %v", err)
		} else {
			for _, b := range booked {
				logf("[%s] removed from venues.json", b.name)
			}
		}
	}
	return 0
}

// notifyBooking sends a per-venue booking confirmation via the
// configured notify transport. Missing transport is logged and skipped.
func notifyBooking(venueName string, slot resyapi.Slot) {
	msg := fmt.Sprintf("booked %s for %s", venueName, slot.StartLabel())
	err := notify.Send(msg)
	switch {
	case err == nil:
		logf("notified booking %s", venueName)
	case errors.Is(err, notify.ErrNoTransport):
		logf("notify %s skipped: no transport configured", venueName)
	default:
		logf("notify %s: %v", venueName, err)
	}
}

// locFromCity returns the *time.Location for the given city slug,
// falling back to time.Local if the slug is empty or unrecognised.
func locFromCity(citySlug string) *time.Location {
	if citySlug == "" {
		citySlug = resyapi.DefaultCity
	}
	if city, ok := resyapi.LookupCity(citySlug); ok {
		if loc, err := time.LoadLocation(city.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}

func nextTargetTime(hm string, now time.Time, loc *time.Location) (time.Time, error) {
	t, err := time.Parse("15:04", hm)
	if err != nil {
		return time.Time{}, err
	}
	nowLoc := now.In(loc)
	today := time.Date(nowLoc.Year(), nowLoc.Month(), nowLoc.Day(), t.Hour(), t.Minute(), 0, 0, loc)
	if !today.After(now) {
		today = today.Add(24 * time.Hour)
	}
	return today, nil
}

// warmupConfigID looks for an already-bookable slot we can use to warm
// up the HTTP/2 connection during pre-snipe prep. Errors are swallowed
// (best-effort); a "" return means use the fallback /3/venue warmup.
func warmupConfigID(client *http.Client, venueID string, partySize int, venue *resyapi.Venue, blackout map[string]bool) string {
	dates, err := resyflow.FilteredDates(client, venueID, partySize, blackout, nil)
	if err != nil {
		return ""
	}
	_, slot, ok, err := resyflow.BestBookableSlot(client, venueID, partySize, venue, dates, nil, nil)
	if err != nil || !ok {
		return ""
	}
	return slot.Config.Token
}

func newSharedClient() *http.Client {
	tr := &http.Transport{
		ForceAttemptHTTP2:   true,
		MaxIdleConns:        50,
		MaxIdleConnsPerHost: 8,
		IdleConnTimeout:     90 * time.Second,
		TLSHandshakeTimeout: 10 * time.Second,
	}
	return &http.Client{
		Transport: tr,
		Timeout:   30 * time.Second,
	}
}

func vloggerFunc() func(string) {
	if verbose {
		return func(s string) { logger.Println(s) }
	}
	return nil
}
