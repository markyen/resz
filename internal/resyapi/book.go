package resyapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

const (
	detailsURL = BaseURL + "/3/details"
	bookURL    = BaseURL + "/3/book"
)

type detailsResponse struct {
	BookToken struct {
		Value string `json:"value"`
	} `json:"book_token"`
}

// Details fetches a one-shot book_token for a given config_id (rgs:// URI).
// Pass nil for client to use http.DefaultClient. Logger is optional.
func Details(client *http.Client, authToken, configID string, logger func(string)) (string, error) {
	if client == nil {
		client = http.DefaultClient
	}
	body := map[string]any{
		"config_id":    configID,
		"struct_items": []any{},
		"commit":       1,
	}
	buf, err := json.Marshal(body)
	if err != nil {
		return "", err
	}

	if logger != nil {
		logger("POST " + detailsURL + " body=" + string(buf))
	}
	req, err := http.NewRequest("POST", detailsURL, bytes.NewReader(buf))
	if err != nil {
		return "", err
	}
	setResyHeaders(req, authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		apiErr := &APIError{Status: resp.StatusCode, Body: string(body)}
		if apiErr.needsAddOns() {
			return "", fmt.Errorf("%w: %w", ErrAddOnsRequired, apiErr)
		}
		return "", apiErr
	}

	var dr detailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return "", err
	}
	if dr.BookToken.Value == "" {
		return "", fmt.Errorf("no book_token.value in details response")
	}
	return dr.BookToken.Value, nil
}

// Book makes the reservation. Pass nil for client to use http.DefaultClient.
func Book(client *http.Client, authToken, bookToken string, paymentMethodID int64, logger func(string)) ([]byte, error) {
	if client == nil {
		client = http.DefaultClient
	}
	pm := fmt.Sprintf(`{"id":%d}`, paymentMethodID)
	form := url.Values{}
	form.Set("book_token", bookToken)
	form.Set("replace", "0")
	form.Set("struct_payment_method", pm)
	form.Set("venue_marketing_opt_in", "0")
	encoded := form.Encode()

	if logger != nil {
		logger("POST " + bookURL + " body=" + encoded)
	}
	req, err := http.NewRequest("POST", bookURL, strings.NewReader(encoded))
	if err != nil {
		return nil, err
	}
	setResyHeaders(req, authToken)
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(resp.Body)
	if resp.StatusCode/100 != 2 {
		return body, &APIError{Status: resp.StatusCode, Body: string(body)}
	}
	return body, nil
}

type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	return fmt.Sprintf("http %d: %s", e.Status, e.Body)
}

// ErrAddOnsRequired marks a slot Resy won't book until the diner picks
// from a required option group. resz sends no selections, so such a
// slot can never be booked unattended.
// The requirement belongs to the slot's template, so every slot sharing
// it (see TemplateID) is equally unbookable.
var ErrAddOnsRequired = errors.New("slot requires add-on selections resz cannot make")

// needsAddOns reports whether /3/details refused the slot for missing
// add-on selections. Resy answers 412 with code "invalid_item_selection"
// ("Must select required add-ons.").
func (e *APIError) needsAddOns() bool {
	var body struct {
		Code string `json:"code"`
	}
	if json.Unmarshal([]byte(e.Body), &body) != nil {
		return false
	}
	return body.Code == "invalid_item_selection"
}

// TemplateID returns the template segment of an rgs:// config id
// (rgs://resy/<venue>/<template>/...), or "" if ref isn't one. Slots of
// one type share a template, and with it any add-on requirement.
func TemplateID(ref string) string {
	rest, ok := strings.CutPrefix(ref, "rgs://")
	if !ok {
		return ""
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 3 {
		return ""
	}
	return parts[2]
}

// IsAuthError returns true if the API error indicates the token is no
// longer accepted (401 or 419).
func IsAuthError(err error) bool {
	var ae *APIError
	if !errors.As(err, &ae) {
		return false
	}
	return ae.Status == 401 || ae.Status == 419
}
