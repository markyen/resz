package snipe

import (
	"context"
	"fmt"
	"net/http"
	"sort"
	"time"

	"resz/internal/bookflow"
	"resz/internal/platform"
	"resz/internal/sevenroomsapi"
	"resz/internal/venueconfig"
)

// snipeSevenRooms is the SevenRooms counterpart to snipeVenue: wait for
// the venue's drop time, then race to book the best slot on the target
// date.
//
// The shape differs from Resy's because the APIs do. Resy hands out a
// stable config_id that can be turned into a book_token ahead of time, so
// snipeVenue prefetches tokens and fires Book repeatedly. SevenRooms has
// no such token — a slot only becomes bookable via a hold taken moments
// before checkout — so the loop here is: re-read availability, hold, book,
// and on failure move to the next-best slot. There is nothing useful to
// prefetch, which also means nothing to invalidate when the drop lands.
func snipeSevenRooms(
	ctx context.Context,
	client *http.Client,
	guest sevenroomsapi.Guest,
	entry venueconfig.ConfigEntry,
	targetTime time.Time,
	partySize int,
	blackout map[string]bool,
	log func(string, ...any),
) (platform.Slot, bool) {
	prefix := fmt.Sprintf("[%s] ", entry.Name)
	logf := func(format string, a ...any) { log(prefix+format, a...) }

	// Sleep until shortly before the drop, then warm the connection so
	// the first real request doesn't pay for TLS.
	warmupAt := targetTime.Add(-10 * time.Second)
	if d := time.Until(warmupAt); d > 0 {
		logf("sleeping %s until warmup at %s", d.Round(time.Millisecond),
			warmupAt.In(targetTime.Location()).Format("15:04:05.000 MST"))
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return platform.Slot{}, false
		}
	}

	vc, err := sevenroomsapi.Open(client, entry.ID, nil)
	if err != nil {
		logf("warmup venue lookup: %v", err)
		// Not fatal on its own — the booking path re-resolves the venue —
		// but it usually means the drop attempt will fail too.
	}

	// The target date is derived from the fire instant, not from
	// time.Now(): computing it during warmup, ~10s early, would roll to
	// the wrong day for a midnight drop.
	targetDate := targetTime.AddDate(0, 0, entry.Snipe.OffsetDays).Format(platform.DateLayout)
	logf("target date: %s", targetDate)

	if d := time.Until(targetTime); d > 0 {
		select {
		case <-time.After(d):
		case <-ctx.Done():
			return platform.Slot{}, false
		}
	}
	logf("FIRING at %s (target %s)", time.Now().Format("15:04:05.000"),
		targetTime.In(targetTime.Location()).Format("15:04:05.000 MST"))

	if vc == nil {
		vc, err = sevenroomsapi.Open(client, entry.ID, nil)
		if err != nil {
			logf("venue lookup: %v", err)
			return platform.Slot{}, false
		}
	}

	deadline := targetTime.Add(10 * time.Second)
	tctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()

	slot, err := sevenRoomsSnipeLoop(tctx, client, vc, guest, targetDate, partySize, blackout, logf)
	if err != nil {
		logf("done without booking: %v", err)
		return platform.Slot{}, false
	}
	return slot, true
}

// sevenRoomsSnipeLoop tries the target date first, then any other
// bookable date, taking each candidate slot in preference order until one
// books or the deadline passes.
//
// Every attempt re-reads availability rather than trusting a cached view:
// at a drop the whole day's inventory appears at once, and a slot list
// fetched even seconds earlier is likely to be empty or stale.
func sevenRoomsSnipeLoop(
	ctx context.Context,
	client *http.Client,
	vc *sevenroomsapi.Client,
	guest sevenroomsapi.Guest,
	targetDate string,
	partySize int,
	blackout map[string]bool,
	logf func(string, ...any),
) (platform.Slot, error) {
	dateOrder := []string{targetDate}
	if blackout[targetDate] {
		logf("target date %s is blacked out; falling back to other dates", targetDate)
		dateOrder = nil
	}
	if all, err := bookflow.FilteredDates(vc, partySize, blackout, nil); err == nil {
		var rest []string
		for _, d := range all {
			if d != targetDate {
				rest = append(rest, d)
			}
		}
		sort.Strings(rest)
		dateOrder = append(dateOrder, rest...)
	} else {
		logf("available dates: %v (continuing with the target date only)", err)
	}
	if len(dateOrder) == 0 {
		return platform.Slot{}, fmt.Errorf("no candidate dates")
	}

	tried := map[string]bool{}
	for _, date := range dateOrder {
		for {
			if err := ctx.Err(); err != nil {
				return platform.Slot{}, err
			}
			// Ask the venue afresh: a drop publishes inventory the
			// previous read couldn't have seen.
			slots, err := freshSlots(client, vc, date, partySize)
			if err != nil {
				logf("find %s: %v", date, err)
				break
			}
			platform.SortByPreference(slots, bookflow.PreferredTime)

			next, ok := firstUntried(slots, tried)
			if !ok {
				break // nothing left on this date; move to the next
			}
			tried[next.Ref] = true

			ref, err := sevenroomsapi.ParseRef(next.Ref)
			if err != nil {
				logf("skipping unparseable slot %s: %v", next.Ref, err)
				continue
			}
			if ref.RequiresPayment {
				logf("skipping %s: %v", next.StartLabel(), sevenroomsapi.ErrPaymentRequired)
				continue
			}
			logf("attempt %s slot %s -> hold+book", date, next.StartLabel())
			if _, err := sevenroomsapi.Reserve(client, ref, guest, nil); err != nil {
				logf("book failed for %s %s: %v", date, next.StartLabel(), err)
				continue
			}
			logf("BOOKED %s", next.StartLabel())
			return next, nil
		}
	}
	return platform.Slot{}, fmt.Errorf("no bookable slot across %d dates", len(dateOrder))
}

// freshSlots reads a date's slots bypassing the venue client's cache,
// which exists to make ordinary availability scans cheap and would
// otherwise hand back a pre-drop snapshot.
func freshSlots(client *http.Client, vc *sevenroomsapi.Client, date string, partySize int) ([]platform.Slot, error) {
	byDate, err := sevenroomsapi.Slots(client, vc.ID(), date, partySize, nil)
	if err != nil {
		return nil, err
	}
	return byDate[date], nil
}

func firstUntried(slots []platform.Slot, tried map[string]bool) (platform.Slot, bool) {
	for _, s := range slots {
		if !tried[s.Ref] {
			return s, true
		}
	}
	return platform.Slot{}, false
}
