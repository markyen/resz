package book

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"resz/internal/platform"
	"resz/internal/sevenroomsapi"
	"resz/internal/venueconfig"
)

// booked identifies what a booked slot reference was for.
type booked struct {
	platform platform.Platform
	venue    string // Resy venue id, or SevenRooms slug
	date     string // YYYY-MM-DD
}

// parseBooked reads the venue and date out of an rgs:// or sr:// slot
// reference.
//
// A Resy config id is rgs://resy/<venue>/<template>/<n>/<date>/<date>/
// <time>/<party>/<label>; the first date is the reservation's.
func parseBooked(ref string) (booked, error) {
	if sevenroomsapi.IsRef(ref) {
		r, err := sevenroomsapi.ParseRef(ref)
		if err != nil {
			return booked{}, err
		}
		return booked{platform.SevenRooms, r.Venue, r.Date()}, nil
	}
	rest, ok := strings.CutPrefix(ref, "rgs://resy/")
	if !ok {
		return booked{}, fmt.Errorf("not a slot reference: %q", ref)
	}
	parts := strings.Split(rest, "/")
	if len(parts) < 4 || parts[0] == "" {
		return booked{}, fmt.Errorf("malformed Resy config id %q", ref)
	}
	if _, err := time.Parse(platform.DateLayout, parts[3]); err != nil {
		return booked{}, fmt.Errorf("malformed date in Resy config id %q", ref)
	}
	return booked{platform.Resy, parts[0], parts[3]}, nil
}

// recordBooking does the bookkeeping check and snipe do after their own
// bookings: block the booked date from further attempts and stop watching
// the venue. Without it, a venue booked by hand after an approval stays
// in the config, and check goes on asking about its other slots.
//
// It reports the names of the entries removed and whether the date was
// newly blacked out; both may be empty when the venue wasn't watched.
func recordBooking(cfg *venueconfig.Config, b booked) (removed []string, blackedOut bool) {
	out := cfg.Venues[:0]
	for _, v := range cfg.Venues {
		if watches(v, b) {
			removed = append(removed, v.Name)
			continue
		}
		out = append(out, v)
	}
	cfg.Venues = out
	// Blacking out a date is a statement about the whole config ("we have
	// plans that night"), so it only follows a booking resz was watching
	// for; an unrelated one-off booking leaves the config alone.
	if len(removed) > 0 {
		blackedOut = cfg.AddBlackout(b.date)
	}
	return removed, blackedOut
}

// watches reports whether config entry v is the venue b was booked at.
func watches(v venueconfig.ConfigEntry, b booked) bool {
	p, err := v.Platform()
	if err != nil || p != b.platform {
		return false
	}
	if p == platform.SevenRooms {
		return strings.EqualFold(sevenroomsapi.SlugFromURL(v.ID), b.venue)
	}
	return v.ID == b.venue
}

// summary is a one-line account of a successful booking, for the log and
// the terminal: what, when, and every reference worth keeping. resyCharge
// is what a Resy booking took at booking, from its details.
func summary(ref string, b booked, res *sevenroomsapi.Reservation, resyBody []byte, resyCharge float64) string {
	var parts []string
	if res != nil {
		if res.Confirmation != "" {
			parts = append(parts, "confirmation "+res.Confirmation)
		}
		if res.ID != "" {
			parts = append(parts, "reservation id "+res.ID)
		}
		switch c := res.Charge; {
		case c.DueNow > 0:
			parts = append(parts, "charged "+c.Breakdown()+" (Stripe "+res.PaymentID+")")
		case c.PreAuthorization > 0:
			parts = append(parts, fmt.Sprintf("$%.2f pre-authorized for a no-show (Stripe %s)", c.PreAuthorization, res.PaymentID))
		}
	}
	if resyBody != nil {
		var r struct {
			ReservationID json.Number `json:"reservation_id"`
			ResyToken     string      `json:"resy_token"`
		}
		if json.Unmarshal(resyBody, &r) == nil {
			if r.ReservationID != "" {
				parts = append(parts, "reservation id "+r.ReservationID.String())
			}
			if r.ResyToken != "" {
				parts = append(parts, "resy_token "+r.ResyToken)
			}
		}
		if resyCharge > 0 {
			parts = append(parts, fmt.Sprintf("charged $%.2f at booking through Resy", resyCharge))
		}
	}
	s := fmt.Sprintf("booked %s %s on %s", b.platform, b.venue, b.date)
	if len(parts) > 0 {
		s += ": " + strings.Join(parts, ", ")
	}
	return s + " [" + ref + "]"
}
