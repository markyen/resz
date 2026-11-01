// Package venueconfig holds the shared on-disk schema (config/venues.json)
// used by resz's snipe, check, and availability subcommands. It is the
// single source of truth for the Config / ConfigEntry types and the
// load/save helpers, so the three subcommands can never drift apart.
package venueconfig

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"resz/internal/platform"
	"resz/internal/respath"
)

// Config is the top-level venues configuration file.
//
// BlackoutDates (blackout_dates) are YYYY-MM-DD dates on which nothing is
// booked. A successful booking adds dates to it, and `resz tick` drops
// dates before today.
//
// BookingBlackout (booking_blackout_days) is how many days a successful
// booking blacks out, centered on the booked date; an even number puts
// the extra day after it (2: the date and the next; 4: one before, two
// after). It is global: it applies to every booking, whatever the venue.
// Absent or 0 means 1, the booked date only; Load refuses a value outside
// 0 to 31. Read it through BookingBlackoutDays.
type Config struct {
	Venues          []ConfigEntry `json:"venues"`
	BlackoutDates   []string      `json:"blackout_dates,omitempty"`
	BookingBlackout int           `json:"booking_blackout_days,omitempty"`
}

// MaxBookingBlackoutDays bounds booking_blackout_days.
const MaxBookingBlackoutDays = 31

// ConfigEntry is one venue we care about. Identity (Platform, Name, ID)
// and the cross-feature settings (PartySize, City) live at the top level.
// The Snipe and Check sub-blocks describe per-feature configuration; each
// is independently opt-in via its presence in the JSON.
type ConfigEntry struct {
	Name string `json:"name"`
	// ID is the venue's platform-native identifier: a numeric id on Resy,
	// a URL slug on SevenRooms.
	ID string `json:"id"`
	// Plat names the reservation platform the venue is on. An empty value
	// means Resy, so configs written before SevenRooms support keep
	// working unchanged. Read it through the Platform method.
	Plat      string `json:"platform,omitempty"`
	PartySize int    `json:"party_size,omitempty"`
	City      string `json:"city,omitempty"` // Resy city slug; drives search bias and snipe timezone
	// AutoBookCard lets check and snipe book this venue's card-required slots
	// without asking first. It applies only to slots that charge nothing
	// at booking — a slot with a real cost always goes to a human,
	// whatever this says, so a venue that starts asking for a deposit
	// cannot quietly start spending money. Note that "charges nothing"
	// is not "costs nothing": these venues keep a card on file and bill
	// it for a no-show or a late cancellation.
	AutoBookCard bool         `json:"auto_book_card,omitempty"`
	Snipe        *SnipeConfig `json:"snipe,omitempty"`
	Check        *CheckConfig `json:"check,omitempty"`
}

// Platform returns the entry's reservation platform, defaulting to Resy
// when the field is absent or unrecognized. An unrecognized value is
// reported so callers can warn instead of silently checking the wrong
// platform.
func (e ConfigEntry) Platform() (platform.Platform, error) {
	if e.Plat == "" {
		return platform.Default, nil
	}
	return platform.Parse(e.Plat)
}

// SnipeConfig configures the `resz snipe` subcommand for a venue.
// Presence of this block enables snipe for the venue; absence skips it.
type SnipeConfig struct {
	TargetTime string `json:"target_time"` // "HH:MM" in the city's timezone
	OffsetDays int    `json:"offset_days"` // booking targets today + N days
}

// CheckConfig configures the `resz check` subcommand for a venue.
// Presence enables check for the venue; an empty `{}` block matches the
// default behavior. DateRange and TimeRange optionally narrow the search.
type CheckConfig struct {
	DateRange *DateRange `json:"date_range,omitempty"`
	TimeRange *TimeRange `json:"time_range,omitempty"`
}

// DateRange is an inclusive YYYY-MM-DD interval. When set on a
// CheckConfig, only dates in [Start, End] (minus blackouts) are
// considered.
type DateRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// TimeRange is an inclusive HH:MM interval in the venue's local time.
// When set on a CheckConfig, it overrides the default weekday/weekend
// bookable-slot window for that venue.
type TimeRange struct {
	Start string `json:"start"`
	End   string `json:"end"`
}

