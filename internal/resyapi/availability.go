package resyapi

import (
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"time"
)

const (
	calendarURL = BaseURL + "/4/venue/calendar"
	findURL     = BaseURL + "/4/find"
)

// Slot is a bookable reservation slot returned by /4/find.
type Slot struct {
	Config struct {
		Token string `json:"token"`
		Type  string `json:"type"`
	} `json:"config"`
	Date struct {
		Start string `json:"start"`
	} `json:"date"`
}

// StartLabel renders the slot's start time as "Mon, Jan 2 at 3:04 PM",
// falling back to the raw API string if it can't be parsed.
func (s Slot) StartLabel() string {
	t, err := time.Parse("2006-01-02 15:04:05", s.Date.Start)
	if err != nil {
		return s.Date.Start
	}
	return t.Format("Mon, Jan 2 at 3:04 PM")
}

type calendarResponse struct {
	Scheduled []struct {
		Date      string `json:"date"`
		Inventory struct {
			Reservation string `json:"reservation"`
		} `json:"inventory"`
	} `json:"scheduled"`
}

type findResponse struct {
	Results struct {
		Venues []struct {
			Slots []Slot `json:"slots"`
		} `json:"venues"`
	} `json:"results"`
}

// AvailableDates returns the dates (YYYY-MM-DD) over the next year where
// `inventory.reservation == "available"` for the given venue.
func AvailableDates(client *http.Client, venueID string, partySize int, logger func(string)) ([]string, error) {
	today := time.Now()
	q := url.Values{}
	q.Set("start_date", today.Format("2006-01-02"))
	q.Set("end_date", today.AddDate(1, 0, 0).Format("2006-01-02"))
	q.Set("num_seats", strconv.Itoa(partySize))
	q.Set("venue_id", venueID)

	var out calendarResponse
	if err := GetJSON(client, calendarURL+"?"+q.Encode(), &out, logger); err != nil {
		return nil, err
	}

	var dates []string
	for _, d := range out.Scheduled {
		if d.Inventory.Reservation == "available" {
			dates = append(dates, d.Date)
		}
	}
	return dates, nil
}

// Window is an inclusive HH:MM interval. Pass nil to SlotsInWindow to
// fall back to the default weekday 18:30-20:00 / weekend 11:30-20:00.
type Window struct {
	Earliest string
	Latest   string
}

// SlotsInWindow returns slots on `date` whose start time falls within the
// given window (or the default weekday/weekend window if window is nil).
func SlotsInWindow(client *http.Client, venueID, date string, partySize int, venue *Venue, window *Window, logger func(string)) ([]Slot, error) {
	q := url.Values{}
	q.Set("day", date)
	q.Set("lat", venue.LatString())
	q.Set("long", venue.LongString())
	q.Set("location", venue.Location.Code)
	q.Set("party_size", strconv.Itoa(partySize))
	q.Set("user_lat", venue.LatString())
	q.Set("user_long", venue.LongString())
	q.Set("venue_id", venueID)

	var out findResponse
	if err := GetJSON(client, findURL+"?"+q.Encode(), &out, logger); err != nil {
		return nil, err
	}

	day, err := time.Parse("2006-01-02", date)
	if err != nil {
		return nil, err
	}
	weekend := day.Weekday() == time.Saturday || day.Weekday() == time.Sunday

	var earliest, latest time.Time
	switch {
	case window != nil:
		var e1, e2 error
		earliest, e1 = parseHM(date, window.Earliest)
		latest, e2 = parseHM(date, window.Latest)
		if e1 != nil {
			return nil, fmt.Errorf("invalid time_range start %q: want HH:MM", window.Earliest)
		}
		if e2 != nil {
			return nil, fmt.Errorf("invalid time_range end %q: want HH:MM", window.Latest)
		}
	case weekend:
		earliest, _ = parseHM(date, "11:30")
		latest, _ = parseHM(date, "20:00")
	default:
		earliest, _ = parseHM(date, "18:30")
		latest, _ = parseHM(date, "20:00")
	}

	var matched []Slot
	for _, v := range out.Results.Venues {
		for _, s := range v.Slots {
			t, err := time.Parse("2006-01-02 15:04:05", s.Date.Start)
			if err != nil {
				continue
			}
			if t.Before(earliest) || t.After(latest) {
				continue
			}
			matched = append(matched, s)
		}
	}
	return matched, nil
}

// BestSlotIndex returns the index in `slots` of the slot whose start time
// is closest to `preferred` ("HH:MM" on the slot's own date). Returns -1
// if slots is empty.
func BestSlotIndex(slots []Slot, preferred string) int {
	if len(slots) == 0 {
		return -1
	}
	bestDelta := math.MaxInt64
	best := -1
	for i, s := range slots {
		t, err := time.Parse("2006-01-02 15:04:05", s.Date.Start)
		if err != nil {
			continue
		}
		// Build target time on the same date as the slot.
		date := t.Format("2006-01-02")
		target, _ := parseHM(date, preferred)
		delta := int(math.Abs(float64(t.Sub(target))))
		if delta < bestDelta {
			bestDelta = delta
			best = i
		}
	}
	return best
}

func parseHM(date, hm string) (time.Time, error) {
	return time.Parse("2006-01-02 15:04", date+" "+hm)
}

// GetJSON issues a GET with the standard Resy headers and decodes a JSON
// response. Non-2xx responses become errors that include a snippet of the
// response body.
func GetJSON(client *http.Client, rawURL string, out any, logger func(string)) error {
	if client == nil {
		client = http.DefaultClient
	}
	if logger != nil {
		logger("GET " + rawURL)
	}
	req, err := http.NewRequest("GET", rawURL, nil)
	if err != nil {
		return err
	}
	setResyHeaders(req, "")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		snippet := string(body)
		if logger == nil && len(snippet) > 200 {
			snippet = snippet[:200] + "...(truncated)"
		}
		return fmt.Errorf("http %d: %s", resp.StatusCode, snippet)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
