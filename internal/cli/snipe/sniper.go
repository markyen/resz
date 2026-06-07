package snipe

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"sort"
	"sync"
	"time"

	"resz/internal/resyapi"
	"resz/internal/resyflow"
	"resz/internal/venueconfig"
)

// snipeVenue runs the full snipe pipeline for one venue, executed
// concurrently per scheduled entry. Returns the booked slot and true on
// success; the zero slot and false otherwise. The pipeline is:
//
//   - sleep until targetTime - 10s
//   - warm up the HTTP/2 connection (via Details if we have a current
//     config_id; otherwise an authenticated venue GET)
//   - compute the target date in the venue's timezone
//   - sleep until targetTime
//   - fire booking attempts with prefetched book_tokens for the
//     next-best slots running concurrently with each Book call, until
//     success or 10s past targetTime.
func snipeVenue(
	ctx context.Context,
	client *http.Client,
	authInit *resyapi.CachedAuth,
	email, password string,
	entry venueconfig.ConfigEntry,
	venue *resyapi.Venue,
	currentConfigID string, // for warmup; "" if none
	targetTime time.Time,
	partySize int,
	blackout map[string]bool,
	log func(string, ...any),
) (resyapi.Slot, bool) {
	prefix := fmt.Sprintf("[%s] ", entry.Name)
	logf := func(format string, a ...any) { log(prefix+format, a...) }
	auth := authInit

	// Sleep until 10s before target_time, when warmup runs.
	warmupAt := targetTime.Add(-10 * time.Second)
	if d := time.Until(warmupAt); d > 0 {
		logf("sleeping %s until warmup at %s", d.Round(time.Millisecond), warmupAt.In(targetTime.Location()).Format("15:04:05.000 MST"))
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return resyapi.Slot{}, false
		}
	}

	// Re-check auth right before warmup — original ensure may have been
	// well over a minute ago.
	if a, err := resyapi.EnsureAuth(email, password, nil); err == nil {
		auth = a
	} else {
		logf("warmup auth refresh error: %v", err)
	}

	// Warmup. Retry once with a forced refresh on auth error.
	warmup := func(tok string) error {
		if currentConfigID != "" {
			_, err := resyapi.Details(client, tok, currentConfigID, nil)
			return err
		}
		return resyapi.GetVenueAuthed(client, tok, entry.ID, nil)
	}
	if currentConfigID != "" {
		logf("warmup via Details(%s)", currentConfigID)
	} else {
		logf("warmup via authed GET /3/venue?id=%s", entry.ID)
	}
	if err := warmup(auth.Token); err != nil {
		if resyapi.IsAuthError(err) {
			logf("warmup auth rejected, forcing refresh")
			_ = resyapi.ClearAuth()
			if a, rerr := resyapi.EnsureAuth(email, password, nil); rerr == nil {
				auth = a
				if err = warmup(auth.Token); err != nil {
					logf("warmup retry error: %v", err)
				}
			} else {
				logf("forced refresh failed: %v", rerr)
			}
		} else {
			logf("warmup error: %v", err)
		}
	}

	// Target date in the entry's timezone, derived from targetTime (the
	// fire instant) rather than time.Now() — computing it during warmup,
	// ~10s early, would roll to the wrong day for a midnight/early-morning
	// drop. targetTime already carries the venue location.
	targetDate := targetTime.AddDate(0, 0, entry.Snipe.OffsetDays).Format("2006-01-02")
	logf("target date: %s", targetDate)

	logf("right before final sleep: now=%s", time.Now().Format("15:04:05.000"))

	// Wait for target_time.
	if d := time.Until(targetTime); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return resyapi.Slot{}, false
		}
	}

	logf("FIRING at %s (target %s)", time.Now().Format("15:04:05.000"), targetTime.In(targetTime.Location()).Format("15:04:05.000 MST"))

	// Booking phase, deadline is target_time + 10s.
	deadline := targetTime.Add(10 * time.Second)
	tctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	slot, bookErr := snipeLoop(tctx, client, auth, email, password, entry, venue, targetDate, targetTime.Location(), partySize, blackout, logf)
	if bookErr != nil {
		logf("done without booking: %v", bookErr)
		return resyapi.Slot{}, false
	}
	return slot, true
}

