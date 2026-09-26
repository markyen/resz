package platform

import (
	"testing"
	"time"
)

func mustSlot(t *testing.T, stamp string) Slot {
	t.Helper()
	start, err := time.Parse("2006-01-02 15:04:05", stamp)
	if err != nil {
		t.Fatalf("bad test timestamp %q: %v", stamp, err)
	}
	return Slot{Platform: Resy, Start: start, Ref: stamp}
}

func TestParse(t *testing.T) {
	for _, in := range []string{"resy", "RESY", " resy "} {
		if got, err := Parse(in); err != nil || got != Resy {
			t.Errorf("Parse(%q) = %q, %v; want resy", in, got, err)
		}
	}
	// The spellings a venue's own booking URL might use.
	for _, in := range []string{"sevenrooms", "SevenRooms", "seven-rooms", "sr"} {
		if got, err := Parse(in); err != nil || got != SevenRooms {
			t.Errorf("Parse(%q) = %q, %v; want sevenrooms", in, got, err)
		}
	}
	if _, err := Parse("opentable"); err == nil {
		t.Error("Parse(opentable) succeeded; want an error")
	}
}

func TestWindowResolveDefaults(t *testing.T) {
	// 2026-08-15 is a Saturday, 2026-08-17 a Monday.
	tests := []struct {
		date, earliest, latest string
	}{
		{"2026-08-15", "11:30", "20:00"}, // weekend
		{"2026-08-16", "11:30", "20:00"}, // weekend
		{"2026-08-17", "18:30", "20:00"}, // weekday
	}
	for _, tt := range tests {
		e, l, err := (*Window)(nil).Resolve(tt.date)
		if err != nil {
			t.Fatalf("Resolve(%s): %v", tt.date, err)
		}
		if got := e.Format("15:04"); got != tt.earliest {
			t.Errorf("Resolve(%s) earliest = %s; want %s", tt.date, got, tt.earliest)
		}
		if got := l.Format("15:04"); got != tt.latest {
			t.Errorf("Resolve(%s) latest = %s; want %s", tt.date, got, tt.latest)
		}
	}
}

func TestWindowResolveExplicitAndInvalid(t *testing.T) {
	w := &Window{Earliest: "17:00", Latest: "22:30"}
	// An explicit window applies on a weekend too, overriding the default.
	e, l, err := w.Resolve("2026-08-15")
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if e.Format("15:04") != "17:00" || l.Format("15:04") != "22:30" {
		t.Errorf("Resolve = %s-%s; want 17:00-22:30", e.Format("15:04"), l.Format("15:04"))
	}
	// A malformed bound must be reported, not silently treated as a
	// window that matches nothing.
	if _, _, err := (&Window{Earliest: "7pm", Latest: "20:00"}).Resolve("2026-08-17"); err == nil {
		t.Error("Resolve with bad start succeeded; want an error")
	}
	if _, _, err := (&Window{Earliest: "18:00", Latest: ""}).Resolve("2026-08-17"); err == nil {
		t.Error("Resolve with empty end succeeded; want an error")
	}
}

func TestFilterWindowIsInclusive(t *testing.T) {
	date := "2026-08-17" // Monday: default window 18:30-20:00
	slots := []Slot{
		mustSlot(t, date+" 18:15:00"), // before
		mustSlot(t, date+" 18:30:00"), // exactly at the lower bound
		mustSlot(t, date+" 19:00:00"),
		mustSlot(t, date+" 20:00:00"), // exactly at the upper bound
		mustSlot(t, date+" 20:15:00"), // after
	}
	got, err := FilterWindow(slots, date, nil)
	if err != nil {
		t.Fatalf("FilterWindow: %v", err)
	}
	want := []string{"18:30", "19:00", "20:00"}
	if len(got) != len(want) {
		t.Fatalf("FilterWindow returned %d slots; want %d", len(got), len(want))
	}
	for i, w := range want {
		if s := got[i].Start.Format("15:04"); s != w {
			t.Errorf("slot %d = %s; want %s", i, s, w)
		}
	}
}

func TestBestSlotIndex(t *testing.T) {
	date := "2026-08-17"
	slots := []Slot{
		mustSlot(t, date+" 18:30:00"),
		mustSlot(t, date+" 19:15:00"), // closest to 19:00
		mustSlot(t, date+" 20:00:00"),
	}
	if got := BestSlotIndex(slots, "19:00"); got != 1 {
		t.Errorf("BestSlotIndex = %d; want 1", got)
	}
	if got := BestSlotIndex(nil, "19:00"); got != -1 {
		t.Errorf("BestSlotIndex(nil) = %d; want -1", got)
	}
}

func TestSortByPreferenceKeepsEverySlot(t *testing.T) {
	date := "2026-08-17"
	slots := []Slot{
		mustSlot(t, date+" 20:00:00"),
		mustSlot(t, date+" 19:00:00"),
		mustSlot(t, date+" 18:30:00"),
	}
	SortByPreference(slots, "19:00")
	want := []string{"19:00", "18:30", "20:00"}
	if len(slots) != len(want) {
		t.Fatalf("SortByPreference changed the slice length to %d; want %d", len(slots), len(want))
	}
	for i, w := range want {
		if s := slots[i].Start.Format("15:04"); s != w {
			t.Errorf("slot %d = %s; want %s", i, s, w)
		}
	}
}

func TestSlotDateAndLabel(t *testing.T) {
	s := mustSlot(t, "2026-08-15 19:45:00")
	if got := s.Date(); got != "2026-08-15" {
		t.Errorf("Date = %q; want 2026-08-15", got)
	}
	if got := s.StartLabel(); got != "Sat, Aug 15 at 7:45 PM" {
		t.Errorf("StartLabel = %q; want %q", got, "Sat, Aug 15 at 7:45 PM")
	}
}
