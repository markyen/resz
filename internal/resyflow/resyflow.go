// Package resyflow composes higher-level operations on top of
// internal/resyapi: filtering the calendar, finding the best bookable
// slot, and authenticating + booking. The CLI subcommands (search,
// availability, book, check, snipe) call into here so the multi-API-call
// dances live in exactly one place.
package resyflow

import (
	"fmt"
	"net/http"
	"time"

	"resz/internal/resyapi"
)

// PreferredTime is the target time for slot selection, expressed as "HH:MM"
// in the venue's local timezone. BestBookableSlot picks the slot whose
// start time is nearest to this.
const PreferredTime = "19:00"

// FilteredDates returns the venue's available dates strictly after today,
// excluding any in `blackout`. The order is whatever the calendar API
// returns (typically ascending).
func FilteredDates(client *http.Client, venueID string, partySize int, blackout map[string]bool, logger func(string)) ([]string, error) {
	dates, err := resyapi.AvailableDates(client, venueID, partySize, logger)
	if err != nil {
		return nil, fmt.Errorf("calendar: %w", err)
	}
	today := time.Now().Format("2006-01-02")
	out := dates[:0]
	for _, d := range dates {
		if d > today && !blackout[d] {
			out = append(out, d)
		}
	}
	return out, nil
}

// BestBookableSlot returns the best slot (nearest to PreferredTime) on
// the earliest of `dates` that has any slot in the target window. If
// venue is nil it is fetched. Pass window=nil for the default
// weekday/weekend window.
//
// `dates` is typically the result of FilteredDates. The caller controls
// the ordering and may supply additional pre-filtering. Pass an empty
// or nil slice to short-circuit to ok=false.
//
// ok=false with no error means the slice was exhausted without finding
// a slot in the configured window. Any API error during the search
// propagates.
func BestBookableSlot(client *http.Client, venueID string, partySize int, venue *resyapi.Venue, dates []string, window *resyapi.Window, logger func(string)) (date string, slot resyapi.Slot, ok bool, err error) {
	if len(dates) == 0 {
		return "", resyapi.Slot{}, false, nil
	}
	if venue == nil {
		v, verr := resyapi.GetVenue(client, venueID)
		if verr != nil {
			return "", resyapi.Slot{}, false, fmt.Errorf("venue: %w", verr)
		}
		venue = v
	}
	for _, d := range dates {
		slots, ferr := resyapi.SlotsInWindow(client, venueID, d, partySize, venue, window, logger)
		if ferr != nil {
			return "", resyapi.Slot{}, false, fmt.Errorf("find %s: %w", d, ferr)
		}
		idx := resyapi.BestSlotIndex(slots, PreferredTime)
		if idx < 0 {
			continue
		}
		return d, slots[idx], true, nil
	}
	return "", resyapi.Slot{}, false, nil
}

// AuthAndBook performs the auth → details → book sequence used by both
// `resz book` and `resz check` after a slot has been identified. If the
// cached token is rejected at /3/details with a 401/419, the cache is
// cleared and one refresh + retry is attempted before giving up.
//
// Returns the raw response body from /3/book on success, or the body
// (which may be nil) plus a wrapped error on failure.
func AuthAndBook(client *http.Client, configID, email, password string, logger func(string)) ([]byte, error) {
	auth, err := resyapi.EnsureAuth(email, password, logger)
	if err != nil {
		return nil, fmt.Errorf("auth: %w", err)
	}
	bookToken, err := resyapi.Details(client, auth.Token, configID, logger)
	if err != nil {
		if resyapi.IsAuthError(err) {
			if logger != nil {
				logger("details: auth rejected, forcing refresh")
			}
			_ = resyapi.ClearAuth()
			auth, err = resyapi.EnsureAuth(email, password, logger)
			if err != nil {
				return nil, fmt.Errorf("auth refresh: %w", err)
			}
			bookToken, err = resyapi.Details(client, auth.Token, configID, logger)
		}
		if err != nil {
			return nil, fmt.Errorf("details: %w", err)
		}
	}
	body, err := resyapi.Book(client, auth.Token, bookToken, auth.PaymentMethodID, logger)
	if err != nil {
		return body, fmt.Errorf("book: %w", err)
	}
	return body, nil
}
