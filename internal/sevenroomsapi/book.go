package sevenroomsapi

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strconv"
	"strings"

	"resz/internal/platform"
)

const (
	holdPath = "/api-yoa/dining/hold/add"
	// bookPath and releasePath are templated with the venue slug. Note
	// they live under /booking, not /api-yoa, and answer with a bare JSON
	// value rather than the usual {status, data} envelope.
	bookPathFmt    = "/booking/dining/widget/%s/book"
	releasePathFmt = "/booking/dining/widget/%s/release-reservation-hold"

	// widgetOrigin tells the backend which widget generation sent the
	// request. The public reservation widget reports "new-widget".
	widgetOrigin = "new-widget"
)

// Guest is the set of diner details every SevenRooms booking carries.
// There is no account and no token: identity travels with the request.
type Guest struct {
	FirstName string
	LastName  string
	Email     string
	// PhoneNumber is the national number, digits only, with no country
	// code — the country travels separately in DialCode.
	PhoneNumber string
	DialCode    string // international dial code without "+", e.g. "1"
	CountryCode string // ISO 3166-1 alpha-2, lowercase, e.g. "us"
}

// Validate reports whether the guest has everything a booking needs.
func (g Guest) Validate() error {
	for _, f := range []struct{ name, value string }{
		{"first name", g.FirstName},
		{"last name", g.LastName},
		{"email", g.Email},
		{"phone number", g.PhoneNumber},
		{"dial code", g.DialCode},
		{"country code", g.CountryCode},
	} {
		if strings.TrimSpace(f.value) == "" {
			return fmt.Errorf("guest %s is required", f.name)
		}
	}
	return nil
}

// checkoutHash is the X-Checkout-Hash header the widget sends: the hex
// SHA-256 of "<first>|<last>", both trimmed and lowercased.
func (g Guest) checkoutHash() string {
	sum := sha256.Sum256([]byte(strings.ToLower(strings.TrimSpace(g.FirstName)) + "|" +
		strings.ToLower(strings.TrimSpace(g.LastName))))
	return hex.EncodeToString(sum[:])
}

// Hold is a temporary claim on a table, created before checkout so the
// slot can't be taken while the booking is submitted.
type Hold struct {
	ID                 string `json:"reservation_hold_id"`
	DurationSec        int    `json:"hold_duration_sec"`
	ShiftPersistentID  string `json:"shift_persistent_id"`
	AccessPersistentID string `json:"access_persistent_id"`
}

// Reservation is the outcome of a successful booking.
type Reservation struct {
	// ID is the reservation's identifier, as SevenRooms names it in the
	// response. Which key carries it varies by venue configuration, so
	// Raw is kept for anything this struct doesn't model.
	ID string
	// Confirmation is the short human-facing code the venue puts in its
	// confirmation email, e.g. "ABCD1234EFG". It is what a person reads
	// out at the host stand, so it is worth surfacing over the opaque id.
	Confirmation string
	Raw          []byte
}

// CreateHold claims the table behind ref for a few minutes.
//
// Two details are load-bearing and were established against the live API:
// `venue` must be the URL slug (the internal venue id is rejected), and
// `picked_duration` must be omitted unless the venue offers selectable
// durations — sending it makes the hold fail with a 470.
func CreateHold(client *http.Client, ref Ref, logger func(string)) (*Hold, error) {
	clock, err := ref.ClockTime()
	if err != nil {
		return nil, err
	}
	body := map[string]any{
		"venue":      ref.Venue,
		"date":       ref.Date(),
		"time":       clock,
		"party_size": ref.PartySize,
		"channel":    Channel,
	}
	if ref.AccessPersistentID != "" {
		body["access_persistent_id"] = ref.AccessPersistentID
	}
	if ref.ShiftPersistentID != "" {
		body["shift_persistent_id"] = ref.ShiftPersistentID
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	if logger != nil {
		logger("POST " + BaseURL + holdPath + " body=" + string(buf))
	}
	req, err := http.NewRequest("POST", BaseURL+holdPath, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	setHeaders(req)
	req.Header.Set("Content-Type", "application/json")

	resp, err := platform.HTTPClient(client).Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &apiError{Status: resp.StatusCode, Body: holdMessage(resp.StatusCode, raw)}
	}

	var env struct {
		Data Hold `json:"data"`
	}
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, fmt.Errorf("decode hold response: %w", err)
	}
	if env.Data.ID == "" {
		return nil, fmt.Errorf("hold response carried no reservation_hold_id: %s", truncate(raw))
	}
	return &env.Data, nil
}

