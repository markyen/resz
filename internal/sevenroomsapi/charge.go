package sevenroomsapi

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
)

// checkoutDetailsPath answers the question the availability list can't:
// exactly what a slot will cost, including the service charge, tax and
// gratuity the venue layers on top of the advertised price. The widget
// reads it as soon as a slot is picked and computes every amount on the
// checkout page from it.
const checkoutDetailsPath = "/api-yoa/availability/ng/widget/checkout-details"

// ccRuleSaveForLater is the cc_payment_rule of a slot that keeps a card on
// file for a later no-show charge rather than taking money up front. Its
// counterpart, "advanced_payment", is a prepaid slot.
const ccRuleSaveForLater = "save_for_later"

// PaymentConfig is a slot's payment settings as checkout-details reports
// them. Percentages are whole numbers (8.875 means 8.875%).
type PaymentConfig struct {
	RequireCreditCard bool   `json:"require_credit_card"`
	CCPaymentRule     string `json:"cc_payment_rule"`
	// CCPartySizeMin is the party size the card requirement starts
	// *above*: a party has to be strictly larger to need one.
	CCPartySizeMin num `json:"cc_party_size_min"`

	// ChargeType says how Cost scales: "person" and "reservation" bill per
	// seat or once; the "_slot" variants also bill per 15 minutes of the
	// slot's duration.
	ChargeType string `json:"charge_type"`
	Cost       num    `json:"cost"`

	ApplyServiceCharge bool `json:"apply_service_charge"`
	ServiceCharge      num  `json:"service_charge"`

	ApplyGratuityCharge bool `json:"apply_gratuity_charge"`
	// GratuityType picks which rate applies: "DEFAULT_GRATUITY" uses
	// DefaultGratuity, "CLIENT_GRATUITY" lets the diner choose (resz
	// chooses none), and anything else uses Gratuity.
	GratuityType    string `json:"gratuity_type"`
	Gratuity        num    `json:"gratuity"`
	DefaultGratuity num    `json:"default_gratuity"`

	TaxRate num `json:"tax_rate"`

	// ComputedAutoCancellationChargeCents is the no-show fee a card-on-file
	// slot pre-authorizes on the card at booking, when the venue asks for
	// one.
	ComputedAutoCancellationChargeCents num `json:"computed_auto_cancellation_charge_cents"`
}

// Charge is what booking a slot costs, broken down the way the booking
// form reports it. Every figure is in the venue's currency and already
// rounded to cents.
type Charge struct {
	Base          float64
	ServiceCharge float64
	Tax           float64
	Gratuity      float64
	// DueNow is the total taken from the card at booking. When it is
	// non-zero the booking is paid for with a Stripe PaymentIntent.
	DueNow float64
	// PreAuthorization is a hold placed on the card for the no-show fee,
	// for a card-on-file slot that asks for one. It is not captured unless
	// the venue charges the fee. It is always zero when DueNow is not.
	PreAuthorization float64
}

// MovesMoney reports whether booking needs a PaymentIntent: something is
// either charged or held on the card. Otherwise the card is only stored,
// with a SetupIntent.
func (c Charge) MovesMoney() bool { return c.DueNow > 0 || c.PreAuthorization > 0 }

// Breakdown renders the amount due with its parts, e.g. "$141.75 ($120.00
// + $8.40 service charge + $13.35 tax)". A bare base price renders alone.
func (c Charge) Breakdown() string {
	s := fmt.Sprintf("$%.2f", c.DueNow)
	var parts []string
	for _, p := range []struct {
		amount float64
		label  string
	}{
		{c.ServiceCharge, "service charge"},
		{c.Tax, "tax"},
		{c.Gratuity, "gratuity"},
	} {
		if p.amount > 0 {
			parts = append(parts, fmt.Sprintf("$%.2f %s", p.amount, p.label))
		}
	}
	if len(parts) == 0 {
		return s
	}
	return s + fmt.Sprintf(" ($%.2f + %s)", c.Base, strings.Join(parts, " + "))
}

