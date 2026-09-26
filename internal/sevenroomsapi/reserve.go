package sevenroomsapi

import (
	"fmt"
	"net/http"

	"resz/internal/cardsecret"
	"resz/internal/stripeapi"
)

// ErrPaymentRequired is returned for a slot the venue will only hold
// against a credit card when no card is configured.
var ErrPaymentRequired = fmt.Errorf("this slot needs a card on file and none is configured")

// Reserve runs the full SevenRooms booking sequence for one slot:
// hold the table, submit the guest's details, and release the hold if
// the booking fails.
//
// The hold matters for more than politeness. Without it the table can be
// taken between availability and checkout, and the book call fails in a
// way that looks like a bug; with it, the failure is a clean "slot no
// longer available" from the hold.
//
// A hold left dangling would block the table until it lapses (about five
// minutes), so it is released on every failure path.
func Reserve(client *http.Client, ref Ref, guest Guest, logger func(string)) (*Reservation, error) {
	return ReserveWithCard(client, ref, guest, nil, logger)
}

// ReserveWithCard is Reserve for a slot the venue will only hold against
// a card. card may be nil, in which case a card-required slot is refused
// with ErrPaymentRequired exactly as before.
//
// For a card slot the sequence gains two steps before the booking: the
// venue opens a Stripe SetupIntent, and the card is confirmed against it
// with the venue's publishable key. Only the resulting SetupIntent id
// reaches SevenRooms; the card goes to Stripe and nowhere else.
//
// Slots that actually charge at booking are refused outright. resz only
// stores a card against a table — spending money is a decision for a
// person, not for a scheduled job.
func ReserveWithCard(client *http.Client, ref Ref, guest Guest, card *cardsecret.Card, logger func(string)) (*Reservation, error) {
	if ref.RequiresPayment {
		if card == nil {
			return nil, ErrPaymentRequired
		}
		if total := ref.ChargeTotal(); total > 0 {
			return nil, fmt.Errorf("%w: this slot charges $%.2f at booking", ErrChargeAtBooking, total)
		}
	}
	// party_size is optional on a ref but mandatory at the venue, and a
	// zero would be sent verbatim. Catch it before the hold.
	if ref.PartySize < 1 {
		return nil, fmt.Errorf("slot reference has no party size: add party_size to the sr:// reference")
	}
	if ref.Venue == "" {
		return nil, fmt.Errorf("slot reference has no venue")
	}
	if err := guest.Validate(); err != nil {
		return nil, err
	}

	// venue_info is cheap and gives the internal venue id the booking
	// form carries. A failure here isn't fatal: the slug in the URL is
	// what actually routes the request.
	venue, err := GetVenue(client, ref.Venue, logger)
	if err != nil {
		if logger != nil {
			logger("venue lookup failed, booking with the slug alone: " + err.Error())
		}
		venue = nil
	}

	hold, err := CreateHold(client, ref, logger)
	if err != nil {
		return nil, fmt.Errorf("hold: %w", err)
	}
	if logger != nil {
		logger(fmt.Sprintf("held table (id %s) for %ds", hold.ID, hold.DurationSec))
	}

	// With a card in hand, set it up before booking. Any failure here has
	// to release the hold, or the table stays blocked until it lapses.
	var cardToken string
	if ref.RequiresPayment && card != nil {
		cardToken, err = setUpCard(client, venue, card, logger)
		if err != nil {
			if rerr := ReleaseHold(client, ref, hold, logger); rerr != nil && logger != nil {
				logger("failed to release hold " + hold.ID + ": " + rerr.Error())
			}
			return nil, err
		}
	}

	res, err := BookWithCard(client, ref, venue, guest, hold, cardToken, logger)
	if err != nil {
		if rerr := ReleaseHold(client, ref, hold, logger); rerr != nil && logger != nil {
			logger("failed to release hold " + hold.ID + ": " + rerr.Error())
		}
		return nil, fmt.Errorf("book: %w", err)
	}
	return res, nil
}

// ErrChargeAtBooking marks a slot that takes money up front. resz stores
// cards; it does not spend, so these are always a human's call.
var ErrChargeAtBooking = fmt.Errorf("slot charges at booking")

// setUpCard opens a SetupIntent on the venue and confirms the card
// against it, returning the SetupIntent id the booking carries.
func setUpCard(client *http.Client, venue *Venue, card *cardsecret.Card, logger func(string)) (string, error) {
	if venue == nil || venue.ID == "" {
		return "", fmt.Errorf("cannot set up a card without the venue's internal id (venue_info lookup failed)")
	}
	if !venue.TakesPayment() {
		return "", fmt.Errorf("venue %s publishes no Stripe key (payment_system %q); its card bookings have to go through the widget",
			venue.Slug, venue.PaymentSystem)
	}
	// Amount 0: a card-on-file hold. A charging slot never reaches here.
	si, err := CreateSetupIntent(client, venue.ID, 0, logger)
	if err != nil {
		return "", fmt.Errorf("setup intent: %w", err)
	}
	res, err := stripeapi.ConfirmSetupIntent(client, card, si.ClientSecret, venue.PaymentPublicToken, venue.PaymentAccountID, logger)
	if err != nil {
		return "", fmt.Errorf("card setup: %w", err)
	}
	return res.ID, nil
}
