package check

import (
	"strings"
	"testing"
	"time"

	"resz/internal/platform"
	"resz/internal/sevenroomsapi"
	"resz/internal/state"
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

func TestApprovalMessageQuotesTheRealTotal(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	msg, amount := approvalMessage("Paid Venue", slot, ref)

	if amount != "$200.00 ($50.00 x 4)" {
		t.Errorf("amount = %q, want the per-person breakdown", amount)
	}
	for _, want := range []string{
		"Paid Venue", "2026-09-26", "$200.00", "APPROVE",
		"https://www.sevenrooms.com/reservations/paidvenue",
		"ref: sr://paidvenue/2026-09-26T17:15:00",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("message is missing %q:\n%s", want, msg)
		}
	}
}

// A card-on-file slot advertises no price. The message must not imply a
// number nobody quoted.
func TestApprovalMessageWithoutAnAmount(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	ref.Cost, ref.ChargeType = 0, ""
	_, amount := approvalMessage("Somewhere", slot, ref)
	if !strings.Contains(amount, "no amount advertised") {
		t.Errorf("amount = %q", amount)
	}
	if strings.Contains(amount, "$") {
		t.Errorf("amount should not invent a figure: %q", amount)
	}
}

func TestApprovalMessageCarriesTheExactRef(t *testing.T) {
	slot, ref := paidvenueSlot(t)
	msg, _ := approvalMessage("Paid Venue", slot, ref)
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

// check runs every half hour; a standing slot must not be messaged each
// time.
func TestApprovalCooldown(t *testing.T) {
	s := &state.State{}
	ref := "sr://paidvenue/2026-09-26T17:15:00?party_size=4"
	now := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)

	if !s.ShouldNotifyApproval(ref, now) {
		t.Fatal("first sighting should notify")
	}
	if s.ShouldNotifyApproval(ref, now.Add(30*time.Minute)) {
		t.Error("the next tick should stay quiet")
	}
	if s.ShouldNotifyApproval(ref, now.Add(state.ApprovalCooldown-time.Minute)) {
		t.Error("still inside the cooldown should stay quiet")
	}
	if !s.ShouldNotifyApproval(ref, now.Add(state.ApprovalCooldown+time.Minute)) {
		t.Error("a slot still sitting there after the cooldown should be raised again")
	}
	// A different slot at the same venue is its own decision.
	if !s.ShouldNotifyApproval(ref+"&other=1", now.Add(31*time.Minute)) {
		t.Error("a different reference should notify on its own schedule")
	}
}

func TestApprovalPruningForgetsStaleRefs(t *testing.T) {
	s := &state.State{}
	now := time.Date(2026, 9, 20, 9, 0, 0, 0, time.UTC)
	s.ShouldNotifyApproval("sr://old", now)
	// Far enough past retention that the reference cannot still matter.
	s.ShouldNotifyApproval("sr://new", now.AddDate(0, 0, 30))
	if _, ok := s.ApprovalNotified["sr://old"]; ok {
		t.Error("a month-old reference should have been pruned")
	}
	if _, ok := s.ApprovalNotified["sr://new"]; !ok {
		t.Error("the current reference should be kept")
	}
}

func TestMayAutoBookCard(t *testing.T) {
	free := sevenroomsapi.Ref{RequiresPayment: true, PartySize: 2}                                     // card on file, $0
	paid := sevenroomsapi.Ref{RequiresPayment: true, PartySize: 2, Cost: 50, ChargeType: "person"}     // $100
	tiny := sevenroomsapi.Ref{RequiresPayment: true, PartySize: 2, Cost: 1, ChargeType: "reservation"} // $1

	tests := []struct {
		name string
		v    venueconfig.ConfigEntry
		ref  sevenroomsapi.Ref
		want bool
	}{
		{"opted in, charges nothing", venueconfig.ConfigEntry{AutoBookCard: true}, free, true},
		{"opted in but charges", venueconfig.ConfigEntry{AutoBookCard: true}, paid, false},
		{"opted in, even a token charge asks", venueconfig.ConfigEntry{AutoBookCard: true}, tiny, false},
		{"not opted in", venueconfig.ConfigEntry{}, free, false},
		{"not opted in and charges", venueconfig.ConfigEntry{}, paid, false},
	}
	for _, tt := range tests {
		if got := mayAutoBookCard(tt.v, tt.ref); got != tt.want {
			t.Errorf("%s: mayAutoBookCard = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// The interlock is the whole safety story: a venue marked for automatic
// booking that starts asking for a deposit must revert to asking, with no
// config change.
func TestAutoBookInterlockOnPriceIntroduction(t *testing.T) {
	v := venueconfig.ConfigEntry{Name: "Example Venue", AutoBookCard: true}
	onFile := sevenroomsapi.Ref{RequiresPayment: true, PartySize: 2}
	if !mayAutoBookCard(v, onFile) {
		t.Fatal("a card-on-file slot at an opted-in venue should book")
	}
	// Same venue, same flag — the venue has introduced a deposit.
	nowCharging := onFile
	nowCharging.Cost, nowCharging.ChargeType = 25, "person"
	if mayAutoBookCard(v, nowCharging) {
		t.Error("once the slot charges, it must go back to asking")
	}
}
