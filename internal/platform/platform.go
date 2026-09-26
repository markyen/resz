// Package platform defines the reservation-platform abstraction shared by
// resz's subcommands: the set of supported platforms, a platform-neutral
// Slot, and the VenueClient interface each platform's API package
// implements. Slot selection — window filtering and "closest to the
// preferred time" — lives here too, so it behaves identically no matter
// which platform a venue is on.
package platform

import (
	"fmt"
	"math"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Platform identifies a reservation platform.
type Platform string

const (
	Resy       Platform = "resy"
	SevenRooms Platform = "sevenrooms"
)

// Default is the platform assumed wherever one isn't named — config
// entries written before SevenRooms support existed, and CLI flags left
// at their default.
const Default = Resy

// All returns the supported platforms in a stable order.
func All() []Platform { return []Platform{Resy, SevenRooms} }

// Parse resolves a platform name. It accepts a few spellings of
// "sevenrooms" because the venue's own booking URLs are inconsistent
// about it.
func Parse(s string) (Platform, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "resy":
		return Resy, nil
	case "sevenrooms", "seven-rooms", "seven_rooms", "7rooms", "sr":
		return SevenRooms, nil
	}
	return "", fmt.Errorf("unknown platform %q (want %q or %q)", s, Resy, SevenRooms)
}

// Names returns the canonical platform names, for flag help text.
func Names() []string {
	out := make([]string, 0, len(All()))
	for _, p := range All() {
		out = append(out, string(p))
	}
	return out
}

// Slot is one reservation slot, normalized across platforms.
//
// Start is the slot's wall-clock time at the venue, carried in a
// time.Time whose location is UTC. Every comparison resz makes — against
// a booking window, against the preferred time — is between two
// wall-clock values on the same calendar date, so the location is
// irrelevant as long as it is applied consistently. Both platforms report
// slot times in venue-local wall clock, which is what lands here.
type Slot struct {
	Platform Platform
	Start    time.Time

	// Ref is the platform-specific booking reference: an "rgs://" config
	// id on Resy, an "sr://" URI on SevenRooms. It is what `resz
	// availability` prints and `resz book` consumes.
	Ref string

	// Label is a short description of the table or seating type, or ""
	// when the platform doesn't provide one.
	Label string
}

// StartLabel renders the slot's start time as "Mon, Jan 2 at 3:04 PM".
func (s Slot) StartLabel() string { return s.Start.Format("Mon, Jan 2 at 3:04 PM") }

// Date returns the slot's calendar date as YYYY-MM-DD.
func (s Slot) Date() string { return s.Start.Format(DateLayout) }

// DateLayout is the YYYY-MM-DD form used for dates throughout resz, on
// the wire and in config.
const DateLayout = "2006-01-02"

// VenueClient reads availability for one venue on one platform. Each
// platform's API package provides an implementation; callers get one from
// bookflow.Open and then work only in terms of this interface.
//
// Implementations may cache per-venue metadata and per-date results, so
// callers should reuse a single VenueClient for a venue rather than
// reopening it per date.
type VenueClient interface {
	// Platform reports which platform this venue is on.
	Platform() Platform

	// ID is the venue's platform-native identifier: a numeric id on Resy,
	// a URL slug on SevenRooms.
	ID() string

	// Name is the venue's display name, or "" if the platform doesn't
	// cheaply expose one.
	Name() string

	// Timezone is the venue's IANA timezone name, or "" if unknown.
	Timezone() string

	// AvailableDates returns the candidate dates (YYYY-MM-DD, ascending)
	// worth examining for the given party size, over the platform's
	// booking horizon. Platforms differ in how precise this is: Resy
	// reports dates that genuinely have inventory, while SevenRooms only
	// reports dates the venue is open for booking. Callers must confirm
	// with SlotsOn either way.
	AvailableDates(partySize int, logger func(string)) ([]string, error)

	// SlotsOn returns every bookable slot on date for the given party
	// size, in whatever order the platform returns them. An empty result
	// with a nil error means "nothing bookable that day".
	SlotsOn(date string, partySize int, logger func(string)) ([]Slot, error)
}

