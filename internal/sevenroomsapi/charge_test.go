package sevenroomsapi

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestComputeChargeBase(t *testing.T) {
	tests := []struct {
		name     string
		cfg      PaymentConfig
		party    int
		duration int
		want     float64
	}{
		{"per person", PaymentConfig{ChargeType: "person", Cost: 60}, 2, 90, 120},
		{"per reservation", PaymentConfig{ChargeType: "reservation", Cost: 50}, 4, 90, 50},
		// 90 minutes is six quarter hours.
		{"per person per quarter hour", PaymentConfig{ChargeType: "person_slot", Cost: 5}, 2, 90, 60},
		{"per reservation per quarter hour", PaymentConfig{ChargeType: "reservation_slot", Cost: 10}, 2, 120, 80},
		{"unrecognized type bills nothing", PaymentConfig{ChargeType: "mystery", Cost: 50}, 2, 90, 0},
		{"no cost", PaymentConfig{ChargeType: "person"}, 2, 90, 0},
	}
	for _, tt := range tests {
		c, err := ComputeCharge(tt.cfg, tt.party, tt.duration)
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		if c.Base != tt.want || c.DueNow != tt.want {
			t.Errorf("%s: base=%v due=%v, want %v", tt.name, c.Base, c.DueNow, tt.want)
		}
	}
}

// The parts that make the bill bigger than the advertised price: service
// charge on the base, tax on base plus service charge, gratuity on the
// base. This is the widget's order of operations.
func TestComputeChargeFees(t *testing.T) {
	cfg := PaymentConfig{
		ChargeType: "person", Cost: 60,
		ApplyServiceCharge: true, ServiceCharge: 7,
		TaxRate:             8.875,
		ApplyGratuityCharge: true, GratuityType: "SPECIFIC_GRATUITY", Gratuity: 20,
	}
	c, err := ComputeCharge(cfg, 2, 90)
	if err != nil {
		t.Fatal(err)
	}
	// base 120; service 8.40; tax (128.40 * 8.875%) = 11.3955 -> 11.40;
	// gratuity 24; total 163.7955 -> 163.80.
	want := Charge{Base: 120, ServiceCharge: 8.4, Tax: 11.4, Gratuity: 24, DueNow: 163.8}
	if c != want {
		t.Errorf("charge = %+v, want %+v", c, want)
	}
}

func TestComputeChargeGratuityTypes(t *testing.T) {
	base := PaymentConfig{ChargeType: "reservation", Cost: 100, ApplyGratuityCharge: true, Gratuity: 18, DefaultGratuity: 22}
	for _, tt := range []struct {
		gratuityType string
		want         float64
	}{
		{"DEFAULT_GRATUITY", 22},
		{"SPECIFIC_GRATUITY", 18},
		// The diner picks; resz picks nothing.
		{"CLIENT_GRATUITY", 0},
		// No type means no gratuity, even with the flag set.
		{"", 0},
	} {
		cfg := base
		cfg.GratuityType = tt.gratuityType
		c, err := ComputeCharge(cfg, 2, 90)
		if err != nil {
			t.Fatal(err)
		}
		if c.Gratuity != tt.want {
			t.Errorf("%q: gratuity = %v, want %v", tt.gratuityType, c.Gratuity, tt.want)
		}
	}
}

// A duration-billed slot with no duration would otherwise price at zero
// and be booked as if free.
func TestComputeChargeRefusesADurationlessSlotCharge(t *testing.T) {
	_, err := ComputeCharge(PaymentConfig{ChargeType: "person_slot", Cost: 5}, 2, 0)
	if err == nil {
		t.Fatal("expected an error for a per-quarter-hour charge with no duration")
	}
}

