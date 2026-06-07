// Package check implements the `resz check` subcommand: a single
// scheduled availability + auto-book pass with exponential backoff and
// optional health-flip / booking notifications.
package check

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"time"

	"resz/internal/notify"
	"resz/internal/reslog"
	"resz/internal/resyapi"
	"resz/internal/resyflow"
	"resz/internal/state"
	"resz/internal/venueconfig"
)

// Main runs the check subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz check", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configFlag := fs.String("c", "", "venues config path (default <workspace>/config/venues.json)")
	verboseFlag := fs.Bool("v", false, "echo log output to stderr as well as the log file")
	forceFlag := fs.Bool("f", false, "bypass cooldown")
	notifyFlag := fs.Bool("notify", true, "send notification on health flips and booking success")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz check [-c PATH] [-v] [-f] [-notify=false]\n")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	lg, lf, err := reslog.Open("check", *verboseFlag)
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

	s, err := state.Load()
	if err != nil {
		lg.Printf("FATAL state load: %v", err)
		return 1
	}
	now := time.Now()

	if in, remaining := s.InCooldown(now); in && !*forceFlag {
		lg.Printf("cooldown active; %s remaining (backoff %s, errors=%d)",
			remaining.Round(time.Second), state.FormatBackoff(s.CurrentBackoffSec), s.ErrorCount)
		return 0
	}

	cfg, err := venueconfig.Load(cfgPath)
	if err != nil {
		lg.Printf("FATAL load %s: %v", cfgPath, err)
		return 1
	}
	if len(cfg.Venues) == 0 {
		lg.Printf("no venues configured; nothing to do")
		return 0
	}
	blackout := venueconfig.BlackoutSet(cfg.BlackoutDates)

	var (
		bookedVenue string
		bookedWhen  string
		checkErr    error
	)

	for i, v := range cfg.Venues {
		if v.Check == nil {
			continue
		}
		if v.ID == "" {
			lg.Printf("[%s] missing id, skipping", v.Name)
			continue
		}
		partySize := v.PartySize
		if partySize == 0 {
			partySize = 2
		}
		lg.Printf("[%s] checking availability (id=%s party=%d)", v.Name, v.ID, partySize)

		dates, ferr := resyflow.FilteredDates(nil, v.ID, partySize, blackout, nil)
		if ferr != nil {
			lg.Printf("[%s] availability error: %v", v.Name, ferr)
			checkErr = ferr
			break
		}
		if r := v.Check.DateRange; r != nil {
			if derr := validateDateRange(r.Start, r.End); derr != nil {
				lg.Printf("[%s] %v; skipping", v.Name, derr)
				continue
			}
			dates = filterByDateRange(dates, r.Start, r.End)
		}
		var window *resyapi.Window
		if r := v.Check.TimeRange; r != nil {
			window = &resyapi.Window{Earliest: r.Start, Latest: r.End}
		}
		_, slot, ok, ferr := resyflow.BestBookableSlot(nil, v.ID, partySize, nil, dates, window, nil)
		if ferr != nil {
			lg.Printf("[%s] availability error: %v", v.Name, ferr)
			checkErr = ferr
			break
		}
		if !ok {
			lg.Printf("[%s] no slots in target window", v.Name)
			continue
		}
		configID := slot.Config.Token
		lg.Printf("[%s] bookable config_id=%s; attempting booking", v.Name, configID)
		_, berr := resyflow.AuthAndBook(nil, configID,
			os.Getenv("RESY_EMAIL"), os.Getenv("RESY_PASSWORD"), nil)
		if berr != nil {
			lg.Printf("[%s] booking error: %v", v.Name, berr)
			checkErr = berr
			break
		}
		bookedVenue = v.Name
		bookedWhen = slot.StartLabel()
		// Block the booked date from future attempts.
		if t, err := time.Parse("2006-01-02 15:04:05", slot.Date.Start); err == nil {
			if d := t.Format("2006-01-02"); cfg.AddBlackout(d) {
				lg.Printf("[%s] added %s to blackout_dates", v.Name, d)
			}
		}
		// Remove the booked venue from venues.json. Errors here are
		// non-fatal — the booking is already confirmed at this point.
		cfg.Venues = append(cfg.Venues[:i], cfg.Venues[i+1:]...)
		if err := venueconfig.Save(cfgPath, cfg); err != nil {
			lg.Printf("[%s] failed to save venues.json: %v", v.Name, err)
		} else {
			lg.Printf("[%s] removed from venues.json", v.Name)
		}
		break
	}

	var prevHealth state.HealthState
	if checkErr != nil {
		prevHealth = s.RecordError(now)
	} else {
		prevHealth = s.RecordSuccess(now)
	}
	if err := state.Save(s); err != nil {
		lg.Printf("state save: %v", err)
	}

	if *notifyFlag {
		sendNotifications(lg, bookedVenue, bookedWhen, checkErr, prevHealth, s)
	}

	if checkErr != nil {
		lg.Printf("FAIL: %v (next backoff %s)", checkErr, state.FormatBackoff(s.CurrentBackoffSec))
		return 1
	}
	lg.Printf("ok")
	return 0
}

