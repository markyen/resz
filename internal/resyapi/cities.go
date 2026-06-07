package resyapi

// City holds geo coordinates and a timezone for one Resy market.
type City struct {
	Slug     string
	Lat      float64
	Long     float64
	Timezone string // IANA timezone name
}

// Cities is the list of supported city slugs accepted by resy-search and
// used by resy-snipe to interpret target_time / bias search results.
var Cities = []City{
	{"athens-greece", 37.9838, 23.7275, "Europe/Athens"},
	{"atlanta-ga", 33.7490, -84.3880, "America/New_York"},
	{"austin-tx", 30.2672, -97.7431, "America/Chicago"},
	{"barcelona-spain", 41.3851, 2.1734, "Europe/Madrid"},
	{"boston-ma", 42.3601, -71.0589, "America/New_York"},
	{"charleston-sc", 32.7765, -79.9311, "America/New_York"},
	{"chicago-il", 41.8781, -87.6298, "America/Chicago"},
	{"dallas-fort-worth-tx", 32.7767, -96.7970, "America/Chicago"},
	{"detroit-mi", 42.3314, -83.0458, "America/Detroit"},
	{"hamptons-ny", 40.9634, -72.1848, "America/New_York"},
	{"hong-kong", 22.3193, 114.1694, "Asia/Hong_Kong"},
	{"houston-tx", 29.7604, -95.3698, "America/Chicago"},
	{"las-vegas-nv", 36.1699, -115.1398, "America/Los_Angeles"},
	{"los-angeles-ca", 34.0522, -118.2437, "America/Los_Angeles"},
	{"madrid-spain", 40.4168, -3.7038, "Europe/Madrid"},
	{"miami-fl", 25.7617, -80.1918, "America/New_York"},
	{"minneapolis-mn", 44.9778, -93.2650, "America/Chicago"},
	{"nashville-tn", 36.1627, -86.7816, "America/Chicago"},
	{"new-orleans-la", 29.9511, -90.0715, "America/Chicago"},
	{"new-york-ny", 40.7128, -74.0060, "America/New_York"},
	{"philadelphia-pa", 39.9526, -75.1652, "America/New_York"},
	{"portland-or", 45.5152, -122.6784, "America/Los_Angeles"},
	{"san-francisco-ca", 37.7749, -122.4194, "America/Los_Angeles"},
	{"seattle-wa", 47.6062, -122.3321, "America/Los_Angeles"},
	{"washington-dc", 38.9072, -77.0369, "America/New_York"},
}

// DefaultCity is the city used when no slug is provided.
const DefaultCity = "new-york-ny"

var citiesBySlug = func() map[string]*City {
	m := make(map[string]*City, len(Cities))
	for i := range Cities {
		m[Cities[i].Slug] = &Cities[i]
	}
	return m
}()

// LookupCity returns the city with the given slug, or nil/false if unknown.
func LookupCity(slug string) (*City, bool) {
	c, ok := citiesBySlug[slug]
	return c, ok
}

// CitySlugs returns all supported slugs.
func CitySlugs() []string {
	out := make([]string, len(Cities))
	for i, c := range Cities {
		out[i] = c.Slug
	}
	return out
}
