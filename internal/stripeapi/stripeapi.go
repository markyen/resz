// Package stripeapi turns a card into a single-use Stripe token.
//
// SevenRooms venues that take payment do it through Stripe Connect: the
// venue publishes a publishable key and its connected account id, and the
// browser widget exchanges the card for a token against those before the
// booking is submitted. Only the token reaches SevenRooms. This package
// does the same exchange server-side.
//
// A publishable key ("pk_live_...") is safe to hold and is the only
// credential involved — it can create tokens and nothing else. resz never
// sees, needs, or stores a Stripe secret key.
//
// Nothing here logs the card. The logger argument, where present,
// receives the endpoint and the resulting token id only.
package stripeapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"resz/internal/cardsecret"
)

// BaseURL is Stripe's API host. It is a variable so tests can point at a
// local server; nothing else should reassign it.
var BaseURL = "https://api.stripe.com"

const tokensPath = "/v1/tokens"

// Token is a single-use Stripe card token.
type Token struct {
	// ID is the "tok_..." value handed onward to the venue.
	ID string `json:"id"`
	// Last4 and Brand identify the card the token stands for, so a
	// confirmation can say which card was charged.
	Last4 string
	Brand string
}

// Tokenize exchanges card for a single-use token against the venue's
// Stripe account.
//
// publishableKey is the venue's "pk_..." value and connectedAccount its
// "acct_..." id; the latter may be empty for a venue that bills directly.
// The returned token is valid for a single charge and expires quickly, so
// it is created immediately before the booking that consumes it.
func Tokenize(client *http.Client, card *cardsecret.Card, publishableKey, connectedAccount string, logger func(string)) (*Token, error) {
	if card == nil {
		return nil, cardsecret.ErrNoCard
	}
	if !strings.HasPrefix(publishableKey, "pk_") {
		return nil, fmt.Errorf("stripe publishable key %q does not look like a pk_ key", truncateKey(publishableKey))
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	form := url.Values{}
	form.Set("card[number]", card.Number())
	form.Set("card[exp_month]", strconv.Itoa(card.ExpMonth()))
	form.Set("card[exp_year]", strconv.Itoa(card.ExpYear()))
	form.Set("card[cvc]", card.CVC())
	if z := card.PostalCode(); z != "" {
		form.Set("card[address_zip]", z)
	}
	if n := card.Name(); n != "" {
		form.Set("card[name]", n)
	}

	req, err := http.NewRequest("POST", BaseURL+tokensPath, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+publishableKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if connectedAccount != "" {
		// Tokens must be created on the account that will charge them; a
		// token minted on the platform account is rejected at charge time.
		req.Header.Set("Stripe-Account", connectedAccount)
	}
	// Deliberately not logging the body: it is the card.
	if logger != nil {
		logger("POST " + BaseURL + tokensPath + " (tokenize " + card.Redacted() + ")")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var out struct {
		ID   string `json:"id"`
		Card struct {
			Last4 string `json:"last4"`
			Brand string `json:"brand"`
		} `json:"card"`
		Error struct {
			Message string `json:"message"`
			Code    string `json:"code"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal(body, &out); jerr != nil {
		return nil, fmt.Errorf("stripe returned %d with an unreadable body", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 {
		msg := out.Error.Message
		if msg == "" {
			msg = "no message"
		}
		// Stripe's decline messages are written for cardholders and say
		// nothing sensitive, so they are safe to surface verbatim.
		return nil, fmt.Errorf("stripe declined the card (%s): %s", out.Error.Code, msg)
	}
	if out.ID == "" {
		return nil, fmt.Errorf("stripe returned no token id")
	}
	if logger != nil {
		logger("stripe token " + out.ID)
	}
	return &Token{ID: out.ID, Last4: out.Card.Last4, Brand: out.Card.Brand}, nil
}

// truncateKey keeps an unusable key out of an error message in full.
func truncateKey(k string) string {
	if len(k) > 12 {
		return k[:12] + "..."
	}
	return k
}

// SetupIntentResult is the outcome of confirming a SetupIntent.
type SetupIntentResult struct {
	// ID is the "seti_..." identifier. This is what SevenRooms wants in
	// the booking's card_token field — not the payment method id, and not
	// a card token.
	ID string
	// PaymentMethod is the "pm_..." Stripe created for the card.
	PaymentMethod string
	// Status is Stripe's own word for where the intent ended up;
	// "succeeded" is the only one that can back a booking.
	Status string
}

// ConfirmSetupIntent attaches card to an existing SetupIntent, which is
// how a venue stores a card against a reservation without charging it.
//
// clientSecret comes from the venue's own setup-intent endpoint;
// publishableKey and connectedAccount come from its venue_info. The
// SetupIntent id this returns is what the booking carries.
//
// Note on bot detection: the browser widget also sends
// radar_options[hcaptcha_token], a token produced by an hCaptcha
// challenge for Stripe's fraud scoring. resz does not send it and will
// not fabricate one. If Stripe or the venue requires it, that booking has
// to be completed by a person in the widget — see docs/sevenrooms-payment.md.
func ConfirmSetupIntent(client *http.Client, card *cardsecret.Card, clientSecret, publishableKey, connectedAccount string, logger func(string)) (*SetupIntentResult, error) {
	out, err := confirmIntent(client, "setup_intents", "seti_", card, clientSecret, publishableKey, connectedAccount, logger)
	if err != nil {
		return nil, err
	}
	if out.Status != "succeeded" {
		return nil, fmt.Errorf("setup intent ended in status %q, not succeeded", out.Status)
	}
	if logger != nil {
		logger("setup intent " + out.ID + " succeeded (" + out.PaymentMethod + ")")
	}
	return &SetupIntentResult{ID: out.ID, PaymentMethod: out.PaymentMethod, Status: out.Status}, nil
}

// PaymentIntentResult is the outcome of confirming a PaymentIntent.
type PaymentIntentResult struct {
	// ID is the "pi_..." identifier, which the booking carries in its
	// card_token field the same way a SetupIntent id would.
	ID     string
	Status string
	// AmountCents is what Stripe charged or held, in the currency's minor
	// unit.
	AmountCents int64
	Currency    string
}

// PaymentIntentInfo is a PaymentIntent as read back before confirming.
type PaymentIntentInfo struct {
	ID          string
	AmountCents int64
	Currency    string
	Status      string
}

// RetrievePaymentIntent reads a PaymentIntent with its client secret,
// which is how a caller checks the amount the venue actually asked Stripe
// for before handing over a card. It moves no money.
func RetrievePaymentIntent(client *http.Client, clientSecret, publishableKey, connectedAccount string, logger func(string)) (*PaymentIntentInfo, error) {
	if !strings.HasPrefix(publishableKey, "pk_") {
		return nil, fmt.Errorf("stripe publishable key %q does not look like a pk_ key", truncateKey(publishableKey))
	}
	id, _, ok := strings.Cut(clientSecret, "_secret_")
	if !ok || !strings.HasPrefix(id, "pi_") {
		return nil, fmt.Errorf("client secret does not look like a PaymentIntent secret")
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}
	q := url.Values{}
	q.Set("client_secret", clientSecret)
	q.Set("key", publishableKey)
	if connectedAccount != "" {
		q.Set("_stripe_account", connectedAccount)
	}
	path := "/v1/payment_intents/" + url.PathEscape(id)
	req, err := http.NewRequest("GET", BaseURL+path+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+publishableKey)
	if connectedAccount != "" {
		req.Header.Set("Stripe-Account", connectedAccount)
	}
	if logger != nil {
		logger("GET " + BaseURL + path)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out struct {
		ID       string `json:"id"`
		Amount   int64  `json:"amount"`
		Currency string `json:"currency"`
		Status   string `json:"status"`
		Error    struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if jerr := json.Unmarshal(body, &out); jerr != nil {
		return nil, fmt.Errorf("stripe returned %d with an unreadable body", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("stripe would not read payment intent %s: %s", id, out.Error.Message)
	}
	return &PaymentIntentInfo{ID: out.ID, AmountCents: out.Amount, Currency: out.Currency, Status: out.Status}, nil
}

// ConfirmPaymentIntent pays an existing PaymentIntent with card. This is
// the step that moves money: a charge is captured, a pre-authorization is
// held.
//
// clientSecret comes from the venue's payment-intent endpoint, and the
// keys from its venue_info, exactly as for ConfirmSetupIntent. Callers
// should check the amount with RetrievePaymentIntent first.
func ConfirmPaymentIntent(client *http.Client, card *cardsecret.Card, clientSecret, publishableKey, connectedAccount string, logger func(string)) (*PaymentIntentResult, error) {
	out, err := confirmIntent(client, "payment_intents", "pi_", card, clientSecret, publishableKey, connectedAccount, logger)
	if err != nil {
		return nil, err
	}
	// "requires_capture" is a successful pre-authorization, or a charge
	// on a venue that captures later; either way the card is good for it.
	if out.Status != "succeeded" && out.Status != "requires_capture" {
		return nil, fmt.Errorf("payment intent ended in status %q, not succeeded", out.Status)
	}
	if logger != nil {
		logger(fmt.Sprintf("payment intent %s %s (%d %s)", out.ID, out.Status, out.Amount, out.Currency))
	}
	return &PaymentIntentResult{ID: out.ID, Status: out.Status, AmountCents: out.Amount, Currency: out.Currency}, nil
}

// intentResponse is the part of a confirmed Setup- or PaymentIntent that
// resz reads.
type intentResponse struct {
	ID            string `json:"id"`
	Status        string `json:"status"`
	PaymentMethod string `json:"payment_method"`
	Amount        int64  `json:"amount"`
	Currency      string `json:"currency"`
	NextAction    any    `json:"next_action"`
	Error         struct {
		Message string `json:"message"`
		Code    string `json:"code"`
	} `json:"error"`
}

// confirmIntent confirms a Setup- or PaymentIntent with card. kind is the
// API collection ("setup_intents") and idPrefix the id prefix its client
// secrets carry ("seti_"). The request is the same for both: only the
// path differs.
func confirmIntent(client *http.Client, kind, idPrefix string, card *cardsecret.Card, clientSecret, publishableKey, connectedAccount string, logger func(string)) (*intentResponse, error) {
	if card == nil {
		return nil, cardsecret.ErrNoCard
	}
	if !strings.HasPrefix(publishableKey, "pk_") {
		return nil, fmt.Errorf("stripe publishable key %q does not look like a pk_ key", truncateKey(publishableKey))
	}
	id, _, ok := strings.Cut(clientSecret, "_secret_")
	if !ok || !strings.HasPrefix(id, idPrefix) {
		return nil, fmt.Errorf("client secret does not look like a %s secret", kind)
	}
	if client == nil {
		client = &http.Client{Timeout: 20 * time.Second}
	}

	form := url.Values{}
	form.Set("client_secret", clientSecret)
	form.Set("key", publishableKey)
	form.Set("expected_payment_method_type", "card")
	form.Set("use_stripe_sdk", "false")
	form.Set("payment_method_data[type]", "card")
	form.Set("payment_method_data[card][number]", card.Number())
	form.Set("payment_method_data[card][cvc]", card.CVC())
	form.Set("payment_method_data[card][exp_month]", strconv.Itoa(card.ExpMonth()))
	form.Set("payment_method_data[card][exp_year]", strconv.Itoa(card.ExpYear()))
	if z := card.PostalCode(); z != "" {
		form.Set("payment_method_data[billing_details][address][postal_code]", z)
		form.Set("payment_method_data[billing_details][address][country]", "US")
	}
	if connectedAccount != "" {
		form.Set("_stripe_account", connectedAccount)
	}

	path := "/v1/" + kind + "/" + url.PathEscape(id) + "/confirm"
	req, err := http.NewRequest("POST", BaseURL+path, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+publishableKey)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	if connectedAccount != "" {
		req.Header.Set("Stripe-Account", connectedAccount)
	}
	// The body is the card; it is never logged.
	if logger != nil {
		logger("POST " + BaseURL + path + " (confirm with " + card.Redacted() + ")")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var out intentResponse
	if jerr := json.Unmarshal(body, &out); jerr != nil {
		return nil, fmt.Errorf("stripe returned %d with an unreadable body", resp.StatusCode)
	}
	if resp.StatusCode/100 != 2 {
		msg := out.Error.Message
		if msg == "" {
			msg = "no message"
		}
		return nil, fmt.Errorf("stripe refused the card (%s): %s", out.Error.Code, msg)
	}
	if out.NextAction != nil {
		// 3-D Secure and friends need a human in a browser. Say so plainly
		// rather than leaving a half-set-up intent behind.
		return nil, fmt.Errorf("this card needs extra verification (3-D Secure); complete the booking in the venue's widget")
	}
	return &out, nil
}
