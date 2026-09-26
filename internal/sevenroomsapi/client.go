package sevenroomsapi

import (
	"net/http"
	"strconv"
	"time"

	"resz/internal/platform"
)

// Client is a platform.VenueClient for one SevenRooms venue.
//
// It caches the day-range responses it fetches. widget/range returns
// three consecutive days per call, and callers walk dates in ascending
// order, so a sequential scan costs roughly one request per three days
// instead of one per day.
type Client struct {
	http  *http.Client
	venue *Venue
	// slots is keyed by "<partySize>|<date>". A present entry with a nil
	// value means "already fetched, nothing bookable".
	slots map[string][]platform.Slot
}

var _ platform.VenueClient = (*Client)(nil)

// Open fetches the venue's metadata and returns a client for it. Pass nil
// for client to use http.DefaultClient.
func Open(client *http.Client, slug string, logger func(string)) (*Client, error) {
	v, err := GetVenue(client, SlugFromURL(slug), logger)
	if err != nil {
		return nil, err
	}
	return &Client{http: client, venue: v, slots: map[string][]platform.Slot{}}, nil
}

func (c *Client) Platform() platform.Platform { return platform.SevenRooms }
func (c *Client) ID() string                  { return c.venue.Slug }
func (c *Client) Name() string                { return c.venue.Name }
func (c *Client) Timezone() string            { return c.venue.Timezone }

// Venue exposes the fetched metadata, for callers that want the city or
// booking horizon.
func (c *Client) Venue() Venue { return *c.venue }

// AvailableDates returns the dates the venue is open for booking, over
// its published horizon.
//
// Unlike Resy's calendar these are not guaranteed to have inventory —
// SevenRooms reports open dates, not available ones — so every date still
// has to be confirmed with SlotsOn.
func (c *Client) AvailableDates(partySize int, logger func(string)) ([]string, error) {
	return OpenDates(c.http, c.venue.Slug, partySize, c.venue.HorizonDays, logger)
}

// SlotsOn returns the bookable slots on date, fetching a three-day window
// starting at that date and caching all of it.
func (c *Client) SlotsOn(date string, partySize int, logger func(string)) ([]platform.Slot, error) {
	key := slotKey(partySize, date)
	if s, ok := c.slots[key]; ok {
		return s, nil
	}
	byDate, err := Slots(c.http, c.venue.Slug, date, partySize, logger)
	if err != nil {
		return nil, err
	}
	for d, s := range byDate {
		c.slots[slotKey(partySize, d)] = s
	}
	// A date the venue is closed on is missing from the response
	// entirely. Record the miss so a re-ask doesn't refetch the window.
	if _, ok := c.slots[key]; !ok {
		c.slots[key] = nil
	}
	return c.slots[key], nil
}

func slotKey(partySize int, date string) string {
	return strconv.Itoa(partySize) + "|" + date
}

// Location resolves the venue's timezone, falling back to the local zone
// when SevenRooms reports one Go doesn't know.
func (c *Client) Location() *time.Location {
	if c.venue.Timezone != "" {
		if loc, err := time.LoadLocation(c.venue.Timezone); err == nil {
			return loc
		}
	}
	return time.Local
}
