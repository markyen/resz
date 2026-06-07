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

	"resz/internal/respath"
)

// Config is the top-level venues configuration file.
type Config struct {
	Venues        []ConfigEntry `json:"venues"`
	BlackoutDates []string      `json:"blackout_dates,omitempty"`
}

// ConfigEntry is one venue we care about. Identity (Name, ID) and the
// cross-feature settings (PartySize, City) live at the top level. The
// Snipe and Check sub-blocks describe per-feature configuration; each
// is independently opt-in via its presence in the JSON.
type ConfigEntry struct {
	Name      string       `json:"name"`
	ID        string       `json:"id"`
	PartySize int          `json:"party_size,omitempty"`
	City      string       `json:"city,omitempty"` // slug; drives search bias and snipe timezone
	Snipe     *SnipeConfig `json:"snipe,omitempty"`
	Check     *CheckConfig `json:"check,omitempty"`
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

// AddBlackout appends a YYYY-MM-DD date to BlackoutDates if it isn't
// already present. Returns true if the date was added.
func (c *Config) AddBlackout(date string) bool {
	for _, d := range c.BlackoutDates {
		if d == date {
			return false
		}
	}
	c.BlackoutDates = append(c.BlackoutDates, date)
	return true
}

// BlackoutSet converts a slice of date strings into a set for O(1) lookup.
func BlackoutSet(dates []string) map[string]bool {
	m := make(map[string]bool, len(dates))
	for _, d := range dates {
		m[d] = true
	}
	return m
}
