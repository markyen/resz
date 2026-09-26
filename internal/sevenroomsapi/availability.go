package sevenroomsapi

import (
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"resz/internal/platform"
)

const (
	datesPath = "/api-yoa/availability/dates"
	rangePath = "/api-yoa/availability/widget/range"

	// rangeNumDays is how many consecutive days one widget/range call
	// covers. The endpoint validates this parameter strictly and accepts
	// only 1 or 3, so 3 is the cheapest way to walk a calendar.
	rangeNumDays = 3

	// defaultHorizonDays bounds the date scan when venue_info doesn't
	// report the venue's own availability_cache_days. SevenRooms venues
	// commonly publish 30-90 days out.
	defaultHorizonDays = 90

	// maxHorizonDays is a hard ceiling on the scan, so a venue that
	// advertises an implausible horizon can't turn one availability check
	// into hundreds of requests.
	maxHorizonDays = 180

	// slotDateLayout is how widget/range formats a slot's local start.
	slotDateLayout = "2006-01-02 15:04:05"

	// rangeDateLayout is the MM-DD-YYYY form widget/range wants for
	// start_date — note it differs from every other date in this package.
	rangeDateLayout = "01-02-2006"
)

// OpenDates returns the dates in [today, today+horizon] on which the
// venue accepts bookings for partySize.
//
// This is weaker than Resy's calendar: SevenRooms reports the dates a
// venue is *open and bookable*, not the dates that still have inventory.
// A fully-booked venue still lists every open date here, so callers must
// confirm each date with Slots.
func OpenDates(client *http.Client, slug string, partySize, horizonDays int, logger func(string)) ([]string, error) {
	if horizonDays <= 0 {
		horizonDays = defaultHorizonDays
	}
	if horizonDays > maxHorizonDays {
		horizonDays = maxHorizonDays
	}
	today := time.Now()
	q := url.Values{}
	q.Set("venue", slug)
	q.Set("party_size", strconv.Itoa(partySize))
	q.Set("start_date", today.Format(rangeDateLayout))
	q.Set("num_days", strconv.Itoa(horizonDays))

	var out struct {
		ValidDates []string `json:"valid_dates"`
	}
	if err := getJSON(client, datesPath, q, &out, logger); err != nil {
		return nil, err
	}
	return out.ValidDates, nil
}

// rangeResponse is the widget/range payload: shifts (services) per date,
// each holding the individual times.
type rangeResponse struct {
	Availability map[string][]struct {
		Name  string `json:"name"`
		Times []struct {
			// Type is "book" for a slot that can be reserved right now,
			// or "request" for one the venue will only accept a
			// reservation *request* against. Only "book" is real
			// availability.
			Type    string `json:"type"`
			TimeISO string `json:"time_iso"`
			// AccessPersistentID identifies the access rule the slot came
			// from; booking needs it, and it distinguishes two slots at
			// the same time in different seating areas.
			AccessPersistentID string `json:"access_persistent_id"`
			ShiftPersistentID  string `json:"shift_persistent_id"`
			Duration           int    `json:"duration"`
			// SeatingArea is the widget's public label for the slot's
			// seating area. Often empty.
			SeatingArea string `json:"public_time_slot_description"`
			// RequireCreditCard is already evaluated for the party size
			// the request asked about, so it needs no further
			// interpretation. Cost and ChargeType catch prepaid or
			// deposit-bearing slots that don't set it.
			RequireCreditCard bool     `json:"require_credit_card"`
			Cost              *float64 `json:"cost"`
			ChargeType        *string  `json:"charge_type"`
		} `json:"times"`
	} `json:"availability"`
}

// Slots returns every bookable slot the venue has on each of up to three
// consecutive days starting at startDate (YYYY-MM-DD), keyed by date.
//
// Days the venue is closed are absent from the result. Request-only
// times — the "notify me" slots a fully-booked venue shows — are dropped,
// because they are not reservations.
func Slots(client *http.Client, slug, startDate string, partySize int, logger func(string)) (map[string][]platform.Slot, error) {
	start, err := time.Parse(platform.DateLayout, startDate)
	if err != nil {
		return nil, fmt.Errorf("invalid start date %q: want YYYY-MM-DD", startDate)
	}
	q := url.Values{}
	q.Set("venue", slug)
	q.Set("party_size", strconv.Itoa(partySize))
	q.Set("start_date", start.Format(rangeDateLayout))
	q.Set("num_days", strconv.Itoa(rangeNumDays))
	q.Set("channel", Channel)
	// time_slot and halo_size_interval are required by the endpoint but
	// don't restrict the response: it returns every shift for the day
	// regardless. They only steer which time the widget pre-selects.
	q.Set("time_slot", "19:00")
	q.Set("halo_size_interval", "100")

	var out rangeResponse
	if err := getJSON(client, rangePath, q, &out, logger); err != nil {
		return nil, err
	}

	byDate := make(map[string][]platform.Slot, len(out.Availability))
	for date, shifts := range out.Availability {
		// Ensure every date the API reported has an entry even if all its
		// times were request-only, so callers can cache "nothing here"
		// rather than re-fetching.
		if _, ok := byDate[date]; !ok {
			byDate[date] = nil
		}
		for _, sh := range shifts {
			for _, t := range sh.Times {
				if t.Type != "book" {
					continue
				}
				startAt, err := time.Parse(slotDateLayout, t.TimeISO)
				if err != nil {
					continue
				}
				byDate[date] = append(byDate[date], platform.Slot{
					Platform: platform.SevenRooms,
					Start:    startAt,
					Ref: Ref{
						Venue:              slug,
						TimeISO:            t.TimeISO,
						PartySize:          partySize,
						AccessPersistentID: t.AccessPersistentID,
						ShiftPersistentID:  t.ShiftPersistentID,
						Duration:           t.Duration,
						RequiresPayment:    t.RequireCreditCard || t.ChargeType != nil || t.Cost != nil,
						Cost:               derefFloat(t.Cost),
						ChargeType:         derefString(t.ChargeType),
					}.String(),
					Label: label(sh.Name, t.SeatingArea),
				})
			}
		}
	}
	return byDate, nil
}

// label combines the shift name ("Dinner") and the seating-area
// description into one short slot label.
func label(shift, seatingArea string) string {
	shift, seatingArea = strings.TrimSpace(shift), strings.TrimSpace(seatingArea)
	switch {
	case shift != "" && seatingArea != "":
		return shift + " / " + seatingArea
	case seatingArea != "":
		return seatingArea
	default:
		return shift
	}
}

// derefFloat and derefString read the optional pricing fields, which are
// absent on the many slots that carry no charge at all.
func derefFloat(p *float64) float64 {
	if p == nil {
		return 0
	}
	return *p
}

func derefString(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
