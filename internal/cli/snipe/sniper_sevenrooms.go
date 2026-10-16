package snipe

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"

	"resz/internal/approval"
	"resz/internal/bookflow"
	"resz/internal/cardsecret"
	"resz/internal/platform"
	"resz/internal/sevenroomsapi"
	"resz/internal/state"
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
	card *cardsecret.Card,
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

	slot, needsApproval, err := sevenRoomsSnipeLoop(tctx, client, vc, guest, card, entry, targetDate, partySize, blackout, logf)
	if err != nil {
		logf("done without booking: %v", err)
		// Nothing resz could book alone, but a card slot it passed over is
		// worth a person's decision — and slots at a drop go in minutes,
		// so ask now rather than leaving it to check's next pass.
		if needsApproval != nil {
			requestApproval(client, entry.Name, *needsApproval, logf)
		}
		return platform.Slot{}, false
	}
	return slot, true
}

// requestApproval asks a person to approve a card slot, unless it was
// asked about recently; see firstAsk.
func requestApproval(client *http.Client, venue string, slot platform.Slot, logf func(string, ...any)) {
	ref, err := sevenroomsapi.ParseRef(slot.Ref)
	if err != nil {
		logf("approval for %s: %v", slot.Ref, err)
		return
	}
	if !firstAsk(slot, logf) {
		return
	}
	approval.Send(logger, client, venue, slot, ref)
}

// firstAsk reports whether slot is due an approval request, and records
// the ask. The cooldown is the one check keeps, in the same state file,
// so the two never ask twice about one slot.
func firstAsk(slot platform.Slot, logf func(string, ...any)) bool {
	// A state file that can't be read is no reason to stay quiet: a
	// duplicate request is cheaper than a missed slot.
	st, err := state.Load()
	if err != nil {
		logf("state load: %v (asking anyway)", err)
		return true
	}
	if !st.ShouldNotifyApproval(slot.Ref, time.Now()) {
		logf("%s needs approval; already asked within %s", slot.StartLabel(), state.ApprovalCooldown)
		return false
	}
	if err := state.Save(st); err != nil {
		logf("state save: %v", err)
	}
	return true
}

// sevenRoomsSnipeLoop tries the target date first, then any other
// bookable date, taking each candidate slot in preference order until one
// books or the deadline passes.
//
// Every attempt re-reads availability rather than trusting a cached view:
// at a drop the whole day's inventory appears at once, and a slot list
// fetched even seconds earlier is likely to be empty or stale.
//
// A slot that needs a card is booked only when the venue is marked
// auto_book_card and the slot charges nothing, as in check. Otherwise it
// is passed over in favour of a slot resz can book alone, and the best one
// passed over comes back as needsApproval for the caller to ask about.
func sevenRoomsSnipeLoop(
	ctx context.Context,
	client *http.Client,
	vc *sevenroomsapi.Client,
	guest sevenroomsapi.Guest,
	card *cardsecret.Card,
	entry venueconfig.ConfigEntry,
	targetDate string,
	partySize int,
	blackout map[string]bool,
	logf func(string, ...any),
) (booked platform.Slot, needsApproval *platform.Slot, err error) {
	// passOver remembers the first (so best) card slot that needs a
	// person.
	passOver := func(s platform.Slot, why string) {
		logf("passing over %s: %s", s.StartLabel(), why)
		if needsApproval == nil {
			needsApproval = &s
		}
	}
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
		return platform.Slot{}, nil, fmt.Errorf("no candidate dates")
	}

	tried := map[string]bool{}
	for _, date := range dateOrder {
		for {
			if err := ctx.Err(); err != nil {
				return platform.Slot{}, needsApproval, err
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
			var useCard *cardsecret.Card
			if ref.RequiresPayment {
				switch {
				case !approval.MayAutoBook(entry, ref):
					passOver(next, "it needs a card and a person's approval")
					continue
				case card == nil:
					passOver(next, sevenroomsapi.ErrPaymentRequired.Error())
					continue
				}
				useCard = card
				logf("attempt %s slot %s -> hold+book with card (auto_book_card)", date, next.StartLabel())
			} else {
				logf("attempt %s slot %s -> hold+book", date, next.StartLabel())
			}
			// Unattended, so nothing is authorized: a slot whose checkout
			// turns out to charge is refused before any money moves.
			if _, err := sevenroomsapi.ReserveWithCard(client, ref, guest, useCard, 0, nil); err != nil {
				if errors.Is(err, sevenroomsapi.ErrChargeAtBooking) || errors.Is(err, sevenroomsapi.ErrGatewayUnsupported) {
					passOver(next, err.Error())
					continue
				}
				logf("book failed for %s %s: %v", date, next.StartLabel(), err)
				continue
			}
			logf("BOOKED %s", next.StartLabel())
			return next, nil, nil
		}
	}
	return platform.Slot{}, needsApproval, fmt.Errorf("no bookable slot across %d dates", len(dateOrder))
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