// holdMessage annotates the hold endpoint's opaque failure codes, which
// carry no detail beyond "Could not add reservation hold".
func holdMessage(status int, body []byte) string {
	msg := message(body)
	if status == 470 || status == 473 {
		msg += " (the slot was probably taken while we looked, or the venue changed its availability)"
	}
	return msg
}

// ReleaseHold gives a held table back. It is best-effort cleanup for a
// booking that failed after the hold succeeded; holds also lapse on their
// own after Hold.DurationSec.
func ReleaseHold(client *http.Client, ref Ref, hold *Hold, logger func(string)) error {
	fields := map[string]string{
		"reservation_hold_id": hold.ID,
		"shift_persistent_id": ref.ShiftPersistentID,
		"date":                ref.Date(),
	}
	path := fmt.Sprintf(releasePathFmt, ref.Venue)
	if logger != nil {
		logger("POST " + BaseURL + path + " (release hold " + hold.ID + ")")
	}
	resp, err := postForm(client, path, fields, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return &apiError{Status: resp.StatusCode, Body: message(raw)}
	}
	return nil
}

// Book completes a reservation against an existing hold.
//
// The payload mirrors what the reservation widget submits: the slot's
// identity, the hold, and the guest's details, flattened into one
// multipart form. Marketing and SMS opt-ins are deliberately absent —
// the widget omits them when they're declined, so omitting them opts out.
func Book(client *http.Client, ref Ref, venue *Venue, guest Guest, hold *Hold, logger func(string)) (*Reservation, error) {
	return BookWithCard(client, ref, venue, guest, hold, "", logger)
}

// BookWithCard is Book with a confirmed SetupIntent id attached, for a
// venue that will only hold the table against a card. The id travels in
// the form's card_token field — despite the name it is a "seti_..."
// SetupIntent id, not a card or payment-method token.
//
// An empty cardToken books exactly as Book does.
func BookWithCard(client *http.Client, ref Ref, venue *Venue, guest Guest, hold *Hold, cardToken string, logger func(string)) (*Reservation, error) {
	if err := guest.Validate(); err != nil {
		return nil, err
	}
	bookDate, err := ref.BookDate()
	if err != nil {
		return nil, err
	}
	clock, err := ref.ClockTime()
	if err != nil {
		return nil, err
	}

	fields := map[string]string{
		// Reservation identity.
		"venue":                ref.Venue,
		"date":                 bookDate,
		"time":                 clock,
		"start_time":           clock,
		"party_size":           strconv.Itoa(ref.PartySize),
		"shift_persistent_id":  ref.ShiftPersistentID,
		"access_persistent_id": ref.AccessPersistentID,
		"reservation_hold_id":  hold.ID,
		// The widget always sends this, empty when nothing was upsold.
		"selected_upsells": "[]",

		// Guest identity.
		"first_name":   guest.FirstName,
		"last_name":    guest.LastName,
		"email":        guest.Email,
		"phone_number": guest.PhoneNumber,
		"dial_code":    guest.DialCode,
		"country_code": guest.CountryCode,
	}
	if venue != nil && venue.ID != "" {
		fields["venue_id"] = venue.ID
		// The widget sends both; a venue whose booking page fronts several
		// venues distinguishes them this way.
		fields["widget_primary_venue_id"] = venue.ID
	}
	if cardToken != "" {
		fields["card_token"] = cardToken
	}

	path := fmt.Sprintf(bookPathFmt, ref.Venue)
	if logger != nil {
		logger("POST " + BaseURL + path + " (book " + ref.TimeISO + " party " + strconv.Itoa(ref.PartySize) + ")")
	}
	resp, err := postForm(client, path, fields, map[string]string{
		"X-Checkout-Hash": guest.checkoutHash(),
		"X-Widget-Origin": widgetOrigin,
	})
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, &apiError{Status: resp.StatusCode, Body: bookMessage(resp.StatusCode, raw)}
	}
	return &Reservation{ID: reservationID(raw), Confirmation: confirmationCode(raw), Raw: raw}, nil
}

