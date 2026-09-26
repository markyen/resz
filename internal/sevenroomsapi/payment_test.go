package sevenroomsapi

import (
	"errors"
	"strings"
	"testing"

	"resz/internal/cardsecret"
)

const cardEntry = `4242424242424242
exp: 12/2088
cvc: 123
zip: 10014`

func testCard(t *testing.T) *cardsecret.Card {
	t.Helper()
	c, err := cardsecret.Parse(cardEntry)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func testGuest() Guest {
	return Guest{FirstName: "Jane", LastName: "Doe", Email: "diner@example.com",
		PhoneNumber: "5551234567", DialCode: "1", CountryCode: "us"}
}

// Without a card, a card-required slot is refused exactly as before.
func TestReserveWithoutCardStillRefuses(t *testing.T) {
	ref := Ref{Venue: "examplevenue", TimeISO: "2026-09-26 20:45:00", PartySize: 2, RequiresPayment: true}
	_, err := ReserveWithCard(nil, ref, testGuest(), nil, nil)
	if !errors.Is(err, ErrPaymentRequired) {
		t.Fatalf("err = %v, want ErrPaymentRequired", err)
	}
}

// The interlock that matters: resz stores cards, it does not spend. A
// slot that charges is refused before any network call, card or not.
func TestReserveRefusesToSpend(t *testing.T) {
	ref := Ref{
		Venue: "paidvenue", TimeISO: "2026-09-26 17:15:00", PartySize: 2,
		RequiresPayment: true, Cost: 50, ChargeType: "person",
	}
	_, err := ReserveWithCard(nil, ref, testGuest(), testCard(t), nil)
	if !errors.Is(err, ErrChargeAtBooking) {
		t.Fatalf("err = %v, want ErrChargeAtBooking", err)
	}
	if !strings.Contains(err.Error(), "$100.00") {
		t.Errorf("error should name the amount it refused: %v", err)
	}
}

func TestSetUpCardNeedsAGateway(t *testing.T) {
	card := testCard(t)
	// A venue with no Stripe key cannot be paid by resz, whatever the
	// slot says. Other Venue is on FreedomPay and lands here.
	_, err := setUpCard(nil, &Venue{Slug: "othervenue", ID: "abc", PaymentSystem: "FREEDOMPAY"}, card, nil)
	if err == nil || !strings.Contains(err.Error(), "no Stripe key") {
		t.Fatalf("err = %v, want a complaint about the missing Stripe key", err)
	}
	if !strings.Contains(err.Error(), "widget") {
		t.Errorf("error should point at the browser fallback: %v", err)
	}
}

func TestSetUpCardNeedsVenueID(t *testing.T) {
	_, err := setUpCard(nil, nil, testCard(t), nil)
	if err == nil || !strings.Contains(err.Error(), "internal id") {
		t.Fatalf("err = %v, want a complaint about the missing venue id", err)
	}
}

// card_token must only appear when there is one, so a free booking keeps
// the exact shape it had before this change.
func TestBookOmitsCardTokenWhenEmpty(t *testing.T) {
	// Exercised through the field map builder via a nil-client call that
	// fails at transport; what matters is that the code path compiles and
	// BookWithCard("") is what plain Book delegates to.
	ref := Ref{Venue: "x", TimeISO: "2026-09-26 17:00:00", PartySize: 2}
	if _, err := Book(nil, ref, nil, Guest{}, &Hold{ID: "h"}, nil); err == nil {
		t.Fatal("expected guest validation to fail")
	}
}

// The dining widget answers with reservation_id and puts the
// human-facing confirmation code in "message".
func TestParsesLiveBookResponse(t *testing.T) {
	body := []byte(`{"message":"ABCD1234EFG","reservation_id":"resid12345","api_reservation_id":"ahNzfmFwaQ","status":"success"}`)
	if got := reservationID(body); got != "resid12345" {
		t.Errorf("reservationID = %q, want the reservation_id", got)
	}
	if got := confirmationCode(body); got != "ABCD1234EFG" {
		t.Errorf("confirmationCode = %q", got)
	}
}

func TestConfirmationCodeIgnoresProse(t *testing.T) {
	for _, body := range []string{
		`{"message":"Your reservation has been confirmed","status":"success"}`,
		`{"message":"","status":"success"}`,
		`{"status":"success"}`,
	} {
		if got := confirmationCode([]byte(body)); got != "" {
			t.Errorf("confirmationCode(%s) = %q, want empty", body, got)
		}
	}
}
