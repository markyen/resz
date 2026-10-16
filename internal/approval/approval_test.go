package approval

import (
	"strings"
	"testing"
	"time"

	"resz/internal/platform"
	"resz/internal/sevenroomsapi"
	"resz/internal/venueconfig"
)

func paidvenueSlot(t *testing.T) (platform.Slot, sevenroomsapi.Ref) {
	t.Helper()
	ref := sevenroomsapi.Ref{
		Venue: "paidvenue", TimeISO: "2026-09-26 17:15:00", PartySize: 4,
		RequiresPayment: true, Cost: 50, ChargeType: "person",
	}
	start, err := time.Parse("2006-01-02 15:04:05", ref.TimeISO)
	if err != nil {
		t.Fatal(err)
	}
	return platform.Slot{Platform: platform.SevenRooms, Start: start, Ref: ref.String()}, ref
}

// The usual case once the venue has priced the slot: the full total with
// its parts, and an authorize line carrying exactly that figure.
func TestMessageQuotesTheVenuesTotal(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	sit := Situation{
		HaveCard: true, Exact: true,
		Charge: sevenroomsapi.Charge{Base: 200, ServiceCharge: 14, Tax: 18.99, DueNow: 232.99},
	}
	msg, cost := Message("Paid Venue", slot, ref, sit)

	if cost != "charges $232.99 ($200.00 + $14.00 service charge + $18.99 tax) at booking" {
		t.Errorf("cost = %q", cost)
	}
	for _, want := range []string{
		"Paid Venue", "2026-09-26", "party of 4", "$232.99",
		"Reply APPROVE and resz will book it with your card, charging $232.99.",
		"authorize: 232.99\n",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
	// The old wording sent the person to the widget; resz books it now.
	if strings.Contains(msg, "widget") {
		t.Errorf("a slot resz can book should not send anyone to the widget:\n%s", msg)
	}
}

// Without a quote the advertised price is a floor, not the bill, so the
// message says so and offers no figure to authorize.
func TestMessageWithOnlyTheAdvertisedPrice(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	msg, cost := Message("Paid Venue", slot, ref, Situation{HaveCard: true})
	if cost != "charges at least $200.00 ($50.00 x 4) at booking, plus any tax or fees the venue adds" {
		t.Errorf("cost = %q", cost)
	}
	if strings.Contains(msg, "authorize:") {
		t.Errorf("an unpriced slot must not carry an authorize line:\n%s", msg)
	}
	if !strings.Contains(msg, "check it with you before charging") {
		t.Errorf("message should promise a second check:\n%s", msg)
	}
}

// A card-on-file slot charges nothing. The message must not imply a
// number nobody quoted, and has nothing to authorize.
func TestMessageForACardOnFileSlot(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	ref.Cost, ref.ChargeType = 0, ""
	for _, sit := range []Situation{{HaveCard: true}, {HaveCard: true, Exact: true}} {
		msg, cost := Message("Somewhere", slot, ref, sit)
		if strings.Contains(cost, "$") {
			t.Errorf("cost should not invent a figure: %q", cost)
		}
		if strings.Contains(msg, "authorize:") {
			t.Errorf("nothing to authorize, but:\n%s", msg)
		}
		if !strings.Contains(msg, "book it with your card on file") {
			t.Errorf("message = %s", msg)
		}
	}
	_, cost := Message("Somewhere", slot, ref, Situation{HaveCard: true})
	if !strings.Contains(cost, "no amount advertised") {
		t.Errorf("unpriced card-on-file cost = %q", cost)
	}
}

func TestMessageForAPreAuthorization(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	ref.Cost, ref.ChargeType = 0, ""
	msg, cost := Message("Somewhere", slot, ref, Situation{
		HaveCard: true, Exact: true, Charge: sevenroomsapi.Charge{PreAuthorization: 50},
	})
	if !strings.Contains(cost, "holds $50.00") || !strings.Contains(cost, "only on a no-show") {
		t.Errorf("cost = %q", cost)
	}
	if strings.Contains(msg, "authorize:") {
		t.Errorf("a hold needs no authorization, but:\n%s", msg)
	}
}

// When resz can't book it, the person has to, and the message says where
// — without an APPROVE that would lead nowhere.
func TestMessageWhenResZCannotBook(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	charged := sevenroomsapi.Charge{Base: 200, DueNow: 200}
	for name, sit := range map[string]Situation{
		"no card":     {Exact: true, Charge: charged},
		"bad gateway": {HaveCard: true, Exact: true, Charge: charged, Gateway: "FREEDOMPAY"},
	} {
		msg, _ := Message("Paid Venue", slot, ref, sit)
		if !strings.Contains(msg, "https://www.sevenrooms.com/reservations/paidvenue") {
			t.Errorf("%s: no widget link:\n%s", name, msg)
		}
		if strings.Contains(msg, "APPROVE") || strings.Contains(msg, "authorize:") {
			t.Errorf("%s: offers an approval resz can't act on:\n%s", name, msg)
		}
	}
	msg, _ := Message("Paid Venue", slot, ref, Situation{HaveCard: true, Gateway: "FREEDOMPAY"})
	if !strings.Contains(msg, "FREEDOMPAY") {
		t.Errorf("message should name the gateway:\n%s", msg)
	}
}

func TestMessageCarriesTheExactRefLast(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	msg, _ := Message("Paid Venue", slot, ref, Situation{
		HaveCard: true, Exact: true, Charge: sevenroomsapi.Charge{Base: 200, DueNow: 200},
	})
	// Whatever books this next must be able to lift the reference back
	// out of the message without reconstructing it.
	i := strings.Index(msg, "ref: ")
	if i < 0 {
		t.Fatalf("no ref line:\n%s", msg)
	}
	got := strings.TrimSpace(msg[i+len("ref: "):])
	if got != slot.Ref {
		t.Errorf("ref line = %q, want %q", got, slot.Ref)
	}
	parsed, err := sevenroomsapi.ParseRef(got)
	if err != nil {
		t.Fatalf("ref from the message does not parse: %v", err)
	}
	if parsed.ChargeTotal() != 200 || parsed.PartySize != 4 {
		t.Errorf("parsed ref lost detail: total=%v party=%d", parsed.ChargeTotal(), parsed.PartySize)
	}
}

func TestMayAutoBook(t *testing.T) {
	free := sevenroomsapi.Ref{RequiresPayment: true, PartySize: 2}
	paid := sevenroomsapi.Ref{RequiresPayment: true, PartySize: 2, Cost: 50, ChargeType: "person"}
	if !MayAutoBook(venueconfig.ConfigEntry{AutoBookCard: true}, free) {
		t.Error("an opted-in venue's card-on-file slot should book")
	}
	if MayAutoBook(venueconfig.ConfigEntry{AutoBookCard: true}, paid) {
		t.Error("a charging slot must ask, opted in or not")
	}
	if MayAutoBook(venueconfig.ConfigEntry{}, free) {
		t.Error("a venue not opted in must ask")
	}
}

// A Resy deposit: the amount, what APPROVE does, the figure to authorize,
// and the reference verbatim on the last line.
func TestResyMessageQuotesTheDeposit(t *testing.T) {
	start := time.Date(2026, 11, 14, 19, 0, 0, 0, time.UTC)
	slot := platform.Slot{Platform: platform.Resy, Start: start,
		Ref: "rgs://resy/90001/123/2/2026-11-14/2026-11-14/19:00:00/2/Table"}
	msg, cost := ResyMessage("Paid Venue", slot, 2, 200)
	if cost != "charges $200.00 at booking (a deposit or prepayment, through Resy)" {
		t.Errorf("cost = %q", cost)
	}
	for _, want := range []string{"Paid Venue", "Nov 14", "party of 2",
		"Reply APPROVE and resz will book it with the card on your Resy account, charging $200.00.",
		"authorize: 200.00\n"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
	if !strings.HasSuffix(msg, "\nref: "+slot.Ref) {
		t.Errorf("the ref must be the last line, verbatim:\n%s", msg)
	}
}