func TestComputeChargePreAuthorization(t *testing.T) {
	cardOnFile := PaymentConfig{
		RequireCreditCard: true, CCPaymentRule: "save_for_later",
		ComputedAutoCancellationChargeCents: 5000,
	}
	tests := []struct {
		name  string
		cfg   func(PaymentConfig) PaymentConfig
		party int
		want  float64
	}{
		{"no-show fee is pre-authorized", func(p PaymentConfig) PaymentConfig { return p }, 2, 50},
		{"party at the minimum needs no card", func(p PaymentConfig) PaymentConfig { p.CCPartySizeMin = 2; return p }, 2, 0},
		{"party above the minimum does", func(p PaymentConfig) PaymentConfig { p.CCPartySizeMin = 2; return p }, 3, 50},
		{"prepaid slots don't pre-authorize", func(p PaymentConfig) PaymentConfig { p.CCPaymentRule = "advanced_payment"; return p }, 2, 0},
		{"no fee, nothing to hold", func(p PaymentConfig) PaymentConfig { p.ComputedAutoCancellationChargeCents = 0; return p }, 2, 0},
		{"a charge replaces the hold", func(p PaymentConfig) PaymentConfig {
			p.ChargeType, p.Cost = "reservation", 25
			return p
		}, 2, 0},
	}
	for _, tt := range tests {
		c, err := ComputeCharge(tt.cfg(cardOnFile), tt.party, 90)
		if err != nil {
			t.Fatal(err)
		}
		if c.PreAuthorization != tt.want {
			t.Errorf("%s: pre-authorization = %v, want %v", tt.name, c.PreAuthorization, tt.want)
		}
		if c.PreAuthorization > 0 && !c.MovesMoney() {
			t.Errorf("%s: a pre-authorization must go through a PaymentIntent", tt.name)
		}
	}
}

func TestRoundCentsMatchesTheWidget(t *testing.T) {
	for in, want := range map[float64]float64{
		11.3955: 11.4,
		0.285:   0.29, // float error would round this down without the nudge
		1.005:   1.01,
		2.5:     2.5,
		0:       0,
	} {
		if got := roundCents(in); got != want {
			t.Errorf("roundCents(%v) = %v, want %v", in, got, want)
		}
	}
}

func TestBreakdown(t *testing.T) {
	c := Charge{Base: 120, ServiceCharge: 8.4, Tax: 11.4, DueNow: 139.8}
	if got := c.Breakdown(); got != "$139.80 ($120.00 + $8.40 service charge + $11.40 tax)" {
		t.Errorf("Breakdown = %q", got)
	}
	if got := (Charge{Base: 120, DueNow: 120}).Breakdown(); got != "$120.00" {
		t.Errorf("a bare price should render alone, got %q", got)
	}
}

// checkout-details is decoded through the same envelope as every api-yoa
// endpoint. Numbers arrive as numbers, strings or null depending on how
// the venue was configured; all three must read.
func TestCheckoutDetailsDecodes(t *testing.T) {
	body := `{"slot":{"duration":90,"time":"7:00 PM"},
	  "policy":{"booking_text":"..."},
	  "payment":{"require_credit_card":true,"cc_payment_rule":"advanced_payment","cc_party_size_min":null,
	    "charge_type":"person","cost":"60","apply_service_charge":true,"service_charge":7,
	    "apply_gratuity_charge":false,"gratuity_type":null,"gratuity":null,"default_gratuity":20,
	    "tax_rate":8.875,"computed_auto_cancellation_charge_cents":null}}`
	var d checkoutDetails
	if err := json.Unmarshal([]byte(body), &d); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if d.Slot.Duration != 90 || d.Payment.Cost != 60 || d.Payment.TaxRate != 8.875 || d.Payment.CCPartySizeMin != 0 {
		t.Errorf("decoded = %+v", d)
	}
	c, err := ComputeCharge(d.Payment, 2, d.Slot.Duration)
	if err != nil {
		t.Fatal(err)
	}
	if c.DueNow != 139.8 {
		t.Errorf("due = %v, want 139.80", c.DueNow)
	}
}

func TestNumRejectsGarbage(t *testing.T) {
	var n num
	if err := json.Unmarshal([]byte(`"twelve"`), &n); err == nil {
		t.Error("a non-numeric string was accepted")
	}
}

