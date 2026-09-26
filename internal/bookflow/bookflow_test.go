package bookflow

import (
	"reflect"
	"testing"

	"resz/internal/platform"
)

func TestMatchRankOrdersExactMatchFirst(t *testing.T) {
	// The case that motivated ranking: Resy's fuzzy search returns dozens
	// of loosely-related venues, and without ranking its first hit would
	// outrank an exact match found on SevenRooms.
	query := "Example Venue"
	exact := matchRank("Example Venue", query)
	prefix := matchRank("Example Venue Brooklyn", query)
	unrelated := matchRank("Heron Incident", query)
	if !(exact < prefix && prefix < unrelated) {
		t.Errorf("ranks not ordered: exact=%d prefix=%d unrelated=%d", exact, prefix, unrelated)
	}
}

func TestMatchRankIgnoresCaseAndPunctuation(t *testing.T) {
	// SevenRooms operators type names in freely, so trailing whitespace,
	// case and punctuation differences are routine.
	if got := matchRank("Example Venue", "example venue"); got != 0 {
		t.Errorf("case-insensitive match rank = %d; want 0", got)
	}
	if got := matchRank("Jane's Kitchen", "janes kitchen"); got != 0 {
		t.Errorf("punctuation-insensitive match rank = %d; want 0", got)
	}
	if got := matchRank("EXAMPLE VENUE", "Example Venue"); got != 0 {
		t.Errorf("uppercase name rank = %d; want 0", got)
	}
}

func TestPlatformOfURL(t *testing.T) {
	tests := []struct {
		in     string
		want   platform.Platform
		wantOK bool
	}{
		{"https://www.sevenrooms.com/explore/examplevenue/reservations/create/search/", platform.SevenRooms, true},
		{"https://fp.sevenrooms.com/explore/examplevenue/", platform.SevenRooms, true},
		{"https://resy.com/cities/ny/example-venue", platform.Resy, true},
		// A plain query names no platform, even one containing a slash.
		{"Example Venue", "", false},
		{"Bar / Restaurant", "", false},
	}
	for _, tt := range tests {
		got, ok := platformOfURL(tt.in)
		if got != tt.want || ok != tt.wantOK {
			t.Errorf("platformOfURL(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.wantOK)
		}
	}
}

func TestIntersect(t *testing.T) {
	all := platform.All()
	if got := intersect(all, platform.SevenRooms); !reflect.DeepEqual(got, []platform.Platform{platform.SevenRooms}) {
		t.Errorf("intersect(all, sevenrooms) = %v; want [sevenrooms]", got)
	}
	// An explicit --platform resy with a SevenRooms URL should find
	// nothing rather than quietly searching SevenRooms anyway.
	if got := intersect([]platform.Platform{platform.Resy}, platform.SevenRooms); got != nil {
		t.Errorf("intersect([resy], sevenrooms) = %v; want nil", got)
	}
}
