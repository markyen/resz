// Package cardsecret reads the payment card resz uses for venues that
// will only hold a table against one.
//
// The card is handled the same way as the Resy password: it is decrypted
// into the environment for the length of a run and nothing here ever
// writes it to disk. It is deliberately not part of config/account.json —
// that file holds only the sort of detail you would type into a booking
// form, and a card is not that.
//
// The Card type redacts itself under every fmt verb, so a stray %v in a
// log line prints "•••• 4242 exp 12/2028" rather than the number. Code
// that needs the real digits must ask for them explicitly via Number and
// CVC, which exist to make those call sites easy to audit.
package cardsecret

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// EnvVar is the environment variable the card is read from.
const EnvVar = "RESZ_CARD"

// ErrNoCard reports that no card is configured. Callers turn this into
// "this slot needs a card and none is set up" rather than a crash.
var ErrNoCard = fmt.Errorf("no payment card: set $%s", EnvVar)

// Card is a payment card. Construct it with FromEnv or Parse; the zero
// value is not usable.
//
// The fields are unexported so that neither encoding/json nor a %+v can
// reach the number by reflection over an exported field name.
type Card struct {
	number   string
	expMonth int
	expYear  int
	cvc      string
	postal   string
	name     string
}

// FromEnv reads the card from $RESZ_CARD. A missing or empty variable
// yields ErrNoCard.
func FromEnv() (*Card, error) {
	raw := os.Getenv(EnvVar)
	if strings.TrimSpace(raw) == "" {
		return nil, ErrNoCard
	}
	return Parse(raw)
}

// Parse reads the card entry. The first non-empty line is the card
// number and the remaining lines are "key: value" pairs:
//
//	4242424242424242
//	exp: 12/2028
//	cvc: 123
//	zip: 10014
//	name: Jane Doe
//
// Spaces and dashes in the number are ignored, so a number pasted in the
// form printed on the card works as-is.
func Parse(raw string) (*Card, error) {
	lines := strings.Split(strings.ReplaceAll(raw, "\r\n", "\n"), "\n")
	c := &Card{}
	first := true
	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		if first {
			c.number = strings.NewReplacer(" ", "", "-", "").Replace(ln)
			first = false
			continue
		}
		key, val, ok := strings.Cut(ln, ":")
		if !ok {
			continue
		}
		key = strings.ToLower(strings.TrimSpace(key))
		val = strings.TrimSpace(val)
		switch key {
		case "exp", "expiry", "expiration":
			m, y, err := parseExpiry(val)
			if err != nil {
				return nil, err
			}
			c.expMonth, c.expYear = m, y
		case "cvc", "cvv", "csc":
			c.cvc = val
		case "zip", "postal", "postal_code":
			c.postal = val
		case "name", "cardholder":
			c.name = val
		}
	}
	if first {
		return nil, fmt.Errorf("card entry is empty")
	}
	if err := c.validate(time.Now()); err != nil {
		return nil, err
	}
	return c, nil
}

// parseExpiry accepts MM/YY and MM/YYYY.
func parseExpiry(s string) (month, year int, err error) {
	ms, ys, ok := strings.Cut(s, "/")
	if !ok {
		return 0, 0, fmt.Errorf("card exp %q: want MM/YY or MM/YYYY", s)
	}
	month, err = strconv.Atoi(strings.TrimSpace(ms))
	if err != nil || month < 1 || month > 12 {
		return 0, 0, fmt.Errorf("card exp %q: month must be 01-12", s)
	}
	ys = strings.TrimSpace(ys)
	year, err = strconv.Atoi(ys)
	if err != nil {
		return 0, 0, fmt.Errorf("card exp %q: bad year", s)
	}
	// A two-digit year is this century: "28" means 2028.
	if len(ys) == 2 {
		year += 2000
	}
	return month, year, nil
}

// validate catches the mistakes worth catching before a booking attempt:
// a mistyped number, a card that has already expired, a missing CVC.
// Everything else is the gateway's business.
func (c *Card) validate(now time.Time) error {
	if len(c.number) < 12 || len(c.number) > 19 {
		return fmt.Errorf("card number has %d digits; want 12-19", len(c.number))
	}
	for _, r := range c.number {
		if r < '0' || r > '9' {
			return fmt.Errorf("card number has a non-digit character")
		}
	}
	if !luhn(c.number) {
		return fmt.Errorf("card number fails its checksum; check for a typo")
	}
	if c.expMonth == 0 || c.expYear == 0 {
		return fmt.Errorf("card entry has no exp line (want \"exp: MM/YYYY\")")
	}
	// A card is good through the last instant of its expiry month.
	end := time.Date(c.expYear, time.Month(c.expMonth), 1, 0, 0, 0, 0, time.UTC).AddDate(0, 1, 0)
	if !now.UTC().Before(end) {
		return fmt.Errorf("card expired %02d/%d", c.expMonth, c.expYear)
	}
	if len(c.cvc) < 3 || len(c.cvc) > 4 {
		return fmt.Errorf("card cvc has %d digits; want 3 or 4", len(c.cvc))
	}
	for _, r := range c.cvc {
		if r < '0' || r > '9' {
			return fmt.Errorf("card cvc has a non-digit character")
		}
	}
	return nil
}

// luhn reports whether the digits satisfy the Luhn checksum every card
// number carries.
func luhn(digits string) bool {
	sum, alt := 0, false
	for i := len(digits) - 1; i >= 0; i-- {
		d := int(digits[i] - '0')
		if alt {
			if d *= 2; d > 9 {
				d -= 9
			}
		}
		sum += d
		alt = !alt
	}
	return sum%10 == 0
}

// Number returns the full card number. Call it only where the number is
// genuinely being handed to the payment gateway.
func (c *Card) Number() string { return c.number }

// CVC returns the card's security code. Same caution as Number.
func (c *Card) CVC() string { return c.cvc }

// ExpMonth and ExpYear are the card's expiry.
func (c *Card) ExpMonth() int { return c.expMonth }
func (c *Card) ExpYear() int  { return c.expYear }

// PostalCode is the billing postal code, empty when the entry omits it.
func (c *Card) PostalCode() string { return c.postal }

// Name is the cardholder name, empty when the entry omits it.
func (c *Card) Name() string { return c.name }

// Last4 is the last four digits, for confirming which card was used.
func (c *Card) Last4() string {
	if len(c.number) < 4 {
		return ""
	}
	return c.number[len(c.number)-4:]
}

// Redacted is the safe description of the card, e.g.
// "•••• 4242 exp 12/2028".
func (c *Card) Redacted() string {
	if c == nil {
		return "(no card)"
	}
	return fmt.Sprintf("•••• %s exp %02d/%d", c.Last4(), c.expMonth, c.expYear)
}

// String makes Redacted the default rendering, so %v and %s in a log line
// cannot print the number.
func (c *Card) String() string { return c.Redacted() }

// GoString covers %#v, which would otherwise dump the struct fields.
func (c *Card) GoString() string { return "cardsecret.Card(" + c.Redacted() + ")" }

// Format covers every remaining verb — %q and %x included — so there is
// no fmt route to the digits.
func (c *Card) Format(f fmt.State, verb rune) {
	switch verb {
	case 'q':
		fmt.Fprintf(f, "%q", c.Redacted())
	default:
		fmt.Fprint(f, c.Redacted())
	}
}

// MarshalJSON keeps the card redacted if it is ever caught up in a struct
// someone serializes. There is deliberately no UnmarshalJSON: a card must
// come from the environment, never from a file resz reads.
func (c *Card) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(c.Redacted())), nil
}
