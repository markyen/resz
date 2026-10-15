// Package approval asks a person to approve a SevenRooms booking that
// resz will not make on its own: one that needs a card, at a venue not
// marked for automatic card booking, or one that charges money.
//
// The request is a message carrying the slot reference verbatim, what
// booking it will cost, and what a reply of APPROVE will do. Whatever acts
// on the reply (the booking-approval skill) books it with `resz book`,
// passing --authorize with the figure the message quoted.
package approval

import (
	"errors"
	"fmt"
	"net/http"
	"strings"

	"resz/internal/cardsecret"
	"resz/internal/notify"
	"resz/internal/platform"
	"resz/internal/sevenroomsapi"
	"resz/internal/venueconfig"
)

// Situation is what the message depends on beyond the slot itself.
type Situation struct {
	// Charge is the venue's quote, and Exact whether there was one. A
	// slot that could not be priced falls back to its advertised price,
	// which leaves out any tax or fees.
	Charge sevenroomsapi.Charge
	Exact  bool
	// HaveCard is whether resz has a card to book with.
	HaveCard bool
	// Gateway names the venue's payment system when resz cannot pay
	// through it, and is empty otherwise.
	Gateway string
}

// CanBook reports whether a reply of APPROVE lets resz book the slot
// itself, rather than the person booking it in the venue's widget.
func (s Situation) CanBook() bool { return s.HaveCard && s.Gateway == "" }

// Assess prices the slot and checks resz can pay for it. Each part fails
// soft: a venue that can't be looked up or priced still gets a request,
// just a less specific one.
func Assess(client *http.Client, ref sevenroomsapi.Ref, logger func(string)) Situation {
	var sit Situation
	if _, err := cardsecret.FromEnv(); err == nil {
		sit.HaveCard = true
	}
	venue, err := sevenroomsapi.GetVenue(client, ref.Venue, logger)
	if err != nil {
		venue = nil
	}
	if venue != nil && !venue.TakesPayment() {
		sit.Gateway = venue.PaymentSystem
		if sit.Gateway == "" {
			sit.Gateway = "an unnamed payment system"
		}
	}
	if c, err := sevenroomsapi.SlotCharge(client, ref, venue, "", logger); err == nil {
		sit.Charge, sit.Exact = c, true
	} else if logger != nil {
		logger("could not price " + ref.String() + ": " + err.Error())
	}
	return sit
}

// Message builds the request, and returns separately a short description
// of the cost for a log line.
//
// The reference is the last line and is included verbatim, so the
// booking that follows cannot drift to a different slot. When resz can
// book it and the venue quoted a charge, an "authorize:" line just above
// it gives the exact figure an APPROVE covers.
func Message(venue string, slot platform.Slot, ref sevenroomsapi.Ref, sit Situation) (msg, cost string) {
	cost = costText(ref, sit)

	var action string
	switch {
	case sit.Gateway != "":
		action = fmt.Sprintf("resz can't pay through %s, this venue's payment system, so book it yourself in the venue's widget: %s",
			sit.Gateway, ref.WidgetURL())
	case !sit.HaveCard:
		action = "resz has no card configured, so book it yourself in the venue's widget: " + ref.WidgetURL()
	case sit.Exact && sit.Charge.DueNow > 0:
		action = fmt.Sprintf("Reply APPROVE and resz will book it with your card, charging $%.2f.", sit.Charge.DueNow)
	case !sit.Exact && ref.ChargeTotal() > 0:
		action = "Reply APPROVE and resz will get the exact total from the venue and check it with you before charging anything."
	default:
		action = "Reply APPROVE and resz will book it with your card on file."
	}

	var b strings.Builder
	fmt.Fprintf(&b, "%s: %s on %s, party of %d, %s.\n\n", venue, slot.StartLabel(), ref.Date(), ref.PartySize, cost)
	b.WriteString(action)
	b.WriteString(" Or ignore this message to let it go.\n\n")
	if sit.CanBook() && sit.Exact && sit.Charge.DueNow > 0 {
		fmt.Fprintf(&b, "authorize: %.2f\n", sit.Charge.DueNow)
	}
	b.WriteString("ref: " + slot.Ref)
	return b.String(), cost
}

// costText says what booking costs, as precisely as the venue allowed.
func costText(ref sevenroomsapi.Ref, sit Situation) string {
	c := sit.Charge
	switch {
	case sit.Exact && c.DueNow > 0:
		return "charges " + c.Breakdown() + " at booking"
	case sit.Exact && c.PreAuthorization > 0:
		return fmt.Sprintf("needs a card on file and holds $%.2f on it for the no-show fee (charged only on a no-show)",
			c.PreAuthorization)
	case sit.Exact:
		return "needs a card on file (nothing charged at booking)"
	}
	total := ref.ChargeTotal()
	if total <= 0 {
		return "needs a card on file (no amount advertised)"
	}
	s := fmt.Sprintf("charges at least $%.2f", total)
	if ref.ChargeType == "person" && ref.PartySize > 1 {
		s += fmt.Sprintf(" ($%.2f x %d)", ref.Cost, ref.PartySize)
	}
	return s + " at booking, plus any tax or fees the venue adds"
}

// Logger is the part of *log.Logger a request logs through.
type Logger interface{ Printf(string, ...any) }

// Send assesses the slot and sends the request through the configured
// notify transport. Cooldown is the caller's business: this always sends.
func Send(lg Logger, client *http.Client, venue string, slot platform.Slot, ref sevenroomsapi.Ref) {
	msg, cost := Message(venue, slot, ref, Assess(client, ref, nil))
	switch err := notify.Send(msg); {
	case err == nil:
		lg.Printf("[%s] approval requested (%s)", venue, cost)
	case errors.Is(err, notify.ErrNoTransport):
		lg.Printf("[%s] approval needed (%s) but no notify transport is configured", venue, cost)
	default:
		lg.Printf("[%s] approval notify failed: %v", venue, err)
	}
}

// MayAutoBook reports whether a card-required slot can be booked without
// asking. Both halves matter: the venue has to be marked for it, and the
// slot has to advertise no charge at booking. The second half is an
// interlock, not a formality — it means a venue that switches from a
// card-on-file hold to a real deposit starts asking again on its own,
// with no config change. (A charge the venue adds only at checkout is
// caught later: an unattended booking authorizes nothing, so it is
// refused before any money moves.)
//
// It says nothing about what the venue may bill later. These slots keep a
// card on file precisely so they can charge a no-show fee.
func MayAutoBook(v venueconfig.ConfigEntry, ref sevenroomsapi.Ref) bool {
	return v.AutoBookCard && ref.ChargeTotal() == 0
}
