package book

import (
	"strings"
	"testing"

	"resz/internal/platform"
	"resz/internal/sevenroomsapi"
	"resz/internal/venueconfig"
)

func TestParseBooked(t *testing.T) {
	tests := []struct {
		ref  string
		want booked
	}{
		{"rgs://resy/10001/2000001/2/2026-09-20/2026-09-20/17:30:00/4/Table",
			booked{platform.Resy, "10001", "2026-09-20"}},
		{"sr://venuea/2026-09-26T17:00:00?party_size=2&cc=1",
			booked{platform.SevenRooms, "venuea", "2026-09-26"}},
	}
	for _, tt := range tests {
		got, err := parseBooked(tt.ref)
		if err != nil {
			t.Fatalf("parseBooked(%q): %v", tt.ref, err)
		}
		if got != tt.want {
			t.Errorf("parseBooked(%q) = %+v, want %+v", tt.ref, got, tt.want)
		}
	}
	for _, bad := range []string{"rgs://resy/10001", "rgs://resy/1/2/3/not-a-date/x", "https://resy.com/x"} {
		if _, err := parseBooked(bad); err == nil {
			t.Errorf("parseBooked(%q) accepted a malformed reference", bad)
		}
	}
}

func testConfig() *venueconfig.Config {
	return &venueconfig.Config{
		Venues: []venueconfig.ConfigEntry{
			{Name: "Venue A", ID: "venuea", Plat: "sevenrooms"},
			{Name: "Venue B", ID: "https://www.sevenrooms.com/explore/venueb/reservations/create/search", Plat: "sevenrooms"},
			{Name: "Venue C", ID: "10001"},
			// Same id, other platform: a different venue.
			{Name: "Venue D", ID: "venuea"},
		},
		BlackoutDates: []string{"2026-10-01"},
	}
}

func names(cfg *venueconfig.Config) string {
	var n []string
	for _, v := range cfg.Venues {
		n = append(n, v.Name)
	}
	return strings.Join(n, ", ")
}

func TestRecordBookingStopsWatchingTheVenue(t *testing.T) {
	cfg := testConfig()
	removed, blackedOut := recordBooking(cfg, booked{platform.SevenRooms, "venuea", "2026-10-25"})
	if strings.Join(removed, ",") != "Venue A" {
		t.Errorf("removed = %v, want only the SevenRooms Venue A", removed)
	}
	if got := names(cfg); got != "Venue B, Venue C, Venue D" {
		t.Errorf("remaining = %s", got)
	}
	if !blackedOut || strings.Join(cfg.BlackoutDates, ",") != "2026-10-01,2026-10-25" {
		t.Errorf("blackout = %v (added %v)", cfg.BlackoutDates, blackedOut)
	}
}

// A venue watched by its booking URL is the same venue as its slug.
func TestRecordBookingMatchesAVenueConfiguredByURL(t *testing.T) {
	cfg := testConfig()
	removed, _ := recordBooking(cfg, booked{platform.SevenRooms, "venueb", "2026-10-31"})
	if strings.Join(removed, ",") != "Venue B" {
		t.Errorf("removed = %v", removed)
	}
}

func TestRecordBookingForResy(t *testing.T) {
	cfg := testConfig()
	removed, _ := recordBooking(cfg, booked{platform.Resy, "10001", "2026-09-20"})
	if strings.Join(removed, ",") != "Venue C" {
		t.Errorf("removed = %v", removed)
	}
}

// A one-off booking at a venue nobody was watching changes nothing,
// including the blackout list.
func TestRecordBookingLeavesAnUnwatchedVenueAlone(t *testing.T) {
	cfg := testConfig()
	removed, blackedOut := recordBooking(cfg, booked{platform.SevenRooms, "elsewhere", "2026-10-25"})
	if len(removed) != 0 || blackedOut || len(cfg.Venues) != 4 || len(cfg.BlackoutDates) != 1 {
		t.Errorf("config changed: removed=%v blackedOut=%v venues=%s dates=%v",
			removed, blackedOut, names(cfg), cfg.BlackoutDates)
	}
}

func TestSummaryKeepsEveryReference(t *testing.T) {
	ref := "sr://venueb/2026-10-31T17:30:00?party_size=2"
	b := booked{platform.SevenRooms, "venueb", "2026-10-31"}
	res := &sevenroomsapi.Reservation{
		ID: "resid12345", Confirmation: "ABCD1234EFG", PaymentID: "pi_123",
		Charge: sevenroomsapi.Charge{Base: 120, Tax: 10.65, DueNow: 130.65},
	}
	got := summary(ref, b, res, nil)
	for _, want := range []string{"venueb", "2026-10-31", "confirmation ABCD1234EFG", "reservation id resid12345",
		"charged $130.65 ($120.00 + $10.65 tax) (Stripe pi_123)", ref} {
		if !strings.Contains(got, want) {
			t.Errorf("summary is missing %q: %s", want, got)
		}
	}

	resy := summary("rgs://resy/10002/1/2/2026-10-25/2026-10-25/19:00:00/2/Table",
		booked{platform.Resy, "10002", "2026-10-25"}, nil,
		[]byte(`{"resy_token":"rt_abc","reservation_id":123456789}`))
	for _, want := range []string{"reservation id 123456789", "resy_token rt_abc"} {
		if !strings.Contains(resy, want) {
			t.Errorf("Resy summary is missing %q: %s", want, resy)
		}
	}
}