// sendNotifications fires alerts on three transitions:
//   - booking success: always notify
//   - health flip ok→error: notify with backoff info
//   - health flip error→ok: notify recovery
//
// Transport is configured in config/notify.json; a missing or empty
// config is treated as "skip silently".
func sendNotifications(lg interface{ Printf(string, ...any) }, bookedVenue, bookedWhen string, checkErr error, prev state.HealthState, s *state.State) {
	send := func(tag, msg string) {
		err := notify.Send(msg)
		switch {
		case err == nil:
			lg.Printf("notified %s", tag)
		case errors.Is(err, notify.ErrNoTransport):
			lg.Printf("notify %s skipped: no transport configured", tag)
		default:
			lg.Printf("notify %s: %v", tag, err)
		}
	}
	if bookedVenue != "" {
		send("booking "+bookedVenue,
			fmt.Sprintf("booked %s for %s", bookedVenue, bookedWhen))
	}
	switch {
	case checkErr != nil && prev != state.HealthError:
		send("health ok→error",
			fmt.Sprintf("resz: api unhealthy (%s); cooldown %s",
				trim(checkErr.Error(), 120), state.FormatBackoff(s.CurrentBackoffSec)))
	case checkErr == nil && prev == state.HealthError:
		send("health error→ok", "resz: api healthy again")
	}
}

// validateDateRange checks that a check.date_range is well-formed: each
// non-empty bound parses as YYYY-MM-DD and start is not after end. An empty
// bound means unbounded on that side. A malformed or inverted range would
// otherwise filter out every date and masquerade as "no availability".
func validateDateRange(start, end string) error {
	if start != "" {
		if _, err := time.Parse("2006-01-02", start); err != nil {
			return fmt.Errorf("invalid date_range start %q: want YYYY-MM-DD", start)
		}
	}
	if end != "" {
		if _, err := time.Parse("2006-01-02", end); err != nil {
			return fmt.Errorf("invalid date_range end %q: want YYYY-MM-DD", end)
		}
	}
	if start != "" && end != "" && start > end {
		return fmt.Errorf("invalid date_range: start %q is after end %q", start, end)
	}
	return nil
}

// filterByDateRange returns the subset of YYYY-MM-DD dates that fall in
// [start, end] inclusive. Empty start or end is treated as unbounded on
// that side, so {"start": "2026-07-01"} matches everything from July 1
// onward.
func filterByDateRange(dates []string, start, end string) []string {
	out := dates[:0]
	for _, d := range dates {
		if start != "" && d < start {
			continue
		}
		if end != "" && d > end {
			continue
		}
		out = append(out, d)
	}
	return out
}

func trim(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(truncated)"
}
