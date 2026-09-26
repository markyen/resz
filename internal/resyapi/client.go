package resyapi

import (
	"net/http"

	"resz/internal/platform"
)

// Client is a platform.VenueClient for one Resy venue. It holds the
// venue's geo metadata, which /4/find requires on every call, so callers
// fetch it once per venue instead of once per date.
type Client struct {
	http     *http.Client
	id       string
	venue    *Venue
	timezone string
}

var _ platform.VenueClient = (*Client)(nil)

// Open fetches the venue's metadata and returns a client for it. Pass nil
// for client to use http.DefaultClient. citySlug, when it names a known
// city, supplies the venue's timezone — /3/venue doesn't report one.
func Open(client *http.Client, venueID, citySlug string) (*Client, error) {
	v, err := GetVenue(client, venueID)
	if err != nil {
		return nil, err
	}
	c := &Client{http: client, id: venueID, venue: v}
	if city, ok := LookupCity(citySlug); ok {
		c.timezone = city.Timezone
	}
	return c, nil
}

// NewClient wraps venue metadata the caller already has. `resz snipe`
// uses it to avoid refetching during its latency-sensitive prep phase.
func NewClient(client *http.Client, venueID, citySlug string, venue *Venue) *Client {
	c := &Client{http: client, id: venueID, venue: venue}
	if city, ok := LookupCity(citySlug); ok {
		c.timezone = city.Timezone
	}
	return c
}

func (c *Client) Platform() platform.Platform { return platform.Resy }
func (c *Client) ID() string                  { return c.id }
func (c *Client) Timezone() string            { return c.timezone }

// Name returns "" — Resy's /3/venue response is only parsed for the geo
// fields /4/find needs, and the venue name is already in the config.
func (c *Client) Name() string { return "" }

// Venue exposes the fetched geo metadata.
func (c *Client) Venue() *Venue { return c.venue }

// AvailableDates returns the dates the venue's calendar reports as having
// inventory for partySize, over the next year.
func (c *Client) AvailableDates(partySize int, logger func(string)) ([]string, error) {
	return AvailableDates(c.http, c.id, partySize, logger)
}

// SlotsOn returns every slot /4/find offers on date.
func (c *Client) SlotsOn(date string, partySize int, logger func(string)) ([]platform.Slot, error) {
	return Slots(c.http, c.id, date, partySize, c.venue, logger)
}
