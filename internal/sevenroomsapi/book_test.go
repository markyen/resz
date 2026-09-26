package sevenroomsapi

import (
	"errors"
	"strings"
	"testing"
)

func TestCheckoutHash(t *testing.T) {
	// The widget hashes "<first>|<last>", trimmed and lowercased, with
	// SHA-256. Getting this wrong is silently rejected at checkout, so
	// pin the exact value.
	g := Guest{FirstName: "Jane", LastName: "Doe"}
	// sha256("jane|doe")
	const want = "1ebc55e4d47680abcb2a5d389ce94e89cb73e2abbffb6aebce10fd956abe2895"
	got := g.checkoutHash()
	if got != want {
		t.Fatalf("checkoutHash = %q; want %q", got, want)
	}

	// Case and surrounding whitespace must not change the hash.
	for _, variant := range []Guest{
		{FirstName: "jane", LastName: "doe"},
		{FirstName: "  Jane  ", LastName: "Doe "},
		{FirstName: "JANE", LastName: "DOE"},
	} {
		if variant.checkoutHash() != got {
			t.Errorf("checkoutHash differs for %+v; normalization is broken", variant)
		}
	}
	// A different name must produce a different hash.
	if (Guest{FirstName: "Jane", LastName: "Doen"}).checkoutHash() == got {
		t.Error("checkoutHash collides across different names")
	}
}

func TestGuestValidate(t *testing.T) {
	full := Guest{
		FirstName: "Jane", LastName: "Doe", Email: "a@b.com",
		PhoneNumber: "5551234567", DialCode: "1", CountryCode: "us",
	}
	if err := full.Validate(); err != nil {
		t.Errorf("Validate on a complete guest: %v", err)
	}
	// Every field is load-bearing; dropping any one must be caught before
	// a half-formed booking is submitted.
	for _, drop := range []func(*Guest){
		func(g *Guest) { g.FirstName = "" },
		func(g *Guest) { g.LastName = "" },
		func(g *Guest) { g.Email = "" },
		func(g *Guest) { g.PhoneNumber = "" },
		func(g *Guest) { g.DialCode = "" },
		func(g *Guest) { g.CountryCode = "" },
	} {
		g := full
		drop(&g)
		if err := g.Validate(); err == nil {
			t.Errorf("Validate accepted an incomplete guest: %+v", g)
		}
	}
}

func TestRefBookFormats(t *testing.T) {
	// The two endpoints disagree about date format, and the clock time
	// has no leading zero. Both are easy to get wrong and neither fails
	// loudly, so pin them.
	r := Ref{Venue: "examplevenue", TimeISO: "2026-08-14 21:45:00", PartySize: 2}
	if got := r.Date(); got != "2026-08-14" {
		t.Errorf("Date = %q; want 2026-08-14 (hold format)", got)
	}
	bd, err := r.BookDate()
	if err != nil {
		t.Fatalf("BookDate: %v", err)
	}
	if bd != "08-14-2026" {
		t.Errorf("BookDate = %q; want 08-14-2026", bd)
	}
	ct, err := r.ClockTime()
	if err != nil {
		t.Fatalf("ClockTime: %v", err)
	}
	if ct != "9:45 PM" {
		t.Errorf("ClockTime = %q; want \"9:45 PM\"", ct)
	}

	// Morning times keep the same no-leading-zero shape.
	am := Ref{TimeISO: "2026-08-14 09:05:00"}
	if ct, _ := am.ClockTime(); ct != "9:05 AM" {
		t.Errorf("ClockTime = %q; want \"9:05 AM\"", ct)
	}
}

func TestPaymentFlagSurvivesRefRoundTrip(t *testing.T) {
	// The flag is what stops resz from holding a table it can never pay
	// for, so it has to survive the availability -> book handoff.
	r := Ref{Venue: "v", TimeISO: "2026-08-14 19:00:00", PartySize: 2, RequiresPayment: true}
	got, err := ParseRef(r.String())
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if !got.RequiresPayment {
		t.Error("RequiresPayment was lost in the round trip")
	}
	plain := Ref{Venue: "v", TimeISO: "2026-08-14 19:00:00", PartySize: 2}
	got, err = ParseRef(plain.String())
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if got.RequiresPayment {
		t.Error("RequiresPayment set on a slot that never had it")
	}
}

func TestBookMessageExtractsErrors(t *testing.T) {
	// /book reports problems as {"errors": [...]}, unlike the api-yoa
	// endpoints' {"msg": ...}; both shapes have to read well to the user.
	got := bookMessage(400, []byte(`{"errors": ["The first name entered is invalid."]}`))
	if got != "The first name entered is invalid." {
		t.Errorf("bookMessage = %q", got)
	}
	got = bookMessage(400, []byte(`{"errors": ["a", "b"]}`))
	if got != "a; b" {
		t.Errorf("bookMessage = %q; want \"a; b\"", got)
	}
	// Throttling answers with an HTML page, not JSON, so the status has
	// to carry the explanation.
	got = bookMessage(429, []byte("<!doctype html><title>429</title>429 Too Many Requests"))
	if !strings.Contains(got, "rate limited") {
		t.Errorf("bookMessage on 429 = %q; want a rate-limit explanation", got)
	}
}

func TestIsRateLimited(t *testing.T) {
	if !IsRateLimited(&apiError{Status: 429}) {
		t.Error("IsRateLimited = false for a 429")
	}
	if IsRateLimited(&apiError{Status: 400}) {
		t.Error("IsRateLimited = true for a 400")
	}
	if IsRateLimited(errors.New("boom")) {
		t.Error("IsRateLimited = true for an unrelated error")
	}
}

func TestReservationID(t *testing.T) {
	// The id sits at a different depth depending on venue configuration.
	for _, body := range []string{
		`{"id": "abc"}`,
		`{"payload": {"id": "abc"}}`,
		`{"payload": {"actual_id": "abc"}}`,
		`{"payload": {"actual": {"id": "abc"}}}`,
		`{"data": {"actual_id": "abc"}}`,
	} {
		if got := reservationID([]byte(body)); got != "abc" {
			t.Errorf("reservationID(%s) = %q; want abc", body, got)
		}
	}
	// An unrecognized shape is fine — the booking still succeeded.
	if got := reservationID([]byte(`{"weird": true}`)); got != "" {
		t.Errorf("reservationID on an unknown shape = %q; want \"\"", got)
	}
}