// The booking form carries the widget's charge fields only when money is
// due, and never a zero: the widget drops falsy values.
func TestChargeFields(t *testing.T) {
	if f := chargeFields(Charge{}); len(f) != 0 {
		t.Errorf("a free slot added fields: %v", f)
	}
	if f := chargeFields(Charge{PreAuthorization: 50}); len(f) != 0 {
		t.Errorf("a pre-authorization is not a charge, but added fields: %v", f)
	}
	f := chargeFields(Charge{Base: 120, Tax: 10.65, DueNow: 130.65})
	want := map[string]string{
		"charge_base_amount": "120",
		"charge_tax":         "10.65",
		"charge_amount":      "130.65",
		"amount_due":         "130.65",
	}
	if len(f) != len(want) {
		t.Errorf("fields = %v, want %v", f, want)
	}
	for k, v := range want {
		if f[k] != v {
			t.Errorf("%s = %q, want %q", k, f[k], v)
		}
	}
}

func TestParsePaymentIntent(t *testing.T) {
	for _, body := range []string{
		`{"payload":{"client_secret":"pi_1_secret_a","id":"pi_1"}}`,
		`{"client_secret":"pi_1_secret_a"}`,
	} {
		pi, err := parsePaymentIntent([]byte(body))
		if err != nil || pi.ClientSecret != "pi_1_secret_a" {
			t.Errorf("parsePaymentIntent(%s) = %+v, %v", body, pi, err)
		}
	}
	for _, body := range []string{`{}`, `{"payload":{"client_secret":"seti_1_secret_a"}}`, `not json`} {
		if _, err := parsePaymentIntent([]byte(body)); err == nil {
			t.Errorf("parsePaymentIntent(%s) accepted a response with no PaymentIntent secret", body)
		}
	}
}

// The authorization ceiling compares whole cents, so approving the quoted
// figure covers exactly that figure and nothing above it.
func TestCheckAuthorizedInCents(t *testing.T) {
	if err := checkAuthorized("$141.75", 141.75, 141.75); err != nil {
		t.Errorf("approving the exact amount was refused: %v", err)
	}
	err := checkAuthorized("$141.76 ($120.00 + ...)", 141.76, 141.75)
	if !errors.Is(err, ErrChargeAtBooking) {
		t.Fatalf("a cent over was allowed: %v", err)
	}
	for _, want := range []string{"$141.76 ($120.00", "$141.75 authorized"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("refusal should say %q: %v", want, err)
		}
	}
	if err := checkAuthorized("$5.00", 5, 0); !errors.Is(err, ErrChargeAtBooking) || !strings.Contains(err.Error(), "at booking") {
		t.Errorf("an unauthorized charge should be refused as a charge at booking: %v", err)
	}
}

// The last guard before money moves: Stripe's own figure for the intent.
func TestCheckIntentAmount(t *testing.T) {
	charge := Charge{Base: 120, Tax: 10.65, DueNow: 130.65}
	if err := checkIntentAmount(13065, charge); err != nil {
		t.Errorf("the quoted amount was refused: %v", err)
	}
	if err := checkIntentAmount(12000, charge); err != nil {
		t.Errorf("less than the quote was refused: %v", err)
	}
	err := checkIntentAmount(13066, charge)
	if !errors.Is(err, ErrChargeAtBooking) || !strings.Contains(err.Error(), "nothing was charged") {
		t.Errorf("more than the quote must be refused before charging: %v", err)
	}
	if err := checkIntentAmount(5001, Charge{PreAuthorization: 50}); err == nil {
		t.Error("a pre-authorization above the no-show fee was allowed")
	}
}

func TestMoneyMovedNamesTheAmount(t *testing.T) {
	if got := moneyMoved(Charge{DueNow: 130.65}); got != "charged $130.65" {
		t.Errorf("moneyMoved = %q", got)
	}
	if got := moneyMoved(Charge{PreAuthorization: 50}); got != "pre-authorized for $50.00" {
		t.Errorf("moneyMoved = %q", got)
	}
}
