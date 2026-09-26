package cardsecret

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// testNumber is Stripe's published test number: a valid Luhn sequence
// that is not anybody's card.
const testNumber = "4242424242424242"

const testEntry = testNumber + `
exp: 12/2088
cvc: 123
zip: 10014
name: Jane Doe`

func TestParse(t *testing.T) {
	c, err := Parse(testEntry)
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := c.Number(); got != testNumber {
		t.Errorf("Number = %q, want %q", got, testNumber)
	}
	if c.ExpMonth() != 12 || c.ExpYear() != 2088 {
		t.Errorf("exp = %02d/%d, want 12/2088", c.ExpMonth(), c.ExpYear())
	}
	if c.CVC() != "123" || c.PostalCode() != "10014" || c.Name() != "Jane Doe" {
		t.Errorf("cvc/zip/name = %q/%q/%q", c.CVC(), c.PostalCode(), c.Name())
	}
	if c.Last4() != "4242" {
		t.Errorf("Last4 = %q", c.Last4())
	}
}

func TestParseAcceptsFormattedNumberAndShortYear(t *testing.T) {
	c, err := Parse("4242 4242-4242 4242\nexp: 3/88\ncvc: 1234")
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if c.Number() != testNumber {
		t.Errorf("Number = %q; spaces and dashes should be stripped", c.Number())
	}
	if c.ExpYear() != 2088 {
		t.Errorf("ExpYear = %d, want 2088 (two-digit year is this century)", c.ExpYear())
	}
}

func TestParseRejectsBadEntries(t *testing.T) {
	tests := []struct {
		name, in, wantSubstr string
	}{
		{"empty", "  \n ", "empty"},
		{"typo in number", "4242424242424243\nexp: 12/2088\ncvc: 123", "checksum"},
		{"too short", "42424242\nexp: 12/2088\ncvc: 123", "digits"},
		{"letters", "4242abcd42424242\nexp: 12/2088\ncvc: 123", "non-digit"},
		{"no exp", testNumber + "\ncvc: 123", "no exp line"},
		{"bad month", testNumber + "\nexp: 13/2088\ncvc: 123", "month must be"},
		{"expired", testNumber + "\nexp: 01/2020\ncvc: 123", "expired"},
		{"short cvc", testNumber + "\nexp: 12/2088\ncvc: 12", "cvc"},
		{"cvc letters", testNumber + "\nexp: 12/2088\ncvc: 12a", "cvc"},
	}
	for _, tt := range tests {
		_, err := Parse(tt.in)
		if err == nil {
			t.Errorf("%s: expected an error", tt.name)
			continue
		}
		if !strings.Contains(err.Error(), tt.wantSubstr) {
			t.Errorf("%s: error %q does not mention %q", tt.name, err, tt.wantSubstr)
		}
	}
}

func TestExpiryIsGoodThroughEndOfMonth(t *testing.T) {
	c := &Card{number: testNumber, expMonth: 6, expYear: 2026, cvc: "123"}
	if err := c.validate(time.Date(2026, 6, 30, 23, 59, 0, 0, time.UTC)); err != nil {
		t.Errorf("last day of expiry month should be valid: %v", err)
	}
	if err := c.validate(time.Date(2026, 7, 1, 0, 0, 0, 0, time.UTC)); err == nil {
		t.Error("first day after expiry month should be rejected")
	}
}

func TestFromEnv(t *testing.T) {
	t.Setenv(EnvVar, testEntry)
	if _, err := FromEnv(); err != nil {
		t.Fatalf("FromEnv: %v", err)
	}
	t.Setenv(EnvVar, "")
	if _, err := FromEnv(); err != ErrNoCard {
		t.Errorf("FromEnv with empty env = %v, want ErrNoCard", err)
	}
}

// TestNoFmtVerbLeaksTheNumber is the one that matters: whatever a caller
// does with a Card in a log line, the digits must not come out.
func TestNoFmtVerbLeaksTheNumber(t *testing.T) {
	c, err := Parse(testEntry)
	if err != nil {
		t.Fatal(err)
	}
	for _, format := range []string{"%v", "%s", "%d", "%q", "%x", "%X", "%#v", "%+v", "%m"} {
		got := fmt.Sprintf(format, c)
		if strings.Contains(got, testNumber) {
			t.Errorf("%s leaked the full number: %s", format, got)
		}
		if strings.Contains(got, "123") && !strings.Contains(got, "••••") {
			t.Errorf("%s may have leaked the cvc: %s", format, got)
		}
	}
	// The same must hold for a Card reached through an enclosing struct,
	// which is how it would realistically show up in a log line.
	wrapper := struct{ Card *Card }{c}
	if got := fmt.Sprintf("%+v", wrapper); strings.Contains(got, testNumber) {
		t.Errorf("%%+v on an enclosing struct leaked the number: %s", got)
	}
}

func TestMarshalJSONRedacts(t *testing.T) {
	c, err := Parse(testEntry)
	if err != nil {
		t.Fatal(err)
	}
	b, err := json.Marshal(struct {
		Card *Card `json:"card"`
	}{c})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), testNumber) {
		t.Errorf("json.Marshal leaked the number: %s", b)
	}
	if !strings.Contains(string(b), "4242 exp") {
		t.Errorf("json.Marshal should still identify the card: %s", b)
	}
}

func TestRedactedNilCard(t *testing.T) {
	var c *Card
	if got := c.Redacted(); got != "(no card)" {
		t.Errorf("nil Card Redacted = %q", got)
	}
}
