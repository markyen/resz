// Package accountconfig reads config/account.json, which holds the
// non-secret account details resz needs to identify you when booking.
//
// Secrets deliberately do not live here. Resy's password still comes from
// $RESY_PASSWORD and its token cache from state/auth.json. This file
// holds only the sort of thing you'd type into a restaurant's booking
// form: a name, an email address, a phone number.
package accountconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"resz/internal/respath"
)

// Config is the top-level account configuration file. Each platform that
// needs identifying details gets its own block; a missing block means
// resz has no details for that platform and will refuse to book on it.
type Config struct {
	SevenRooms *SevenRooms `json:"sevenrooms,omitempty"`
}

// SevenRooms holds the guest details every SevenRooms booking carries.
//
// SevenRooms bookings are guest checkouts: there is no account, no
// password and no token — each request simply states who the diner is.
type SevenRooms struct {
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Email     string `json:"email"`
	// Phone is the diner's number in E.164 form, e.g. "+15551234567".
	// resz splits it into the national number and dial code the API
	// wants.
	Phone string `json:"phone"`

	// CountryCode overrides the ISO 3166-1 alpha-2 country resz infers
	// from the dial code ("us", "gb", ...). Set it when the inference is
	// wrong — most usefully for +1 numbers, which resz assumes are US
	// rather than Canadian.
	CountryCode string `json:"country_code,omitempty"`
	// DialCode overrides the inferred international dial code, without
	// the leading "+".
	DialCode string `json:"dial_code,omitempty"`
}

// ErrNoConfig is returned when account.json is absent entirely.
var ErrNoConfig = fmt.Errorf("no account config: create config/account.json")

// ConfigPath returns the on-disk path of account.json.
func ConfigPath() (string, error) {
	dir, err := respath.Config()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "account.json"), nil
}

// Load reads account.json. A missing file yields ErrNoConfig, which
// callers can report as "booking isn't configured" rather than a crash.
func Load() (*Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, ErrNoConfig
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// LoadSevenRooms reads the SevenRooms block and validates it, returning a
// message that names the file when anything is missing.
func LoadSevenRooms() (*SevenRooms, error) {
	cfg, err := Load()
	if err != nil {
		return nil, err
	}
	if cfg.SevenRooms == nil {
		p, _ := ConfigPath()
		return nil, fmt.Errorf("no \"sevenrooms\" block in %s", p)
	}
	if err := cfg.SevenRooms.Validate(); err != nil {
		p, _ := ConfigPath()
		return nil, fmt.Errorf("%s: %w", p, err)
	}
	return cfg.SevenRooms, nil
}

// Validate checks that every field a booking needs is present and that
// the phone number can be split. It does not check that the details are
// genuine — only the venue can tell you that.
func (s *SevenRooms) Validate() error {
	for _, f := range []struct{ name, value string }{
		{"first_name", s.FirstName},
		{"last_name", s.LastName},
		{"email", s.Email},
		{"phone", s.Phone},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("sevenrooms.%s is required", f.name)
		}
	}
	if !strings.Contains(s.Email, "@") {
		return fmt.Errorf("sevenrooms.email %q is not an email address", s.Email)
	}
	_, _, _, err := s.PhoneParts()
	return err
}

// PhoneParts splits the configured phone number into the three pieces
// SevenRooms wants: the national number (digits only, no country code),
// the dial code, and the ISO country code.
//
// Explicit dial_code / country_code in the config always win. Otherwise
// the dial code is inferred by longest-prefix match against dialCodes
// below, which covers the markets SevenRooms operates in; anything else
// has to be stated explicitly rather than guessed wrong.
func (s *SevenRooms) PhoneParts() (national, dialCode, countryCode string, err error) {
	raw := strings.TrimSpace(s.Phone)
	digits := onlyDigits(raw)
	if digits == "" {
		return "", "", "", fmt.Errorf("sevenrooms.phone %q has no digits", s.Phone)
	}

	dialCode, countryCode = s.DialCode, strings.ToLower(s.CountryCode)

	if !strings.HasPrefix(raw, "+") {
		// Without a leading "+" there is no country information in the
		// number, so both parts have to come from the config.
		if dialCode == "" || countryCode == "" {
			return "", "", "", fmt.Errorf("sevenrooms.phone %q is not in E.164 form: write it as \"+1...\", or set sevenrooms.dial_code and sevenrooms.country_code", s.Phone)
		}
		return strings.TrimPrefix(digits, dialCode), dialCode, countryCode, nil
	}

	if dialCode == "" {
		code, country, ok := lookupDialCode(digits)
		if !ok {
			return "", "", "", fmt.Errorf("cannot infer the country of sevenrooms.phone %q: set sevenrooms.dial_code and sevenrooms.country_code explicitly", s.Phone)
		}
		dialCode = code
		if countryCode == "" {
			countryCode = country
		}
	}
	if countryCode == "" {
		if _, country, ok := lookupDialCode(digits); ok {
			countryCode = country
		} else {
			return "", "", "", fmt.Errorf("cannot infer the country of sevenrooms.phone %q: set sevenrooms.country_code explicitly", s.Phone)
		}
	}
	national = strings.TrimPrefix(digits, dialCode)
	if national == "" {
		return "", "", "", fmt.Errorf("sevenrooms.phone %q is only a country code", s.Phone)
	}
	return national, dialCode, countryCode, nil
}

// dialCodes maps international dial codes to a default ISO country, for
// the markets SevenRooms venues sit in. It is intentionally small: a
// wrong guess would put bad data on a real reservation, so an unlisted
// code asks the user instead. "1" resolves to the US; Canadian numbers
// need an explicit country_code.
var dialCodes = map[string]string{
	"1":   "us",
	"30":  "gr",
	"31":  "nl",
	"33":  "fr",
	"34":  "es",
	"39":  "it",
	"44":  "gb",
	"49":  "de",
	"52":  "mx",
	"55":  "br",
	"61":  "au",
	"64":  "nz",
	"65":  "sg",
	"81":  "jp",
	"351": "pt",
	"353": "ie",
	"852": "hk",
	"971": "ae",
}

// lookupDialCode finds the longest dial code that prefixes the number, so
// "852..." resolves to Hong Kong rather than to a shorter code that also
// happens to match.
func lookupDialCode(digits string) (dialCode, country string, ok bool) {
	for n := 3; n >= 1; n-- {
		if len(digits) <= n {
			continue
		}
		if c, found := dialCodes[digits[:n]]; found {
			return digits[:n], c, true
		}
	}
	return "", "", false
}

func onlyDigits(s string) string {
	var b strings.Builder
	for _, r := range s {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}