// snipeLoop tries booking the best slot on targetDate first, then on each
// available fallback date (tomorrow → latest, ascending), retrying until
// success or the context deadline expires. While each Book is in flight,
// the next slot's book_token is fetched concurrently.
//
// Returns the booked slot on success (err == nil), or the zero slot with
// an error otherwise.
func snipeLoop(
	ctx context.Context,
	client *http.Client,
	auth *resyapi.CachedAuth,
	email, password string,
	entry venueconfig.ConfigEntry,
	venue *resyapi.Venue,
	targetDate string,
	loc *time.Location,
	partySize int,
	blackout map[string]bool,
	logf func(string, ...any),
) (resyapi.Slot, error) {
	type prep struct {
		slot      resyapi.Slot
		date      string
		bookToken string
		err       error
	}

	// Build the ordered list of candidate dates: target first (if not blacked
	// out), then any other available dates strictly after today, ascending.
	today := time.Now().In(loc).Format("2006-01-02")
	var dateOrder []string
	if !blackout[targetDate] {
		dateOrder = append(dateOrder, targetDate)
	} else {
		logf("target date %s is blacked out; falling back to other dates", targetDate)
	}
	if all, err := resyapi.AvailableDates(client, entry.ID, partySize, nil); err == nil {
		for _, d := range all {
			if d > today && d != targetDate && !blackout[d] {
				dateOrder = append(dateOrder, d)
			}
		}
		if len(dateOrder) > 1 {
			sort.Strings(dateOrder[1:])
		}
	} else {
		logf("AvailableDates: %v (continuing with target only)", err)
	}
	if len(dateOrder) > 1 {
		logf("fallback date order: %v", dateOrder[1:])
	}

	// slotsByDate caches /4/find results per date; lazy-fetched on first use.
	var (
		slotsMu     sync.Mutex
		slotsByDate = map[string][]resyapi.Slot{}
		triedMu     sync.Mutex
		tried       = map[string]bool{}
	)

	getSlots := func(d string) ([]resyapi.Slot, error) {
		slotsMu.Lock()
		if s, ok := slotsByDate[d]; ok {
			slotsMu.Unlock()
			return s, nil
		}
		slotsMu.Unlock()
		slots, err := resyapi.SlotsInWindow(client, entry.ID, d, partySize, venue, nil, nil)
		if err != nil {
			return nil, err
		}
		sortByPreference(slots, resyflow.PreferredTime)
		slotsMu.Lock()
		slotsByDate[d] = slots
		slotsMu.Unlock()
		return slots, nil
	}

	markTried := func(token string) {
		triedMu.Lock()
		tried[token] = true
		triedMu.Unlock()
	}

	// pickNext returns the next-best (date, slot) across dateOrder,
	// skipping anything already in `tried`. Does NOT mark the returned
	// slot as tried — the caller does that to coordinate prefetch.
	pickNext := func() (string, resyapi.Slot, bool) {
		for _, d := range dateOrder {
			slots, err := getSlots(d)
			if err != nil {
				logf("find %s: %v", d, err)
				continue
			}
			triedMu.Lock()
			for _, s := range slots {
				if !tried[s.Config.Token] {
					triedMu.Unlock()
					return d, s, true
				}
			}
			triedMu.Unlock()
		}
		return "", resyapi.Slot{}, false
	}

	fetchPrep := func() prep {
		d, s, ok := pickNext()
		if !ok {
			return prep{err: fmt.Errorf("no untried slots across %d dates", len(dateOrder))}
		}
		bt, err := resyapi.Details(client, auth.Token, s.Config.Token, nil)
		if err != nil {
			markTried(s.Config.Token)
			return prep{slot: s, date: d, err: fmt.Errorf("details: %w", err)}
		}
		return prep{slot: s, date: d, bookToken: bt}
	}

	// Initial prep. If the cached token went stale since warmup, the first
	// Details call returns an auth error; clear the cache, refresh once, and
	// retry synchronously here — before the concurrent firing loop, which
	// has no auth recovery of its own. Doing it before any goroutine starts
	// keeps the auth reassignment race-free.
	cur := fetchPrep()
	if cur.err != nil && resyapi.IsAuthError(cur.err) {
		logf("details auth rejected, forcing refresh")
		_ = resyapi.ClearAuth()
		if a, rerr := resyapi.EnsureAuth(email, password, nil); rerr == nil {
			auth = a
			cur = fetchPrep()
		} else {
			logf("forced refresh failed: %v", rerr)
		}
	}
	if cur.err != nil {
		return resyapi.Slot{}, cur.err
	}

	for {
		if err := ctx.Err(); err != nil {
			return resyapi.Slot{}, err
		}
		logf("attempt %s slot %s -> Book", cur.date, cur.slot.Date.Start)

		// Fire Book in a goroutine.
		bookCh := make(chan error, 1)
		go func(tok string) {
			_, err := resyapi.Book(client, auth.Token, tok, auth.PaymentMethodID, nil)
			bookCh <- err
		}(cur.bookToken)

		// Mark current slot as tried so the prefetch picks something else.
		markTried(cur.slot.Config.Token)
		nextCh := make(chan prep, 1)
		go func() { nextCh <- fetchPrep() }()

		// Wait for book to resolve.
		var bookErr error
		select {
		case bookErr = <-bookCh:
		case <-ctx.Done():
			return resyapi.Slot{}, ctx.Err()
		}

		if bookErr == nil {
			logf("BOOKED %s at %s", entry.Name, cur.slot.Date.Start)
			return cur.slot, nil
		}
		logf("book failed for %s %s: %v", cur.date, cur.slot.Date.Start, bookErr)

		// Drain prefetch result so the goroutine doesn't leak.
		var next prep
		select {
		case next = <-nextCh:
		case <-ctx.Done():
			return resyapi.Slot{}, ctx.Err()
		}
		if next.err != nil {
			logf("prefetch failed: %v", next.err)
			return resyapi.Slot{}, next.err
		}
		cur = next
	}
}

func sortByPreference(slots []resyapi.Slot, preferred string) {
	type scored struct {
		s     resyapi.Slot
		delta int64
	}
	scoredSlots := make([]scored, 0, len(slots))
	for _, s := range slots {
		// Unparseable starts sort last but are kept, so scoredSlots always
		// has the same length as slots and the write-back below covers every
		// element (no stale duplicates left in the tail).
		delta := int64(math.MaxInt64)
		if t, err := time.Parse("2006-01-02 15:04:05", s.Date.Start); err == nil {
			date := t.Format("2006-01-02")
			target, _ := time.Parse("2006-01-02 15:04", date+" "+preferred)
			delta = int64(math.Abs(float64(t.Sub(target))))
		}
		scoredSlots = append(scoredSlots, scored{s, delta})
	}
	sort.Slice(scoredSlots, func(i, j int) bool { return scoredSlots[i].delta < scoredSlots[j].delta })
	for i, sc := range scoredSlots {
		slots[i] = sc.s
	}
}
