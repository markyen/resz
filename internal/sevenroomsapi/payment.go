package sevenroomsapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
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
// venueID is the venue's internal id (Venue.ID), not the URL slug. The
// amount is in whole currency units and is 0 for a card-on-file hold,
// which is the only case resz supports: a slot that actually charges at
// booking goes to a human for approval instead.
func CreateSetupIntent(client *http.Client, venueID string, amount int, logger func(string)) (*SetupIntent, error) {
	if venueID == "" {
		return nil, fmt.Errorf("setup intent needs the venue's internal id")
	}
	path := fmt.Sprintf(setupIntentPathFmt, venueID)
	if logger != nil {
		logger("POST " + BaseURL + path + " (setup intent, amount " + strconv.Itoa(amount) + ")")
	}
	resp, err := postForm(client, path, map[string]string{
		"amount": strconv.Itoa(amount),
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