// DefaultPath returns <workspace>/config/venues.json.
func DefaultPath() (string, error) {
	dir, err := respath.Config()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "venues.json"), nil
}

// Load reads a Config from path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if n := cfg.BookingBlackout; n < 0 || n > MaxBookingBlackoutDays {
		return nil, fmt.Errorf("%s: booking_blackout_days is %d; it must be 1 to %d (absent or 0 means 1)",
			path, n, MaxBookingBlackoutDays)
	}
	return &cfg, nil
}

// Save writes a Config to path atomically via a temp file rename.
func Save(path string, cfg *Config) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(cfg, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// AddBlackout inserts a YYYY-MM-DD date into BlackoutDates if it isn't
// already present, keeping the list sorted (which is chronological for
// YYYY-MM-DD). Returns true if the date was added.
func (c *Config) AddBlackout(date string) bool {
	for _, d := range c.BlackoutDates {
		if d == date {
			return false
		}
	}
	c.BlackoutDates = append(c.BlackoutDates, date)
	sort.Strings(c.BlackoutDates)
	return true
}

// BookingBlackoutDays returns how many days a booking blacks out: the
// configured booking_blackout_days, or 1 when it is absent or 0.
func (c *Config) BookingBlackoutDays() int {
	if c.BookingBlackout <= 0 {
		return 1
	}
	return c.BookingBlackout
}

// BookingWindow returns the days YYYY-MM-DD dates centered on date, in
// order: (days-1)/2 before it and the rest after it, so an even count
// puts the extra day after. Dates are calendar dates, stepped in UTC, so
// no daylight-saving change can shift one.
func BookingWindow(date string, days int) ([]string, error) {
	d, err := time.ParseInLocation(platform.DateLayout, date, time.UTC)
	if err != nil {
		return nil, fmt.Errorf("booking date %q: %w", date, err)
	}
	if days < 1 || days > MaxBookingBlackoutDays {
		return nil, fmt.Errorf("booking window of %d days: it must be 1 to %d", days, MaxBookingBlackoutDays)
	}
	before := (days - 1) / 2
	out := make([]string, 0, days)
	for k := -before; k < days-before; k++ {
		out = append(out, d.AddDate(0, 0, k).Format(platform.DateLayout))
	}
	return out, nil
}

// AddBookingBlackout blacks out the booking window around date (see
// BookingBlackoutDays) and returns the dates it added, in order; dates
// already blacked out are skipped. A window's dates before today are
// added like the rest; tick's housekeep drops them on its next pass. If
// no window can be made around date, the date alone is added, and the
// error is returned with it.
func (c *Config) AddBookingBlackout(date string) ([]string, error) {
	window, err := BookingWindow(date, c.BookingBlackoutDays())
	if err != nil {
		if c.AddBlackout(date) {
			return []string{date}, err
		}
		return nil, err
	}
	var added []string
	for _, d := range window {
		if c.AddBlackout(d) {
			added = append(added, d)
		}
	}
	return added, nil
}

// DateSpan renders added dates for a log line: "2026-11-12" for one,
// "2026-11-12..2026-11-15" for a run of consecutive days, and a comma
// list when dates already blacked out break the run.
func DateSpan(dates []string) string {
	if len(dates) > 1 {
		first, err1 := time.ParseInLocation(platform.DateLayout, dates[0], time.UTC)
		last, err2 := time.ParseInLocation(platform.DateLayout, dates[len(dates)-1], time.UTC)
		if err1 == nil && err2 == nil && first.AddDate(0, 0, len(dates)-1).Equal(last) {
			return dates[0] + ".." + dates[len(dates)-1]
		}
	}
	return strings.Join(dates, ", ")
}

// BlackoutSet converts a slice of date strings into a set for O(1) lookup.
func BlackoutSet(dates []string) map[string]bool {
	m := make(map[string]bool, len(dates))
	for _, d := range dates {
		m[d] = true
	}
	return m
}
