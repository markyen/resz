package sevenroomsapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strings"
)

const venueInfoPath = "/api-yoa/dining/venue_info"

// Venue is the subset of /api-yoa/dining/venue_info resz uses.
type Venue struct {
	// Slug is the venue's URL key, e.g. "blueheron".
	Slug string
	// ID is the venue's opaque internal identifier. The booking form
	// carries it alongside the slug.
	ID string
	// Name is the display name. SevenRooms lets operators type these in
	// freely, so it can carry stray whitespace; Name is already trimmed.
	Name     string
	City     string
	State    string
	Timezone string
	// HorizonDays is how far ahead the venue publishes availability
	// (SevenRooms calls it availability_cache_days). Dates beyond it are
	// never bookable, so it bounds how far resz scans.
	HorizonDays int

	// PaymentSystem names the gateway the venue bills through ("STRIPE").
	// PaymentPublicToken is that gateway's publishable key and
	// PaymentAccountID the connected account the charge lands in. All
	// three are published by the widget and are only useful for creating
	// a single-use token; none of them is a secret.
	PaymentSystem      string
	PaymentPublicToken string
	PaymentAccountID   string
	CurrencyCode       string
}

// TakesPayment reports whether the venue is set up to charge a card at
// all. A venue without a gateway cannot be paid by resz, whatever an
// individual slot claims.
func (v Venue) TakesPayment() bool {
	return v.PaymentSystem != "" && v.PaymentPublicToken != ""
}

// venueInfoResponse mirrors the parts of the venue_info payload we read.
// Per-venue booking settings live under venue_map, keyed by URL slug,
// while identity and locale live at the top level.
type venueInfoResponse struct {
	VenueInfo struct {
		ID       string `json:"id"`
		Name     string `json:"name"`
		URLKey   string `json:"url_key"`
		City     string `json:"city"`
		State    string `json:"state"`
		Timezone string `json:"timezone"`
		VenueMap map[string]struct {
			AvailabilityCacheDays int    `json:"availability_cache_days"`
			PaymentSystem         string `json:"payment_system"`
			PaymentPublicToken    string `json:"payment_public_token"`
			PaymentAccountID      string `json:"payment_account_id"`
			CurrencyCode          string `json:"currency_code"`
		} `json:"venue_map"`
	} `json:"venue_info"`
}

// GetVenue fetches venue metadata for a slug. An unknown slug yields an
// error for which IsUnknownVenue reports true.
func GetVenue(client *http.Client, slug string, logger func(string)) (*Venue, error) {
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return nil, fmt.Errorf("empty venue slug")
	}
	q := url.Values{}
	q.Set("venue_url_key", slug)

	var out venueInfoResponse
	if err := getJSON(client, venueInfoPath, q, &out, logger); err != nil {
		return nil, err
	}
	vi := out.VenueInfo
	if vi.URLKey == "" {
		return nil, fmt.Errorf("venue %s: no url_key in venue_info response", slug)
	}
	v := &Venue{
		Slug:     vi.URLKey,
		ID:       vi.ID,
		Name:     strings.TrimSpace(vi.Name),
		City:     vi.City,
		State:    vi.State,
		Timezone: vi.Timezone,
	}
	if m, ok := vi.VenueMap[vi.URLKey]; ok {
		v.HorizonDays = m.AvailabilityCacheDays
		v.PaymentSystem = m.PaymentSystem
		v.PaymentPublicToken = m.PaymentPublicToken
		v.PaymentAccountID = m.PaymentAccountID
		v.CurrencyCode = m.CurrencyCode
	}
	return v, nil
}

// IsUnknownVenue reports whether err means "no SevenRooms venue with that
// slug", as opposed to a transport or server failure.
func IsUnknownVenue(err error) bool { return errNotFound(err) }

// SlugFromURL extracts a venue slug from a SevenRooms booking URL or
// returns the input unchanged if it isn't a URL. It handles both widget
// shapes:
//
//	https://www.sevenrooms.com/explore/blueheron/reservations/create/search/
//	https://fp.sevenrooms.com/reservations/theoldmill
//
// so a slug can be pasted straight out of the browser.
func SlugFromURL(s string) string {
	s = strings.TrimSpace(s)
	if !strings.Contains(s, "/") {
		return s
	}
	u, err := url.Parse(s)
	if err != nil {
		return s
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, p := range parts {
		if (p == "explore" || p == "reservations" || p == "direct") && i+1 < len(parts) {
			return parts[i+1]
		}
	}
	// No recognizable marker: fall back to the last non-empty segment.
	for i := len(parts) - 1; i >= 0; i-- {
		if parts[i] != "" {
			return parts[i]
		}
	}
	return s
}
