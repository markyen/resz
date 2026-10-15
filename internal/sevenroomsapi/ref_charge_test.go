package sevenroomsapi

import "testing"

func TestChargeTotal(t *testing.T) {
	tests := []struct {
		name string
		ref  Ref
		want float64
	}{
		{"per person", Ref{Cost: 50, ChargeType: "person", PartySize: 4}, 200},
		{"flat reservation", Ref{Cost: 50, ChargeType: "reservation", PartySize: 4}, 50},
		{"no charge type bills once", Ref{Cost: 25, PartySize: 4}, 25},
		{"no cost", Ref{ChargeType: "person", PartySize: 4}, 0},
		{"card on file, no amount", Ref{RequiresPayment: true, PartySize: 2}, 0},
		{"party size unset", Ref{Cost: 50, ChargeType: "person"}, 50},
		// The "_slot" types bill per quarter hour of the slot as well.
		{"per person per quarter hour", Ref{Cost: 5, ChargeType: "person_slot", PartySize: 2, Duration: 90}, 60},
		{"per reservation per quarter hour", Ref{Cost: 10, ChargeType: "reservation_slot", PartySize: 2, Duration: 120}, 80},
	}
	for _, tt := range tests {
		if got := tt.ref.ChargeTotal(); got != tt.want {
			t.Errorf("%s: ChargeTotal = %v, want %v", tt.name, got, tt.want)
		}
	}
}

// The cost must survive a round trip through the ref string, because the
// notification quotes an amount parsed back out of one.
func TestChargeSurvivesRefRoundTrip(t *testing.T) {
	in := Ref{
		Venue: "paidvenue", TimeISO: "2026-09-26 17:15:00", PartySize: 4,
		RequiresPayment: true, Cost: 50, ChargeType: "person",
	}
	out, err := ParseRef(in.String())
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if out.Cost != 50 || out.ChargeType != "person" {
		t.Fatalf("round trip lost pricing: cost=%v charge=%q", out.Cost, out.ChargeType)
	}
	if got := out.ChargeTotal(); got != 200 {
		t.Errorf("ChargeTotal after round trip = %v, want 200", got)
	}
}
