package stripeapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"resz/internal/cardsecret"
)

const testEntry = `4242424242424242
exp: 12/2088
cvc: 123
zip: 10014
name: Jane Doe`

func testCard(t *testing.T) *cardsecret.Card {
	t.Helper()
	c, err := cardsecret.Parse(testEntry)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestTokenizeSendsTheCardCorrectly(t *testing.T) {
	var gotForm url.Values
	var gotAuth, gotAccount string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		gotForm, _ = url.ParseQuery(string(body))
		gotAuth = r.Header.Get("Authorization")
		gotAccount = r.Header.Get("Stripe-Account")
		w.Header().Set("Content-Type", "application/json")
		io.WriteString(w, `{"id":"tok_abc123","card":{"last4":"4242","brand":"Visa"}}`)
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = old }()

	tok, err := Tokenize(srv.Client(), testCard(t), "pk_live_xyz", "acct_123", nil)
	if err != nil {
		t.Fatalf("Tokenize: %v", err)
	}
	if tok.ID != "tok_abc123" || tok.Last4 != "4242" || tok.Brand != "Visa" {
		t.Errorf("token = %+v", tok)
	}
	if gotAuth != "Bearer pk_live_xyz" {
		t.Errorf("Authorization = %q", gotAuth)
	}
	if gotAccount != "acct_123" {
		t.Errorf("Stripe-Account = %q, want the connected account", gotAccount)
	}
	for k, want := range map[string]string{
		"card[number]":      "4242424242424242",
		"card[exp_month]":   "12",
		"card[exp_year]":    "2088",
		"card[cvc]":         "123",
		"card[address_zip]": "10014",
		"card[name]":        "Jane Doe",
	} {
		if got := gotForm.Get(k); got != want {
			t.Errorf("%s = %q, want %q", k, got, want)
		}
	}
}

func TestTokenizeOmitsStripeAccountWhenUnset(t *testing.T) {
	var seen bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, seen = r.Header["Stripe-Account"]
		io.WriteString(w, `{"id":"tok_1"}`)
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = old }()

	if _, err := Tokenize(srv.Client(), testCard(t), "pk_test_1", "", nil); err != nil {
		t.Fatal(err)
	}
	if seen {
		t.Error("Stripe-Account should be absent when no connected account is given")
	}
}

func TestTokenizeSurfacesDeclines(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(402)
		io.WriteString(w, `{"error":{"message":"Your card was declined.","code":"card_declined","type":"card_error"}}`)
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = old }()

	_, err := Tokenize(srv.Client(), testCard(t), "pk_live_x", "", nil)
	if err == nil {
		t.Fatal("expected a decline error")
	}
	if !strings.Contains(err.Error(), "card_declined") || !strings.Contains(err.Error(), "declined") {
		t.Errorf("error should explain the decline: %v", err)
	}
}

func TestTokenizeRejectsNonPublishableKey(t *testing.T) {
	_, err := Tokenize(nil, testCard(t), "sk_live_secret_do_not_use", "", nil)
	if err == nil || !strings.Contains(err.Error(), "pk_") {
		t.Fatalf("a secret key must be refused, got %v", err)
	}
	if strings.Contains(err.Error(), "secret_do_not_use") {
		t.Errorf("the key must not be echoed in full: %v", err)
	}
}

func TestTokenizeWithoutCard(t *testing.T) {
	if _, err := Tokenize(nil, nil, "pk_live_x", "", nil); err != cardsecret.ErrNoCard {
		t.Errorf("Tokenize(nil card) = %v, want ErrNoCard", err)
	}
}

// The logger is the most likely accidental leak path, so it gets its own
// test: it must see the endpoint and the redacted card, never the digits.
func TestLoggerNeverSeesTheCard(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"id":"tok_1","card":{"last4":"4242"}}`)
	}))
	defer srv.Close()
	old := BaseURL
	BaseURL = srv.URL
	defer func() { BaseURL = old }()

	var logged []string
	_, err := Tokenize(srv.Client(), testCard(t), "pk_live_x", "", func(m string) { logged = append(logged, m) })
	if err != nil {
		t.Fatal(err)
	}
	all := strings.Join(logged, "\n")
	if all == "" {
		t.Fatal("expected the logger to be called")
	}
	for _, secret := range []string{"4242424242424242", "card[number]", "cvc=123", "card[cvc]"} {
		if strings.Contains(all, secret) {
			t.Errorf("logger saw %q in: %s", secret, all)
		}
	}
}
