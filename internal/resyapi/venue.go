package resyapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
)

const venueURL = BaseURL + "/3/venue"

type Venue struct {
	Location struct {
		Code      string  `json:"code"`
		Latitude  float64 `json:"latitude"`
		Longitude float64 `json:"longitude"`
	} `json:"location"`
}

// GetVenue fetches venue metadata using the (unauthenticated) /3/venue
// endpoint. Pass nil for client to use http.DefaultClient.
func GetVenue(client *http.Client, id string) (*Venue, error) {
	if client == nil {
		client = http.DefaultClient
	}
	q := url.Values{}
	q.Set("id", id)

	req, err := http.NewRequest("GET", venueURL+"?"+q.Encode(), nil)
	if err != nil {
		return nil, err
	}
	setResyHeaders(req, "")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	var v Venue
	if err := json.NewDecoder(resp.Body).Decode(&v); err != nil {
		return nil, err
	}
	if v.Location.Code == "" {
		return nil, fmt.Errorf("venue %s has no location code", id)
	}
	return &v, nil
}

// GetVenueAuthed performs an authenticated GET against /3/venue. Useful
// purely to warm a TCP/TLS/HTTP-2 connection and exercise the auth path.
func GetVenueAuthed(client *http.Client, authToken, id string, logger func(string)) error {
	if client == nil {
		client = http.DefaultClient
	}
	q := url.Values{}
	q.Set("id", id)
	full := venueURL + "?" + q.Encode()

	if logger != nil {
		logger("GET " + full + " (warmup)")
	}
	req, err := http.NewRequest("GET", full, nil)
	if err != nil {
		return err
	}
	setResyHeaders(req, authToken)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)

	if resp.StatusCode/100 != 2 {
		return &APIError{Status: resp.StatusCode, Body: ""}
	}
	return nil
}

func (v *Venue) LatString() string  { return strconv.FormatFloat(v.Location.Latitude, 'f', -1, 64) }
func (v *Venue) LongString() string { return strconv.FormatFloat(v.Location.Longitude, 'f', -1, 64) }