// IsRateLimited reports whether err is SevenRooms throttling us. The book
// endpoint rate-limits noticeably harder than the read endpoints, and
// answers with an HTML error page rather than JSON, so callers that
// retry need to recognize it.
func IsRateLimited(err error) bool {
	var ae *apiError
	return errors.As(err, &ae) && ae.Status == http.StatusTooManyRequests
}

// bookMessage pulls the human-readable complaint out of a book failure.
// This endpoint reports problems as {"errors": [...]}, unlike the
// api-yoa endpoints' {"msg": ...}, and falls back to an HTML page when
// it throttles.
func bookMessage(status int, body []byte) string {
	var e struct {
		Errors []string `json:"errors"`
	}
	if json.Unmarshal(body, &e) == nil && len(e.Errors) > 0 {
		return strings.Join(e.Errors, "; ")
	}
	if status == http.StatusTooManyRequests {
		return "rate limited by SevenRooms; wait a few minutes before retrying"
	}
	return message(body)
}

// reservationID digs the reservation's id out of a successful response.
// SevenRooms nests it differently depending on venue configuration, so
// the common shapes are tried in turn and "" is an acceptable answer —
// the booking succeeded either way.
func reservationID(body []byte) string {
	var out struct {
		ID string `json:"id"`
		// The dining widget's book endpoint answers with these rather
		// than a bare "id".
		ReservationID    string `json:"reservation_id"`
		APIReservationID string `json:"api_reservation_id"`
		Payload          struct {
			ID       string `json:"id"`
			ActualID string `json:"actual_id"`
			Actual   struct {
				ID string `json:"id"`
			} `json:"actual"`
		} `json:"payload"`
		Data struct {
			ID       string `json:"id"`
			ActualID string `json:"actual_id"`
		} `json:"data"`
	}
	if json.Unmarshal(body, &out) != nil {
		return ""
	}
	for _, c := range []string{
		out.ID,
		out.ReservationID, out.APIReservationID,
		out.Payload.ID, out.Payload.ActualID, out.Payload.Actual.ID,
		out.Data.ID, out.Data.ActualID,
	} {
		if c != "" {
			return c
		}
	}
	return ""
}

// postForm submits a multipart/form-data request, which is what both
// /booking endpoints expect.
func postForm(client *http.Client, path string, fields, headers map[string]string) (*http.Response, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for k, v := range fields {
		if v == "" {
			continue
		}
		if err := w.WriteField(k, v); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", BaseURL+path, &buf)
	if err != nil {
		return nil, err
	}
	setHeaders(req)
	req.Header.Set("Content-Type", w.FormDataContentType())
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return platform.HTTPClient(client).Do(req)
}

func truncate(b []byte) string {
	s := strings.TrimSpace(string(b))
	if len(s) > 200 {
		return s[:200] + "...(truncated)"
	}
	return s
}

// confirmationCode pulls the venue's short human-facing reference out of
// a booking response. The dining widget puts it in "message", which reads
// like a status field but carries the code.
func confirmationCode(body []byte) string {
	var out struct {
		Message string `json:"message"`
		Status  string `json:"status"`
	}
	if json.Unmarshal(body, &out) != nil {
		return ""
	}
	// Guard against the field genuinely being prose on some other venue's
	// configuration: a confirmation code is short and has no spaces.
	m := strings.TrimSpace(out.Message)
	if m == "" || len(m) > 24 || strings.ContainsAny(m, " \t") {
		return ""
	}
	return m
}
