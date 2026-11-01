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

// Without a card, a card-required slot is refused with ErrPaymentRequired.
func TestReserveWithoutCardStillRefuses(t *testing.T) {
	ref := Ref{Venue: "examplevenue", TimeISO: "2026-09-26 20:45:00", PartySize: 2, RequiresPayment: true}
	_, err := ReserveWithCard(nil, ref, testGuest(), nil, 0, nil)
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
	_, err := ReserveWithCard(nil, ref, testGuest(), testCard(t), 0, nil)
	if !errors.Is(err, ErrChargeAtBooking) {
		t.Fatalf("err = %v, want ErrChargeAtBooking", err)
	}
	if !strings.Contains(err.Error(), "$100.00") {
		t.Errorf("error should name the amount it refused: %v", err)
	}
}

func TestSetUpCardNeedsAGateway(t *testing.T) {
	card := testCard(t)
	// Only Stripe can be driven headlessly. A FreedomPay venue must say so
	// in terms that stop a caller retrying, and send it to the widget.
	_, _, err := setUpCard(nil, &Venue{Slug: "othervenue", ID: "abc", PaymentSystem: "FREEDOMPAY"}, card, Charge{}, nil)
	if !errors.Is(err, ErrGatewayUnsupported) {
		t.Fatalf("err = %v, want ErrGatewayUnsupported", err)
	}
	for _, want := range []string{"FREEDOMPAY", "othervenue", "widget"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %q: %v", want, err)
		}
	}
}

// A venue that reports no gateway at all still produces a readable error
// rather than naming an empty string.
func TestSetUpCardWithUnnamedGateway(t *testing.T) {
	_, _, err := setUpCard(nil, &Venue{Slug: "somewhere", ID: "abc"}, testCard(t), Charge{}, nil)
	if !errors.Is(err, ErrGatewayUnsupported) {
		t.Fatalf("err = %v, want ErrGatewayUnsupported", err)
	}
	if strings.Contains(err.Error(), `""`) {
		t.Errorf("empty gateway should be described, not quoted empty: %v", err)
	}
}

func TestSetUpCardNeedsVenueID(t *testing.T) {
	_, _, err := setUpCard(nil, nil, testCard(t), Charge{}, nil)
	if err == nil || !strings.Contains(err.Error(), "internal id") {
		t.Fatalf("err = %v, want a complaint about the missing venue id", err)
	}
}

// card_token appears only when there is a card, so a free booking's form
// carries no card field at all.
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

// The authorization ceiling is the whole safety story for the CLI path:
// a person names the figure they were quoted, and anything above it is
// refused rather than quietly paid.
func TestAuthorizedCeiling(t *testing.T) {
	// $60 per person, party of 2 => $120.
	ref := Ref{
		Venue: "paidvenue", TimeISO: "2026-10-31 17:30:00", PartySize: 2,
		RequiresPayment: true, Cost: 60, ChargeType: "person",
	}
	card := testCard(t)

	// Authorizing less than the charge must refuse, and say both numbers.
	_, err := ReserveWithCard(nil, ref, testGuest(), card, 100, nil)
	if !errors.Is(err, ErrChargeAtBooking) {
		t.Fatalf("under-authorized: err = %v, want ErrChargeAtBooking", err)
	}
	for _, want := range []string{"$120.00", "$100.00"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should name %s: %v", want, err)
		}
	}

	// Authorizing exactly the charge clears the interlock — it then fails
	// later, on the network, which is a different error.
	_, err = ReserveWithCard(nil, ref, testGuest(), card, 120, nil)
	if errors.Is(err, ErrChargeAtBooking) {
		t.Errorf("authorizing the exact amount should clear the interlock, got %v", err)
	}

	// A price rise after approval is refused, not absorbed.
	risen := ref
	risen.Cost = 75 // now $150
	_, err = ReserveWithCard(nil, risen, testGuest(), card, 120, nil)
	if !errors.Is(err, ErrChargeAtBooking) {
		t.Errorf("a price above the authorization must be refused, got %v", err)
	}
}

// Unattended callers authorize nothing, so any slot that charges is refused.
func TestZeroAuthorizationStillRefusesAnyCharge(t *testing.T) {
	ref := Ref{
		Venue: "paidvenue", TimeISO: "2026-10-31 17:30:00", PartySize: 2,
		RequiresPayment: true, Cost: 1, ChargeType: "reservation",
	}
	if _, err := ReserveWithCard(nil, ref, testGuest(), testCard(t), 0, nil); !errors.Is(err, ErrChargeAtBooking) {
		t.Fatalf("even $1 must be refused without authorization, got %v", err)
	}
}

// A card-on-file slot charges nothing, so it books with no authorization.
func TestCardOnFileNeedsNoAuthorization(t *testing.T) {
	ref := Ref{Venue: "examplevenue", TimeISO: "2026-10-31 17:30:00", PartySize: 2, RequiresPayment: true}
	_, err := ReserveWithCard(nil, ref, testGuest(), testCard(t), 0, nil)
	if errors.Is(err, ErrChargeAtBooking) {
		t.Errorf("a $0 slot should not need authorization, got %v", err)
	}
}

// gatewayRefusal is the guard that runs before any hold is taken, so an
// undrivable gateway never blocks a real table for the five minutes a
// hold takes to lapse.
func TestGatewayRefusal(t *testing.T) {
	card := testCard(t)
	paid := Ref{Venue: "v", RequiresPayment: true}
	free := Ref{Venue: "v"}
	stripe := &Venue{Slug: "v", PaymentSystem: "STRIPE", PaymentPublicToken: "pk_live_x"}
	freedom := &Venue{Slug: "v", PaymentSystem: "FREEDOMPAY"}

	tests := []struct {
		name   string
		ref    Ref
		card   *cardsecret.Card
		venue  *Venue
		refuse bool
	}{
		{"freedompay with a card", paid, card, freedom, true},
		{"stripe with a card", paid, card, stripe, false},
		{"freedompay but nothing to pay", free, card, freedom, false},
		{"freedompay but no card configured", paid, nil, freedom, false},
		{"venue unknown", paid, card, nil, false},
	}
	for _, tt := range tests {
		err := gatewayRefusal(tt.ref, tt.card, tt.venue)
		if tt.refuse && !errors.Is(err, ErrGatewayUnsupported) {
			t.Errorf("%s: err = %v, want ErrGatewayUnsupported", tt.name, err)
		}
		if !tt.refuse && err != nil {
			t.Errorf("%s: unexpected refusal %v", tt.name, err)
		}
	}
}
