package sevenroomsapi

import (
	"fmt"
	"math"
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
	return ReserveWithCard(client, ref, guest, nil, 0, logger)
}

// ReserveWithCard is Reserve for a slot the venue will only hold against
// a card. card may be nil, in which case a card-required slot is refused
// with ErrPaymentRequired.
//
// For a card slot the sequence gains steps between the hold and the
// booking, mirroring the venue's own widget. The slot is priced from
// checkout-details, which includes the service charge, tax and gratuity
// the advertised price leaves out. Then the card goes to Stripe, and only
// Stripe's intent id reaches SevenRooms:
//
//   - A slot that charges nothing at booking stores the card with a
//     SetupIntent.
//   - A slot that charges, or that pre-authorizes a no-show fee, is paid
//     with a PaymentIntent. Its amount is read back from Stripe and
//     checked against the quote before the card is confirmed, which is
//     the step that moves money.
//
// authorizedUSD is the largest charge a person has explicitly approved
// for this one booking. Zero — what every unattended caller passes —
// means no charge is authorized at all, so a slot wanting money is
// refused with ErrChargeAtBooking and goes to a human. A slot charging
// more than authorizedUSD is refused with the same error, so a price that
// moved between approval and booking is never quietly paid. The check
// runs twice: on the advertised price before anything else, and on the
// full price once the venue has quoted it. A pre-authorization is a hold
// for a fee charged only on a no-show, like a stored card, and needs no
// authorization.
func ReserveWithCard(client *http.Client, ref Ref, guest Guest, card *cardsecret.Card, authorizedUSD float64, logger func(string)) (*Reservation, error) {
	if ref.RequiresPayment {
		if card == nil {
			return nil, ErrPaymentRequired
		}
		if total := ref.ChargeTotal(); total > 0 {
			if err := checkAuthorized(fmt.Sprintf("$%.2f", total), total, authorizedUSD); err != nil {
				return nil, err
			}
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

	// Refuse an undrivable gateway before holding anything. Discovering it
	// after the hold would block a real table for the five minutes it takes
	// to lapse, for a booking that was never going to complete.
	if err := gatewayRefusal(ref, card, venue); err != nil {
		return nil, err
	}

	hold, err := CreateHold(client, ref, logger)
	if err != nil {
		return nil, fmt.Errorf("hold: %w", err)
	}
	if logger != nil {
		logger(fmt.Sprintf("held table (id %s) for %ds", hold.ID, hold.DurationSec))
	}
	// Every failure from here on has to release the hold, or the table
	// stays blocked until it lapses.
	release := func() {
		if rerr := ReleaseHold(client, ref, hold, logger); rerr != nil && logger != nil {
			logger("failed to release hold " + hold.ID + ": " + rerr.Error())
		}
	}

	var (
		charge    Charge
		cardToken string
		payment   *stripeapi.PaymentIntentResult
	)
	if ref.RequiresPayment && card != nil {
		charge, err = priceHeldSlot(client, ref, venue, hold, logger)
		if err == nil && charge.DueNow > 0 {
			err = checkAuthorized(charge.Breakdown(), charge.DueNow, authorizedUSD)
		}
		if err == nil {
			cardToken, payment, err = setUpCard(client, venue, card, charge, logger)
		}
		if err != nil {
			release()
			return nil, err
		}
	}

	res, err := BookWithCard(client, ref, venue, guest, hold, cardToken, charge, logger)
	if err != nil {
		release()
		if payment != nil {
			// The money moved before the booking was refused. That is the
			// widget's order too, but it leaves something only the venue
			// can undo, so say exactly what and where.
			return nil, fmt.Errorf("book: %w (the card was already %s; ask the venue to refund or release Stripe payment %s)",
				err, moneyMoved(charge), payment.ID)
		}
		return nil, fmt.Errorf("book: %w", err)
	}
	res.Charge = charge
	if payment != nil {
		res.PaymentID = payment.ID
	}
	return res, nil
}

// priceHeldSlot asks the venue what the held slot costs.
//
// A slot advertised with a price is never paid for without this answer:
// paying the advertised figure would leave out the tax and fees the venue
// expects, and the booking would be refused after the card was charged. A
// card-on-file slot falls back to storing the card, which is what resz
// did before it read checkout-details at all.
func priceHeldSlot(client *http.Client, ref Ref, venue *Venue, hold *Hold, logger func(string)) (Charge, error) {
	charge, err := SlotCharge(client, ref, venue, hold.ID, logger)
	if err != nil {
		if ref.ChargeTotal() > 0 {
			return Charge{}, fmt.Errorf("could not confirm what this slot costs, so nothing was charged: %w", err)
		}
		if logger != nil {
			logger("could not price the slot (" + err.Error() + "); storing the card without a charge")
		}
		return Charge{}, nil
	}
	if logger != nil {
		switch {
		case charge.DueNow > 0:
			logger("venue quotes " + charge.Breakdown() + " due at booking")
		case charge.PreAuthorization > 0:
			logger(fmt.Sprintf("venue pre-authorizes $%.2f for a no-show", charge.PreAuthorization))
		default:
			logger("venue charges nothing at booking; storing the card")
		}
	}
	return charge, nil
}

// checkAuthorized refuses a charge above what a person approved. desc is
// how the charge is described in the refusal. Amounts compare in whole
// cents, so an approval of "141.75" covers a charge of 141.75 exactly.
func checkAuthorized(desc string, total, authorizedUSD float64) error {
	if cents(total) <= cents(authorizedUSD) {
		return nil
	}
	if authorizedUSD <= 0 {
		return fmt.Errorf("%w: this slot charges %s at booking", ErrChargeAtBooking, desc)
	}
	return fmt.Errorf("%w: this slot charges %s, above the $%.2f authorized", ErrChargeAtBooking, desc, authorizedUSD)
}

func cents(x float64) int64 { return int64(math.Round(x * 100)) }

// moneyMoved says what happened to the card, for an error raised after
// the PaymentIntent was confirmed.
func moneyMoved(c Charge) string {
	if c.DueNow > 0 {
		return fmt.Sprintf("charged $%.2f", c.DueNow)
	}
	return fmt.Sprintf("pre-authorized for $%.2f", c.PreAuthorization)
}

// ErrChargeAtBooking marks a slot that takes money up front. resz stores
// cards; it does not spend, so these are always a human's call.
var ErrChargeAtBooking = fmt.Errorf("slot charges at booking")

// ErrGatewayUnsupported marks a venue whose payment gateway resz cannot
// drive. Only Stripe is supported, and that is unlikely to change.
//
// FreedomPay — the other gateway seen on SevenRooms — captures the card
// in i4Go hosted fields served from FreedomPay's own domain, and its
// /api-yoa/payments/<venue>/authorize call carries an invisible reCAPTCHA
// token alongside a device risk-session id. Supplying those from a
// non-browser client would mean defeating a bot-detection control, so
// resz does not try. These bookings belong in the venue's widget, with a
// person present.
var ErrGatewayUnsupported = fmt.Errorf("payment gateway not supported; book this one in the venue's widget")

// gatewayRefusal reports whether this booking is doomed by the venue's
// payment gateway, so the caller can refuse before taking a hold. It
// returns nil when there is nothing to pay, no card to pay with, or the
// gateway is one resz can drive — and nil when the venue is unknown,
// since the booking may still be a free one.
func gatewayRefusal(ref Ref, card *cardsecret.Card, venue *Venue) error {
	if !ref.RequiresPayment || card == nil || venue == nil || venue.TakesPayment() {
		return nil
	}
	return fmt.Errorf("%w: %s is on %s", ErrGatewayUnsupported, venue.Slug, gatewayName(venue.PaymentSystem))
}

// gatewayName renders a venue's gateway for an error message, naming the
// empty case rather than printing "".
func gatewayName(system string) string {
	if system == "" {
		return "an unnamed gateway"
	}
	return system
}

// setUpCard puts the card behind a booking and returns the Stripe intent
// id the booking carries as its card_token: a SetupIntent for a slot that
// charges nothing, a PaymentIntent for one that charges or pre-authorizes.
// payment is set only when the PaymentIntent was confirmed, which is the
// point after which money has moved.
func setUpCard(client *http.Client, venue *Venue, card *cardsecret.Card, charge Charge, logger func(string)) (token string, payment *stripeapi.PaymentIntentResult, err error) {
	if venue == nil || venue.ID == "" {
		return "", nil, fmt.Errorf("cannot set up a card without the venue's internal id (venue_info lookup failed)")
	}
	if !venue.TakesPayment() {
		return "", nil, fmt.Errorf("%w: %s is on %s, not Stripe", ErrGatewayUnsupported, venue.Slug, gatewayName(venue.PaymentSystem))
	}
	pk, acct := venue.PaymentPublicToken, venue.PaymentAccountID

	if !charge.MovesMoney() {
		si, err := CreateSetupIntent(client, venue.ID, logger)
		if err != nil {
			return "", nil, fmt.Errorf("setup intent: %w", err)
		}
		res, err := stripeapi.ConfirmSetupIntent(client, card, si.ClientSecret, pk, acct, logger)
		if err != nil {
			return "", nil, fmt.Errorf("card setup: %w", err)
		}
		return res.ID, nil, nil
	}

	pi, err := CreatePaymentIntent(client, venue.ID, charge, logger)
	if err != nil {
		return "", nil, fmt.Errorf("payment intent: %w", err)
	}
	// Read back what the venue actually asked Stripe for before handing
	// over the card. Nothing has moved yet, so a disagreement costs only
	// the hold.
	info, err := stripeapi.RetrievePaymentIntent(client, pi.ClientSecret, pk, acct, logger)
	if err != nil {
		return "", nil, fmt.Errorf("payment intent: %w", err)
	}
	if err := checkIntentAmount(info.AmountCents, charge); err != nil {
		return "", nil, err
	}
	res, err := stripeapi.ConfirmPaymentIntent(client, card, pi.ClientSecret, pk, acct, logger)
	if err != nil {
		return "", nil, fmt.Errorf("payment: %w", err)
	}
	return res.ID, res, nil
}

// checkIntentAmount refuses a PaymentIntent for more than the quote. A
// smaller amount is allowed: the quote is what was authorized, and paying
// less never exceeds it.
func checkIntentAmount(amountCents int64, charge Charge) error {
	want := charge.DueNow
	if want == 0 {
		want = charge.PreAuthorization
	}
	if amountCents > cents(want) {
		return fmt.Errorf("%w: the venue asked Stripe for $%.2f, more than the $%.2f quoted; nothing was charged",
			ErrChargeAtBooking, float64(amountCents)/100, want)
	}
	return nil
}
