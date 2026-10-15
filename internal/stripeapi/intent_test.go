package stripeapi

import (
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// stripeStub serves one canned response and records the request.
type stripeStub struct {
	method, path string
	query, form  url.Values
}

func serveStripe(t *testing.T, status int, body string) (*stripeStub, *http.Client) {
	t.Helper()
	got := &stripeStub{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got.method, got.path, got.query = r.Method, r.URL.Path, r.URL.Query()
		b, _ := io.ReadAll(r.Body)
		got.form, _ = url.ParseQuery(string(b))
		w.WriteHeader(status)
		io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	old := BaseURL
	BaseURL = srv.URL
	t.Cleanup(func() { BaseURL = old })
	return got, srv.Client()
}

func TestConfirmPaymentIntentCharges(t *testing.T) {
	got, client := serveStripe(t, 200, `{"id":"pi_123","status":"succeeded","amount":14175,"currency":"usd"}`)
	res, err := ConfirmPaymentIntent(client, testCard(t), "pi_123_secret_abc", "pk_live_x", "acct_9", nil)
	if err != nil {
		t.Fatalf("ConfirmPaymentIntent: %v", err)
	}
	if res.ID != "pi_123" || res.AmountCents != 14175 || res.Status != "succeeded" {
		t.Errorf("result = %+v", res)
	}
	if got.path != "/v1/payment_intents/pi_123/confirm" {
		t.Errorf("path = %q", got.path)
	}
	for k, want := range map[string]string{
		"client_secret":                       "pi_123_secret_abc",
		"_stripe_account":                     "acct_9",
		"payment_method_data[type]":           "card",
		"payment_method_data[card][number]":   "4242424242424242",
		"payment_method_data[card][exp_year]": "2088",
	} {
		if v := got.form.Get(k); v != want {
			t.Errorf("%s = %q, want %q", k, v, want)
		}
	}
}

// A pre-authorization succeeds by stopping short of capture.
func TestConfirmPaymentIntentAcceptsAPreAuthorization(t *testing.T) {
	_, client := serveStripe(t, 200, `{"id":"pi_1","status":"requires_capture","amount":5000,"currency":"usd"}`)
	if _, err := ConfirmPaymentIntent(client, testCard(t), "pi_1_secret_x", "pk_live_x", "", nil); err != nil {
		t.Fatalf("requires_capture should be a success: %v", err)
	}
}

func TestConfirmPaymentIntentFailures(t *testing.T) {
	for _, tt := range []struct {
		name, body, want string
		status           int
	}{
		{"declined", `{"error":{"code":"card_declined","message":"Your card was declined."}}`, "declined", 402},
		{"3-D Secure", `{"id":"pi_1","status":"requires_action","next_action":{"type":"use_stripe_sdk"}}`, "3-D Secure", 200},
		{"left unpaid", `{"id":"pi_1","status":"requires_payment_method"}`, "requires_payment_method", 200},
	} {
		_, client := serveStripe(t, tt.status, tt.body)
		_, err := ConfirmPaymentIntent(client, testCard(t), "pi_1_secret_x", "pk_live_x", "", nil)
		if err == nil || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s: err = %v, want it to mention %q", tt.name, err, tt.want)
		}
	}
}

// The two intent kinds share a request but not a secret format; mixing
// them up must fail before anything is sent.
func TestConfirmRejectsTheWrongKindOfSecret(t *testing.T) {
	if _, err := ConfirmPaymentIntent(nil, testCard(t), "seti_1_secret_x", "pk_live_x", "", nil); err == nil {
		t.Error("a SetupIntent secret was accepted as a PaymentIntent secret")
	}
	if _, err := ConfirmSetupIntent(nil, testCard(t), "pi_1_secret_x", "pk_live_x", "", nil); err == nil {
		t.Error("a PaymentIntent secret was accepted as a SetupIntent secret")
	}
}

func TestConfirmSetupIntentStillWorks(t *testing.T) {
	got, client := serveStripe(t, 200, `{"id":"seti_7","status":"succeeded","payment_method":"pm_1"}`)
	res, err := ConfirmSetupIntent(client, testCard(t), "seti_7_secret_y", "pk_live_x", "", nil)
	if err != nil {
		t.Fatalf("ConfirmSetupIntent: %v", err)
	}
	if res.ID != "seti_7" || got.path != "/v1/setup_intents/seti_7/confirm" {
		t.Errorf("result = %+v, path = %q", res, got.path)
	}
}

// Reading an intent back is how the amount gets checked before the card
// is handed over, so it must send no card at all.
func TestRetrievePaymentIntent(t *testing.T) {
	got, client := serveStripe(t, 200, `{"id":"pi_5","amount":12000,"currency":"usd","status":"requires_payment_method"}`)
	info, err := RetrievePaymentIntent(client, "pi_5_secret_z", "pk_live_x", "acct_2", nil)
	if err != nil {
		t.Fatalf("RetrievePaymentIntent: %v", err)
	}
	if info.AmountCents != 12000 || info.ID != "pi_5" {
		t.Errorf("info = %+v", info)
	}
	if got.method != "GET" || got.path != "/v1/payment_intents/pi_5" {
		t.Errorf("request = %s %s", got.method, got.path)
	}
	if got.query.Get("client_secret") != "pi_5_secret_z" || got.query.Get("_stripe_account") != "acct_2" {
		t.Errorf("query = %v", got.query)
	}
	for k := range got.query {
		if strings.Contains(k, "card") {
			t.Errorf("retrieve sent card field %q", k)
		}
	}
}
