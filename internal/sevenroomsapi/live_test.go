package sevenroomsapi

import (
	"os"
	"testing"

	"resz/internal/platform"
)

// liveVenue is a venue that reliably publishes near-term availability,
// used by the opt-in live tests below.
const liveVenue = "examplevenue"

// requireLive skips unless RESZ_LIVE_SEVENROOMS=1. These tests talk to
// the real SevenRooms API, so they are off by default: they are slow,
// they depend on a third party being up, and CreateHold briefly claims a
// real table at a real restaurant.
func requireLive(t *testing.T) {
	t.Helper()
	if os.Getenv("RESZ_LIVE_SEVENROOMS") != "1" {
		t.Skip("set RESZ_LIVE_SEVENROOMS=1 to run live API tests")
	}
}

func TestLiveVenueInfo(t *testing.T) {
	requireLive(t)
	v, err := GetVenue(nil, liveVenue, nil)
	if err != nil {
		t.Fatalf("GetVenue: %v", err)
	}
	if v.Slug != liveVenue || v.Name == "" || v.Timezone == "" || v.ID == "" {
		t.Errorf("incomplete venue metadata: %+v", v)
	}
	if v.HorizonDays <= 0 {
		t.Errorf("HorizonDays = %d; want a positive booking horizon", v.HorizonDays)
	}
}

func TestLiveUnknownVenueIs404(t *testing.T) {
	requireLive(t)
	// Search relies on being able to tell "wrong guess" apart from a real
	// failure, so this distinction has to keep holding.
	_, err := GetVenue(nil, "definitely-not-a-real-venue-slug-xyz", nil)
	if err == nil {
		t.Fatal("GetVenue on a bogus slug succeeded")
	}
	if !IsUnknownVenue(err) {
		t.Errorf("IsUnknownVenue = false for %v; want true", err)
	}
}

// TestLiveHoldAndRelease exercises the two halves of the booking flow
// that can be run without actually reserving a table: it claims the first
// bookable slot it finds, then immediately gives it back.
//
// It deliberately stops short of Book. Completing a booking would create
// a real obligation at a real restaurant, which no test should do.
func TestLiveHoldAndRelease(t *testing.T) {
	requireLive(t)

	c, err := Open(nil, liveVenue, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	dates, err := c.AvailableDates(2, nil)
	if err != nil {
		t.Fatalf("AvailableDates: %v", err)
	}

	var found platform.Slot
	for _, d := range dates {
		slots, err := c.SlotsOn(d, 2, nil)
		if err != nil {
			t.Fatalf("SlotsOn(%s): %v", d, err)
		}
		if len(slots) > 0 {
			// Take the last slot of the day: least likely to collide with
			// a real diner browsing at the same moment.
			found = slots[len(slots)-1]
			break
		}
	}
	if found.Ref == "" {
		t.Skip("no bookable slots at the live venue right now")
	}

	ref, err := ParseRef(found.Ref)
	if err != nil {
		t.Fatalf("ParseRef(%s): %v", found.Ref, err)
	}
	if ref.RequiresPayment {
		t.Skip("the only slot found needs a credit card; nothing to hold")
	}

	hold, err := CreateHold(nil, ref, nil)
	if err != nil {
		t.Fatalf("CreateHold(%s): %v", found.StartLabel(), err)
	}
	// Release before asserting, so a failed assertion can't strand a hold
	// on a real table.
	relErr := ReleaseHold(nil, ref, hold, nil)

	if hold.ID == "" {
		t.Error("hold carried no id")
	}
	if hold.DurationSec <= 0 {
		t.Errorf("hold duration = %ds; want a positive window", hold.DurationSec)
	}
	if hold.ShiftPersistentID != ref.ShiftPersistentID {
		t.Errorf("hold shift = %q; want %q", hold.ShiftPersistentID, ref.ShiftPersistentID)
	}
	if relErr != nil {
		t.Errorf("ReleaseHold: %v", relErr)
	}
}
