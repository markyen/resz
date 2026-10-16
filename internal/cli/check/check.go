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

	"resz/internal/approval"
	"resz/internal/bookflow"
	"resz/internal/cliflag"
	"resz/internal/notify"
	"resz/internal/platform"
	"resz/internal/reslog"
	"resz/internal/resyapi"
	"resz/internal/sevenroomsapi"
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
		fmt.Fprintf(os.Stderr, "usage: resz check [-c PATH] [-v] [-f] [--notify=false]\n")
		cliflag.PrintDefaults(fs)
	}
	if err := fs.Parse(cliflag.Expand(fs, args)); err != nil {
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

venues:
	for i, v := range cfg.Venues {
		if v.Check == nil {
			continue
		}
		if v.ID == "" {
			lg.Printf("[%s] missing id, skipping", v.Name)
			continue
		}
		p, perr := v.Platform()
		if perr != nil {
			lg.Printf("[%s] %v, skipping", v.Name, perr)
			continue
		}
		partySize := v.PartySize
		if partySize == 0 {
			partySize = 2
		}
		lg.Printf("[%s] checking availability (%s id=%s party=%d)", v.Name, p, v.ID, partySize)

		vc, verr := bookflow.Open(p, nil, v.ID, v.City, nil)
		if verr != nil {
			lg.Printf("[%s] venue lookup error: %v", v.Name, verr)
			checkErr = verr
			break
		}
		dates, ferr := bookflow.FilteredDates(vc, partySize, blackout, nil)
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
		var window *platform.Window
		if r := v.Check.TimeRange; r != nil {
			window = &platform.Window{Earliest: r.Start, Latest: r.End}
		}
		cands := bookflow.NewCandidates(vc, partySize, dates, window, nil)
		skippedAddOns := 0
		for {
			slot, ok, ferr := cands.Next()
			if ferr != nil {
				lg.Printf("[%s] availability error: %v", v.Name, ferr)
				checkErr = ferr
				break venues
			}
			if !ok {
				if skippedAddOns > 0 {
					lg.Printf("[%s] no slots in target window without required add-ons", v.Name)
				} else {
					lg.Printf("[%s] no slots in target window", v.Name)
				}
				continue venues
			}
			// A slot the venue will only hold against a card normally goes to
			// a human first: the charge is real money and often
			// non-refundable. The exception is a venue marked auto_book_card
			// whose slot charges nothing at booking. Either way this is a
			// skip rather than a failure, so the remaining venues still get
			// their turn and health stays green.
			if sevenroomsapi.IsRef(slot.Ref) {
				if ref, perr := sevenroomsapi.ParseRef(slot.Ref); perr == nil && ref.RequiresPayment {
					if !mayAutoBookCard(v, ref) {
						if total := ref.ChargeTotal(); total > 0 && v.AutoBookCard {
							// Worth saying out loud: the venue is marked for
							// automatic booking but has started charging, so
							// the interlock sent it to a human instead.
							lg.Printf("[%s] auto_book_card is set but %s now charges $%.2f; asking anyway",
								v.Name, slot.StartLabel(), total)
						}
						lg.Printf("[%s] %s needs a card; requesting approval", v.Name, slot.StartLabel())
						if *notifyFlag && s.ShouldNotifyApproval(slot.Ref, now) {
							sendApprovalRequest(lg, v.Name, slot, ref)
						}
						continue venues
					}
					lg.Printf("[%s] %s needs a card but charges nothing at booking; auto-booking per auto_book_card",
						v.Name, slot.StartLabel())
				}
			}

			lg.Printf("[%s] bookable ref=%s; attempting booking", v.Name, slot.Ref)
			_, berr := bookflow.Book(nil, slot,
				os.Getenv("RESY_EMAIL"), os.Getenv("RESY_PASSWORD"), nil)
			if berr != nil {
				// A slot needing add-ons (a preorder, a takeout order) can't
				// be booked by resz at all. That says nothing about the
				// venue's other slot types, so pass over every slot of this
				// one and try the next-best.
				if errors.Is(berr, resyapi.ErrAddOnsRequired) {
					lg.Printf("[%s] %s: %q slots require add-ons resz can't select; skipping them",
						v.Name, slot.StartLabel(), slot.Label)
					cands.Exclude(slot)
					skippedAddOns++
					continue
				}
				// A Resy slot that takes a deposit or prepayment: check
				// authorizes nothing, so the charge goes to a person,
				// quoted, and the other venues still get their turn.
				var ce *resyapi.ChargeError
				if errors.As(berr, &ce) {
					lg.Printf("[%s] %v; requesting approval", v.Name, berr)
					if *notifyFlag && s.ShouldNotifyApproval(slot.Ref, now) {
						approval.SendResy(lg, v.Name, slot, partySize, ce.Charge)
					}
					continue venues
				}
				// Three cases are a human's to handle rather than a failed run:
				// no card is configured, the venue is on a gateway resz
				// cannot drive, or the venue's checkout quoted a charge the
				// advertised price didn't show. All ask for approval and let
				// the rest of the venues have their turn.
				if errors.Is(berr, sevenroomsapi.ErrPaymentRequired) ||
					errors.Is(berr, sevenroomsapi.ErrGatewayUnsupported) ||
					errors.Is(berr, sevenroomsapi.ErrChargeAtBooking) {
					lg.Printf("[%s] %v; falling back to approval", v.Name, berr)
					if ref, perr := sevenroomsapi.ParseRef(slot.Ref); perr == nil {
						if *notifyFlag && s.ShouldNotifyApproval(slot.Ref, now) {
							sendApprovalRequest(lg, v.Name, slot, ref)
						}
					}
					continue venues
				}
				lg.Printf("[%s] booking error: %v", v.Name, berr)
				checkErr = berr
				break venues
			}
			bookedVenue = v.Name
			bookedWhen = slot.StartLabel()
			// Block the booked date from future attempts.
			if cfg.AddBlackout(slot.Date()) {
				lg.Printf("[%s] added %s to blackout_dates", v.Name, slot.Date())
			}
			// Remove the booked venue from venues.json. Errors here are
			// non-fatal — the booking is already confirmed at this point.
			cfg.Venues = append(cfg.Venues[:i], cfg.Venues[i+1:]...)
			if err := venueconfig.Save(cfgPath, cfg); err != nil {
				lg.Printf("[%s] failed to save venues.json: %v", v.Name, err)
			} else {
				lg.Printf("[%s] removed from venues.json", v.Name)
			}
			break venues
		}
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

// sendApprovalRequest asks the human to authorize a card booking; see
// approval.Message for what the request says.
func sendApprovalRequest(lg interface{ Printf(string, ...any) }, venue string, slot platform.Slot, ref sevenroomsapi.Ref) {
	approval.Send(lg, nil, venue, slot, ref)
}

// mayAutoBookCard reports whether a card-required slot can be booked
// without asking; see approval.MayAutoBook.
func mayAutoBookCard(v venueconfig.ConfigEntry, ref sevenroomsapi.Ref) bool {
	return approval.MayAutoBook(v, ref)
}