// Window is an inclusive HH:MM interval in the venue's local time.
type Window struct {
	Earliest string
	Latest   string
}

// Resolve returns the inclusive wall-clock bounds the window describes on
// `date`. A nil Window falls back to resz's default booking window:
// 11:30-20:00 at weekends, 18:30-20:00 on weekdays.
func (w *Window) Resolve(date string) (earliest, latest time.Time, err error) {
	day, err := time.Parse(DateLayout, date)
	if err != nil {
		return time.Time{}, time.Time{}, err
	}
	if w != nil {
		e, eerr := ParseHM(date, w.Earliest)
		if eerr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid time_range start %q: want HH:MM", w.Earliest)
		}
		l, lerr := ParseHM(date, w.Latest)
		if lerr != nil {
			return time.Time{}, time.Time{}, fmt.Errorf("invalid time_range end %q: want HH:MM", w.Latest)
		}
		return e, l, nil
	}
	if wd := day.Weekday(); wd == time.Saturday || wd == time.Sunday {
		e, _ := ParseHM(date, "11:30")
		l, _ := ParseHM(date, "20:00")
		return e, l, nil
	}
	e, _ := ParseHM(date, "18:30")
	l, _ := ParseHM(date, "20:00")
	return e, l, nil
}

// FilterWindow returns the subset of slots on `date` whose start time
// falls inside the window (or the default weekday/weekend window when
// window is nil).
func FilterWindow(slots []Slot, date string, window *Window) ([]Slot, error) {
	earliest, latest, err := window.Resolve(date)
	if err != nil {
		return nil, err
	}
	var matched []Slot
	for _, s := range slots {
		if s.Start.Before(earliest) || s.Start.After(latest) {
			continue
		}
		matched = append(matched, s)
	}
	return matched, nil
}

// BestSlotIndex returns the index of the slot whose start time is closest
// to `preferred` ("HH:MM", interpreted on the slot's own date), or -1 if
// slots is empty.
func BestSlotIndex(slots []Slot, preferred string) int {
	best, bestDelta := -1, int64(math.MaxInt64)
	for i, s := range slots {
		if d := preferenceDelta(s, preferred); d < bestDelta {
			bestDelta, best = d, i
		}
	}
	return best
}

// SortByPreference orders slots in place, closest to `preferred` first.
func SortByPreference(slots []Slot, preferred string) {
	sort.SliceStable(slots, func(i, j int) bool {
		return preferenceDelta(slots[i], preferred) < preferenceDelta(slots[j], preferred)
	})
}

// preferenceDelta is a slot's absolute distance from `preferred` on its
// own date. Slots whose date can't be reconciled with preferred sort
// last rather than being dropped.
func preferenceDelta(s Slot, preferred string) int64 {
	target, err := ParseHM(s.Date(), preferred)
	if err != nil {
		return math.MaxInt64
	}
	return int64(math.Abs(float64(s.Start.Sub(target))))
}

// ParseHM parses "HH:MM" on the given YYYY-MM-DD date into the same
// wall-clock (UTC-located) representation Slot.Start uses.
func ParseHM(date, hm string) (time.Time, error) {
	return time.Parse("2006-01-02 15:04", date+" "+hm)
}

// SearchHit is one result from a cross-platform venue search.
type SearchHit struct {
	Platform Platform
	// ID is the value to store in the config and pass to `resz
	// availability`: a numeric id on Resy, a URL slug on SevenRooms.
	ID   string
	Name string
	// City is the venue's city, when the platform reports one.
	City string
}

// HTTPClient returns client, or http.DefaultClient when client is nil, so
// API packages can accept an optional client uniformly.
func HTTPClient(client *http.Client) *http.Client {
	if client == nil {
		return http.DefaultClient
	}
	return client
}
