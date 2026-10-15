package sevenroomsapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// setupIntentPathFmt is templated with the venue's internal id — not its
// slug. Note it sits under /booking/widget, a different prefix from the
// /booking/dining/widget the booking itself uses.
const setupIntentPathFmt = "/booking/widget/%s/setup_intent"

// SetupIntent is a Stripe SetupIntent the venue created on its own
// connected account, for storing a card against a reservation.
type SetupIntent struct {
	// ID is the "seti_..." identifier. It is what the booking carries in
	// its card_token field once the intent has been confirmed.
	ID string `json:"id"`
	// ClientSecret authorizes confirming this intent with a publishable
	// key, which is how the card reaches Stripe without resz ever holding
	// a Stripe secret key.
	ClientSecret string `json:"client_secret"`
	Status       string `json:"status"`
}

// CreateSetupIntent asks the venue to open a SetupIntent for a booking.
//
// venueID is the venue's internal id (Venue.ID), not the URL slug. A
// SetupIntent only stores a card, so this is for slots that charge
// nothing at booking; the widget always sends an amount of 0 here. A
// slot that charges or pre-authorizes uses CreatePaymentIntent instead.
func CreateSetupIntent(client *http.Client, venueID string, logger func(string)) (*SetupIntent, error) {
	if venueID == "" {
		return nil, fmt.Errorf("setup intent needs the venue's internal id")
	}
	path := fmt.Sprintf(setupIntentPathFmt, venueID)
	if logger != nil {
		logger("POST " + BaseURL + path + " (setup intent)")
	}
	resp, err := postForm(client, path, map[string]string{
		"amount": "0",
	}, map[string]string{
		"X-Widget-Origin": widgetOrigin,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &apiError{Status: resp.StatusCode, Body: bookMessage(resp.StatusCode, raw)}
	}
	// This endpoint answers with the Stripe object directly rather than
	// the {status, data} envelope the api-yoa endpoints use.
	var si SetupIntent
	if err := json.Unmarshal(raw, &si); err != nil {
		return nil, fmt.Errorf("decode setup intent: %w", err)
	}
	if si.ID == "" || si.ClientSecret == "" {
		return nil, fmt.Errorf("setup intent response carried no id or client secret: %s", truncate(raw))
	}
	if logger != nil {
		logger("setup intent " + si.ID + " (" + si.Status + ")")
	}
	return &si, nil
}

// paymentIntentPathFmt is the PaymentIntent counterpart of
// setupIntentPathFmt, keyed the same way on the venue's internal id.
const paymentIntentPathFmt = "/booking/widget/%s/payment_intent"

// diningChargeSource is the `source` the widget tags a dining payment
// with: base64 of "DINING", verbatim from the widget's own constant.
const diningChargeSource = "RElOSU5H"

// PaymentIntent is a Stripe PaymentIntent the venue created on its own
// connected account to take or hold money for a booking.
type PaymentIntent struct {
	// ClientSecret authorizes confirming (and reading) the intent with a
	// publishable key. Its prefix is the "pi_..." id.
	ClientSecret string `json:"client_secret"`
}

// CreatePaymentIntent asks the venue to open a PaymentIntent for a
// booking, either to charge c.DueNow or, for a card-on-file slot with a
// no-show fee, to pre-authorize c.PreAuthorization.
//
// Amounts go over in the venue's currency units as the widget formats
// them ("141.75"), not in cents. The venue decides the PaymentIntent's
// amount from them; ReserveWithCard reads it back from Stripe before
// confirming, so a disagreement is caught before any money moves.
func CreatePaymentIntent(client *http.Client, venueID string, c Charge, logger func(string)) (*PaymentIntent, error) {
	if venueID == "" {
		return nil, fmt.Errorf("payment intent needs the venue's internal id")
	}
	fields := map[string]string{"source": diningChargeSource}
	switch {
	case c.DueNow > 0:
		fields["amount"] = formatAmount(c.DueNow)
	case c.PreAuthorization > 0:
		fields["amount"] = formatAmount(c.PreAuthorization)
		fields["pre_authorization"] = "true"
	default:
		return nil, fmt.Errorf("payment intent needs an amount; a free slot uses a setup intent")
	}
	path := fmt.Sprintf(paymentIntentPathFmt, venueID)
	if logger != nil {
		desc := "charge"
		if fields["pre_authorization"] != "" {
			desc = "pre-authorization"
		}
		logger("POST " + BaseURL + path + " (payment intent, " + desc + " " + fields["amount"] + ")")
	}
	resp, err := postForm(client, path, fields, map[string]string{
		"X-Widget-Origin": widgetOrigin,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &apiError{Status: resp.StatusCode, Body: bookMessage(resp.StatusCode, raw)}
	}
	pi, err := parsePaymentIntent(raw)
	if err != nil {
		return nil, err
	}
	if logger != nil {
		id, _, _ := strings.Cut(pi.ClientSecret, "_secret_")
		logger("payment intent " + id)
	}
	return pi, nil
}

// parsePaymentIntent reads the payment_intent response. Unlike the setup
// intent endpoint, this one wraps the Stripe object in "payload"; a bare
// object is accepted too in case the two are ever brought into line.
func parsePaymentIntent(raw []byte) (*PaymentIntent, error) {
	var out struct {
		Payload PaymentIntent `json:"payload"`
		PaymentIntent
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode payment intent: %w", err)
	}
	pi := out.Payload
	if pi.ClientSecret == "" {
		pi = out.PaymentIntent
	}
	if !strings.HasPrefix(pi.ClientSecret, "pi_") || !strings.Contains(pi.ClientSecret, "_secret_") {
		return nil, fmt.Errorf("payment intent response carried no client secret: %s", truncate(raw))
	}
	return &pi, nil
}