// ComputeCharge works out what a slot costs for partySize, from its
// payment settings and duration in minutes.
//
// It mirrors the widget's own arithmetic rather than inventing one, since
// the booking is checked against the same figures: the base price scaled
// by charge type, a service charge on the base, tax on base plus service
// charge, and gratuity on the base. Each part is rounded to cents the way
// the widget rounds. resz applies no promo code, gift card or upsell, and
// picks no gratuity where the diner may choose.
func ComputeCharge(p PaymentConfig, partySize, durationMin int) (Charge, error) {
	base, err := baseCharge(p.ChargeType, float64(p.Cost), partySize, durationMin)
	if err != nil {
		return Charge{}, err
	}
	var service float64
	if p.ApplyServiceCharge {
		service = base * float64(p.ServiceCharge) / 100
	}
	tax := (base + service) * float64(p.TaxRate) / 100
	var gratuity float64
	if p.ApplyGratuityCharge && p.GratuityType != "" && p.GratuityType != "CLIENT_GRATUITY" {
		rate := p.Gratuity
		if p.GratuityType == "DEFAULT_GRATUITY" {
			rate = p.DefaultGratuity
		}
		gratuity = base * float64(rate) / 100
	}
	c := Charge{
		Base:          roundCents(base),
		ServiceCharge: roundCents(service),
		Tax:           roundCents(tax),
		Gratuity:      roundCents(gratuity),
		DueNow:        roundCents(math.Max(base+service+tax+gratuity, 0)),
	}
	if c.DueNow == 0 && p.RequireCreditCard && p.CCPaymentRule == ccRuleSaveForLater &&
		(p.CCPartySizeMin <= 0 || partySize > int(p.CCPartySizeMin)) &&
		p.ComputedAutoCancellationChargeCents > 0 {
		c.PreAuthorization = roundCents(float64(p.ComputedAutoCancellationChargeCents) / 100)
	}
	return c, nil
}

// baseCharge is the slot's price before service charge, tax and gratuity.
// An unrecognized charge type bills nothing, as it does in the widget.
func baseCharge(chargeType string, cost float64, partySize, durationMin int) (float64, error) {
	if cost <= 0 {
		return 0, nil
	}
	if strings.HasSuffix(chargeType, "_slot") && durationMin <= 0 {
		// A zero here would silently price the slot at nothing.
		return 0, fmt.Errorf("charge type %q bills by duration, but the slot has none", chargeType)
	}
	quarterHours := float64(durationMin) / 15
	switch chargeType {
	case "person":
		return cost * float64(partySize), nil
	case "person_slot":
		return cost * float64(partySize) * quarterHours, nil
	case "reservation":
		return cost, nil
	case "reservation_slot":
		return cost * quarterHours, nil
	}
	return 0, nil
}

// roundCents rounds to the cent exactly as the widget does: half up, with
// a nudge that keeps 0.285 from landing on 0.28 through float error.
func roundCents(x float64) float64 {
	return math.Floor(x*100+1e-10+0.5) / 100
}

// formatAmount renders an amount the way the widget's form encoding does
// (JavaScript's Number#toString): "120", "37.5", "141.75".
func formatAmount(x float64) string {
	return strconv.FormatFloat(x, 'f', -1, 64)
}

// checkoutDetails is the checkout-details payload. Only the parts resz
// prices from are modeled.
type checkoutDetails struct {
	Slot struct {
		Duration int `json:"duration"`
	} `json:"slot"`
	Payment PaymentConfig `json:"payment"`
}

// SlotCharge asks the venue what ref will cost, from checkout-details.
//
// venue may be nil, in which case the slug stands in for the internal id.
// holdID is optional: the endpoint prices a slot with or without a hold,
// but refuses one that is no longer available.
func SlotCharge(client *http.Client, ref Ref, venue *Venue, holdID string, logger func(string)) (Charge, error) {
	clock, err := ref.ClockTime()
	if err != nil {
		return Charge{}, err
	}
	venueKey := ref.Venue
	if venue != nil && venue.ID != "" {
		venueKey = venue.ID
	}
	q := url.Values{}
	q.Set("venue", venueKey)
	q.Set("venue_date", ref.Date())
	q.Set("time", clock)
	q.Set("party_size", strconv.Itoa(ref.PartySize))
	q.Set("channel", Channel)
	if ref.AccessPersistentID != "" {
		q.Set("access_persistent_id", ref.AccessPersistentID)
	}
	if holdID != "" {
		q.Set("hold_id", holdID)
	}
	var d checkoutDetails
	if err := getJSON(client, checkoutDetailsPath, q, &d, logger); err != nil {
		return Charge{}, fmt.Errorf("checkout details: %w", err)
	}
	duration := d.Slot.Duration
	if duration == 0 {
		duration = ref.Duration
	}
	return ComputeCharge(d.Payment, ref.PartySize, duration)
}

// num is a JSON number that tolerates the shapes SevenRooms actually
// sends for numeric settings: a number, a numeric string, or null (read
// as zero).
type num float64

func (n *num) UnmarshalJSON(b []byte) error {
	s := strings.TrimSpace(string(b))
	if s == "null" || s == `""` {
		*n = 0
		return nil
	}
	if strings.HasPrefix(s, `"`) {
		var str string
		if err := json.Unmarshal(b, &str); err != nil {
			return err
		}
		s = strings.TrimSpace(str)
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("not a number: %s", b)
	}
	*n = num(f)
	return nil
}
