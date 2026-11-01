package venueconfig

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBookingWindow(t *testing.T) {
	for _, tc := range []struct {
		date string
		days int
		want string
	}{
		{"2026-11-12", 1, "2026-11-12"},
		{"2026-11-12", 2, "2026-11-12,2026-11-13"},
		{"2026-11-12", 3, "2026-11-11,2026-11-12,2026-11-13"},
		{"2026-11-12", 4, "2026-11-11,2026-11-12,2026-11-13,2026-11-14"},
		{"2026-11-12", 5, "2026-11-10,2026-11-11,2026-11-12,2026-11-13,2026-11-14"},
		{"2026-11-12", 6, "2026-11-10,2026-11-11,2026-11-12,2026-11-13,2026-11-14,2026-11-15"},
		// Month, year and leap-day edges.
		{"2026-12-31", 4, "2026-12-30,2026-12-31,2027-01-01,2027-01-02"},
		{"2028-02-28", 3, "2028-02-27,2028-02-28,2028-02-29"},
		{"2028-03-01", 3, "2028-02-29,2028-03-01,2028-03-02"},
		// Across the daylight-saving change in New York (1 Nov 2026).
		{"2026-11-01", 3, "2026-10-31,2026-11-01,2026-11-02"},
	} {
		got, err := BookingWindow(tc.date, tc.days)
		if err != nil || strings.Join(got, ",") != tc.want {
			t.Errorf("BookingWindow(%s, %d) = %v, %v; want %s", tc.date, tc.days, got, err, tc.want)
		}
	}
	for _, days := range []int{0, -1, 32} {
		if _, err := BookingWindow("2026-11-12", days); err == nil {
			t.Errorf("BookingWindow(%d days) did not refuse", days)
		}
	}
	if _, err := BookingWindow("12/11/2026", 1); err == nil {
		t.Error("BookingWindow accepted a date not in YYYY-MM-DD form")
	}
}

func TestBookingBlackoutDaysDefault(t *testing.T) {
	for _, n := range []int{0, -3} {
		if got := (&Config{BookingBlackout: n}).BookingBlackoutDays(); got != 1 {
			t.Errorf("BookingBlackoutDays with %d = %d; want 1", n, got)
		}
	}
	if got := (&Config{BookingBlackout: 7}).BookingBlackoutDays(); got != 7 {
		t.Errorf("BookingBlackoutDays with 7 = %d", got)
	}
}

func TestAddBookingBlackout(t *testing.T) {
	// Absent (0): the booked date only.
	c := &Config{BlackoutDates: []string{"2026-10-01"}}
	added, err := c.AddBookingBlackout("2026-11-12")
	if err != nil || strings.Join(added, ",") != "2026-11-12" ||
		strings.Join(c.BlackoutDates, ",") != "2026-10-01,2026-11-12" {
		t.Errorf("default: added %v, %v; dates %v", added, err, c.BlackoutDates)
	}
	// A window over dates already blacked out adds, and returns, only the new ones.
	c = &Config{BookingBlackout: 5, BlackoutDates: []string{"2026-11-11", "2026-11-13"}}
	added, err = c.AddBookingBlackout("2026-11-12")
	if err != nil || strings.Join(added, ",") != "2026-11-10,2026-11-12,2026-11-14" {
		t.Errorf("overlap: added %v, %v", added, err)
	}
	if strings.Join(c.BlackoutDates, ",") != "2026-11-10,2026-11-11,2026-11-12,2026-11-13,2026-11-14" {
		t.Errorf("overlap: dates %v", c.BlackoutDates)
	}
	if added, _ := c.AddBookingBlackout("2026-11-12"); len(added) != 0 {
		t.Errorf("a second identical booking added %v", added)
	}
}

func TestDateSpan(t *testing.T) {
	for _, tc := range []struct {
		in   []string
		want string
	}{
		{nil, ""},
		{[]string{"2026-11-12"}, "2026-11-12"},
		{[]string{"2026-11-12", "2026-11-13", "2026-11-14", "2026-11-15"}, "2026-11-12..2026-11-15"},
		{[]string{"2026-11-10", "2026-11-12", "2026-11-14"}, "2026-11-10, 2026-11-12, 2026-11-14"},
	} {
		if got := DateSpan(tc.in); got != tc.want {
			t.Errorf("DateSpan(%v) = %q; want %q", tc.in, got, tc.want)
		}
	}
}

func write(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "venues.json")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestLoadValidatesBookingBlackoutDays(t *testing.T) {
	for _, n := range []string{"-1", "32"} {
		_, err := Load(write(t, `{"venues": [], "booking_blackout_days": `+n+`}`))
		if err == nil || !strings.Contains(err.Error(), "booking_blackout_days") || !strings.Contains(err.Error(), "1 to 31") {
			t.Errorf("Load with %s: %v; want an error naming the field and the range", n, err)
		}
	}
	for _, n := range []string{"0", "1", "7", "31"} {
		if _, err := Load(write(t, `{"venues": [], "booking_blackout_days": `+n+`}`)); err != nil {
			t.Errorf("Load with %s: %v", n, err)
		}
	}
}

func TestSaveRoundTrip(t *testing.T) {
	// A file that never sets the field is saved without it, byte for byte.
	body := "{\n  \"venues\": [\n    {\n      \"name\": \"A\",\n      \"id\": \"1\"\n    }\n  ],\n  \"blackout_dates\": [\n    \"2026-11-12\"\n  ]\n}\n"
	p := write(t, body)
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(p); string(got) != body {
		t.Errorf("round trip changed the file:\n%s", got)
	}
	// One that sets it keeps it.
	c.BookingBlackout = 7
	if err := Save(p, c); err != nil {
		t.Fatal(err)
	}
	if c2, err := Load(p); err != nil || c2.BookingBlackout != 7 {
		t.Errorf("saved 7, loaded %v, %v", c2, err)
	}
}
