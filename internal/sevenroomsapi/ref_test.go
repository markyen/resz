package sevenroomsapi

import "testing"

func TestRefRoundTrip(t *testing.T) {
	// The persistent ids SevenRooms hands out contain '-' and '.', and
	// are long; they must survive a round trip through the URI intact.
	want := Ref{
		Venue:              "examplevenue",
		TimeISO:            "2026-08-15 19:45:00",
		PartySize:          2,
		AccessPersistentID: "ahNzfnNldmVucm9vbXM-1779808463.101306-0.9704945629635379",
		ShiftPersistentID:  "ahNzfnNldmVucm9vbXM-DINNER-1725471123.54804",
		Duration:           90,
	}
	s := want.String()
	if !IsRef(s) {
		t.Fatalf("IsRef(%q) = false", s)
	}
	got, err := ParseRef(s)
	if err != nil {
		t.Fatalf("ParseRef(%q): %v", s, err)
	}
	if got != want {
		t.Errorf("round trip = %+v; want %+v", got, want)
	}
	if d := got.Date(); d != "2026-08-15" {
		t.Errorf("Date = %q; want 2026-08-15", d)
	}
}

func TestParseRefRejectsBadInput(t *testing.T) {
	tests := []struct{ name, in string }{
		{"resy config id", "rgs://resy/1234"},
		{"no scheme", "examplevenue/2026-08-15T19:45:00"},
		{"no timestamp", "sr://examplevenue"},
		{"no venue", "sr:///2026-08-15T19:45:00"},
		{"unparseable timestamp", "sr://examplevenue/not-a-time"},
		{"date only", "sr://examplevenue/2026-08-15"},
		{"bad party size", "sr://examplevenue/2026-08-15T19:45:00?party_size=two"},
		{"bad duration", "sr://examplevenue/2026-08-15T19:45:00?duration=long"},
	}
	for _, tt := range tests {
		if _, err := ParseRef(tt.in); err == nil {
			t.Errorf("%s: ParseRef(%q) succeeded; want an error", tt.name, tt.in)
		}
	}
}

func TestParseRefToleratesMissingOptionalFields(t *testing.T) {
	got, err := ParseRef("sr://examplevenue/2026-09-01T18:00:00?party_size=4")
	if err != nil {
		t.Fatalf("ParseRef: %v", err)
	}
	if got.Venue != "examplevenue" || got.PartySize != 4 {
		t.Errorf("got %+v; want venue examplevenue, party 4", got)
	}
	if got.Duration != 0 || got.AccessPersistentID != "" {
		t.Errorf("absent fields should stay zero, got %+v", got)
	}
}

func TestSlugFromURL(t *testing.T) {
	tests := []struct{ in, want string }{
		{"https://www.sevenrooms.com/explore/examplevenue/reservations/create/search/", "examplevenue"},
		{"https://fp.sevenrooms.com/explore/examplevenue/reservations/create/search/", "examplevenue"},
		{"https://www.sevenrooms.com/reservations/examplevenue", "examplevenue"},
		{"sevenrooms.com/explore/examplevenue", "examplevenue"},
		// Not a URL: passed through so a bare slug still works.
		{"examplevenue", "examplevenue"},
		{"  examplevenue  ", "examplevenue"},
	}
	for _, tt := range tests {
		if got := SlugFromURL(tt.in); got != tt.want {
			t.Errorf("SlugFromURL(%q) = %q; want %q", tt.in, got, tt.want)
		}
	}
}

func TestSlugCandidates(t *testing.T) {
	tests := []struct {
		name, query string
		wantFirst   string
		wantContain []string
	}{
		{
			name:        "spaces are removed",
			query:       "Blue Heron",
			wantFirst:   "blueheron",
			wantContain: []string{"blue-heron", "theblueheron"},
		},
		{
			// A venue may exist under the full name or under the name
			// with its leading article dropped, so the full name is
			// probed first and the shortened form still tried.
			name:        "leading article kept first",
			query:       "The Blue Heron",
			wantFirst:   "theblueheron",
			wantContain: []string{"blueheron", "theblue"},
		},
		{
			name:      "apostrophes vanish inside words",
			query:     "Jane's Kitchen",
			wantFirst: "janeskitchen",
		},
		{
			name:      "a URL resolves to exactly one candidate",
			query:     "https://www.sevenrooms.com/explore/examplevenue/reservations/create/search/",
			wantFirst: "examplevenue",
		},
	}
	for _, tt := range tests {
		got := slugCandidates(tt.query)
		if len(got) == 0 {
			t.Errorf("%s: no candidates for %q", tt.name, tt.query)
			continue
		}
		if got[0] != tt.wantFirst {
			t.Errorf("%s: first candidate = %q; want %q (all: %v)", tt.name, got[0], tt.wantFirst, got)
		}
		for _, w := range tt.wantContain {
			found := false
			for _, c := range got {
				if c == w {
					found = true
					break
				}
			}
			if !found {
				t.Errorf("%s: candidates %v missing %q", tt.name, got, w)
			}
		}
		if len(got) > maxCandidates {
			t.Errorf("%s: %d candidates exceeds the cap of %d", tt.name, len(got), maxCandidates)
		}
	}

	if got := slugCandidates("   "); got != nil {
		t.Errorf("slugCandidates(blank) = %v; want nil", got)
	}
}
