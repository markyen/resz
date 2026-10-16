package resyapi

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
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
	Payment struct {
		Amounts struct {
			ReservationCharge amount `json:"reservation_charge"`
			Total             amount `json:"total"`
		} `json:"amounts"`
	} `json:"payment"`
	Cancellation struct {
		// Fee is only reported, never acted on, so it is read leniently:
		// a shape resz doesn't expect must not block the booking.
		Fee json.RawMessage `json:"fee"`
	} `json:"cancellation"`
}

// Detail is what /3/details says about a slot: the one-shot book token,
// and what booking it costs.
type Detail struct {
	BookToken string
	// Charge is what Resy takes from the card when the slot is booked —
	// a deposit or a prepayment — and is zero for most slots. It is the
	// figure Resy's own checkout weighs: payment.amounts.total, or the
	// reservation charge when that is larger.
	Charge float64
	// CancellationFee is billed only on a late cancellation or a no-show,
	// so it moves no money at booking.
	CancellationFee float64
}

// Details fetches a one-shot book_token for a given config_id (rgs:// URI),
// along with what booking the slot would charge. It doesn't refuse a
// charging slot itself; see Detail.Authorize.
//
// Pass nil for client to use http.DefaultClient. Logger is optional.
func Details(client *http.Client, authToken, configID string, logger func(string)) (*Detail, error) {
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
		return nil, err
	}

	if logger != nil {
		logger("POST " + detailsURL + " body=" + string(buf))
	}
	req, err := http.NewRequest("POST", detailsURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	setResyHeaders(req, authToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		apiErr := &APIError{Status: resp.StatusCode, Body: string(body)}
		if apiErr.needsAddOns() {
			return nil, fmt.Errorf("%w: %w", ErrAddOnsRequired, apiErr)
		}
		return nil, apiErr
	}

	var dr detailsResponse
	if err := json.NewDecoder(resp.Body).Decode(&dr); err != nil {
		return nil, err
	}
	if dr.BookToken.Value == "" {
		return nil, fmt.Errorf("no book_token.value in details response")
	}
	d := &Detail{
		BookToken: dr.BookToken.Value,
		Charge:    max(float64(dr.Payment.Amounts.Total), float64(dr.Payment.Amounts.ReservationCharge)),
	}
	var fee struct {
		Amount amount `json:"amount"`
	}
	if json.Unmarshal(dr.Cancellation.Fee, &fee) == nil {
		d.CancellationFee = float64(fee.Amount)
	}
	return d, nil
}

// ErrChargeAtBooking marks a slot that takes money from the card when it
// is booked, beyond what the caller authorized. Unattended callers
// authorize nothing, so for them any deposit or prepayment is refused and
// becomes a person's decision.
var ErrChargeAtBooking = errors.New("slot charges at booking")

// ChargeError is ErrChargeAtBooking with the figures, so a caller can
// quote the charge when it asks for approval.
type ChargeError struct {
	Charge     float64
	Authorized float64
}

func (e *ChargeError) Error() string {
	s := fmt.Sprintf("%v: this slot charges $%.2f at booking", ErrChargeAtBooking, e.Charge)
	if e.Authorized > 0 {
		s += fmt.Sprintf(", above the $%.2f authorized", e.Authorized)
	}
	return s
}

func (e *ChargeError) Is(target error) bool { return target == ErrChargeAtBooking }

// Authorize refuses a slot whose charge at booking exceeds authorizedUSD.
// The comparison is in whole cents, so a figure quoted back exactly as
// the approval request gave it always passes.
func (d *Detail) Authorize(authorizedUSD float64) error {
	if cents(d.Charge) > cents(authorizedUSD) {
		return &ChargeError{Charge: d.Charge, Authorized: authorizedUSD}
	}
	return nil
}

func cents(usd float64) int64 { return int64(math.Round(usd * 100)) }

// amount decodes a money figure that Resy may send as a number, a numeric
// string, or null. Anything else fails the decode rather than reading as
// zero, since zero would let a charge through.
type amount float64

func (a *amount) UnmarshalJSON(b []byte) error {
	s := strings.Trim(string(b), `"`)
	if s == "" || s == "null" {
		*a = 0
		return nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return fmt.Errorf("amount %s: %w", b, err)
	}
	*a = amount(f)
	return nil
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
