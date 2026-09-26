// Package sevenroomsapi is a client for the SevenRooms reservation
// widget API — the endpoints behind
// https://www.sevenrooms.com/explore/<venue>/reservations/create/search/
// — covering venue lookup, availability, and booking.
//
// Venues are addressed by their URL slug ("blueheron"), which is the
// last meaningful path segment of that booking URL. Some venues are
// served from vanity hosts (fp.sevenrooms.com, and custom domains), but
// www.sevenrooms.com answers for all of them, so this package always
// talks to www.
//
// Reading availability needs no credentials at all. Booking is a guest
// checkout — see book.go and reserve.go — which carries the diner's
// details inline rather than authenticating.
package sevenroomsapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"resz/internal/platform"
)

const (
	// BaseURL is the host every widget endpoint lives on.
	BaseURL = "https://www.sevenrooms.com"

	// UserAgent identifies resz as a normal browser client. The widget
	// API rejects some obviously-scripted agents.
	UserAgent = "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36"

	// Channel is the `channel` parameter the public widget sends.
	Channel = "SEVENROOMS_WIDGET"
)

// setHeaders applies the headers the widget endpoints expect. There is no
// API key: the read endpoints are unauthenticated.
func setHeaders(req *http.Request) {
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", UserAgent)
}

// apiError is a non-2xx response from a widget endpoint.
type apiError struct {
	Status int
	Body   string
}

func (e *apiError) Error() string { return fmt.Sprintf("http %d: %s", e.Status, e.Body) }

// errNotFound reports whether err is a 404, which SevenRooms returns for
// an unknown venue slug. Search uses this to tell "no such venue" apart
// from a real failure.
func errNotFound(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusNotFound
}

// getJSON issues a GET against a widget endpoint and decodes the JSON
// envelope's `data` field into out.
//
// Every endpoint wraps its payload as {"status": 200, "data": {...}} and
// signals failure with both an HTTP status and a `msg`, so a non-2xx
// response is turned into an apiError carrying that message.
func getJSON(client *http.Client, path string, q url.Values, out any, logger func(string)) error {
	full := BaseURL + path
	if len(q) > 0 {
		full += "?" + q.Encode()
	}
	if logger != nil {
		logger("GET " + full)
	}
	req, err := http.NewRequest("GET", full, nil)
	if err != nil {
		return err
	}
	setHeaders(req)

	resp, err := platform.HTTPClient(client).Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return &apiError{Status: resp.StatusCode, Body: message(body)}
	}
	var env struct {
		Status int             `json:"status"`
		Data   json.RawMessage `json:"data"`
	}
	if err := json.Unmarshal(body, &env); err != nil {
		return fmt.Errorf("decode envelope: %w", err)
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(env.Data, out)
}

// message extracts the API's `msg` field from an error body, falling back
// to a truncated copy of the body itself.
func message(body []byte) string {
	var e struct {
		Msg string `json:"msg"`
	}
	if json.Unmarshal(body, &e) == nil && e.Msg != "" {
		return e.Msg
	}
	s := strings.TrimSpace(string(body))
	if len(s) > 200 {
		s = s[:200] + "...(truncated)"
	}
	return s
}
