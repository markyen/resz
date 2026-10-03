package resyapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"resz/internal/platform"
)

const (
	calendarURL = BaseURL + "/4/venue/calendar"
	findURL     = BaseURL + "/4/find"

	// slotDateLayout is how /4/find formats a slot's local start time.
	slotDateLayout = "2006-01-02 15:04:05"
)

// slot is a bookable reservation slot as /4/find returns it, before
// normalization into a platform.Slot.
type slot struct {
	Config struct {
		Token string `json:"token"`
		Type  string `json:"type"`
	} `json:"config"`
	Date struct {
		Start string `json:"start"`
	} `json:"date"`
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
			Slots []slot `json:"slots"`
		} `json:"venues"`
	} `json:"results"`
}

// AvailableDates returns the dates (YYYY-MM-DD) over the next year where
// `inventory.reservation == "available"` for the given venue.
func AvailableDates(client *http.Client, venueID string, partySize int, logger func(string)) ([]string, error) {
	today := time.Now()
	q := url.Values{}
	q.Set("start_date", today.Format(platform.DateLayout))
	q.Set("end_date", today.AddDate(1, 0, 0).Format(platform.DateLayout))
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

// findAttempts is how many times a /4/find call is tried before giving
// up. The endpoint returns an occasional empty 500 — a few a day against
// roughly a hundred calls — and a lost call means a missed slot, so it is
// worth a few cheap retries.
const findAttempts = 5

// Slots returns every slot /4/find offers for the venue on `date`.
// Filtering to a booking window is the caller's job — see
// platform.FilterWindow.
//
// Transient 5xx responses are retried: see findAttempts.
func Slots(client *http.Client, venueID, date string, partySize int, venue *Venue, logger func(string)) ([]platform.Slot, error) {
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
	if err := getJSONRetrying(client, findURL+"?"+q.Encode(), &out, findAttempts, logger); err != nil {
		return nil, err
	}

	var slots []platform.Slot
	for _, v := range out.Results.Venues {
		for _, s := range v.Slots {
			start, err := time.Parse(slotDateLayout, s.Date.Start)
			if err != nil {
				continue
			}
			slots = append(slots, platform.Slot{
				Platform: platform.Resy,
				Start:    start,
				Ref:      s.Config.Token,
				Label:    s.Config.Type,
			})
		}
	}
	return slots, nil
}

// GetJSON issues a GET with the standard Resy headers and decodes a JSON
// response. Non-2xx responses become errors that include a snippet of the
// response body.
func GetJSON(client *http.Client, rawURL string, out any, logger func(string)) error {
	client = platform.HTTPClient(client)
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

// isRetriable reports whether an error from GetJSON looks like the
// endpoint's intermittent failure rather than a real answer. Only server
// errors qualify: a 4xx means the request itself is wrong and retrying it
// would just repeat the mistake.
func isRetriable(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, code := range []string{"http 500", "http 502", "http 503", "http 504"} {
		if strings.Contains(msg, code) {
			return true
		}
	}
	return false
}

// getJSONRetrying calls GetJSON until it succeeds, the error is not
// retriable, or attempts run out.
//
// The backoff starts short deliberately. snipe runs against a booking
// window where a lost second costs the table, so waiting out a flaky
// endpoint has to stay inside a second or so in the common case.
func getJSONRetrying(client *http.Client, rawURL string, out any, attempts int, logger func(string)) error {
	var err error
	delay := 100 * time.Millisecond
	for i := 1; i <= attempts; i++ {
		err = GetJSON(client, rawURL, out, logger)
		if err == nil {
			if i > 1 && logger != nil {
				logger(fmt.Sprintf("succeeded on attempt %d", i))
			}
			return nil
		}
		if !isRetriable(err) {
			return err
		}
		if i < attempts {
			if logger != nil {
				logger(fmt.Sprintf("attempt %d/%d: %v; retrying in %s", i, attempts, err, delay))
			}
			time.Sleep(delay)
			delay *= 2
		}
	}
	return fmt.Errorf("after %d attempts: %w", attempts, err)
}
