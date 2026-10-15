package check

import (
	"testing"
	"time"

	"resz/internal/sevenroomsapi"
	"resz/internal/state"
	"resz/internal/venueconfig"
)

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
