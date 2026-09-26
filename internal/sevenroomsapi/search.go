package sevenroomsapi

import (
	"net/http"
	"strings"
	"sync"

	"resz/internal/platform"
)

// maxCandidates bounds how many slugs one Search will probe, so a long
// query can't fan out into a burst of requests.
const maxCandidates = 12

// Search finds SevenRooms venues matching a free-text query.
//
// SevenRooms publishes no venue directory or search API — venues are only
// reachable by the URL slug on their own booking link — so this works by
// deriving plausible slugs from the query ("Blue Heron" ->
// "blueheron", "blue-heron", "theblueheron", ...) and asking
// venue_info about each. That resolves the common case where the slug is
// just the name with the spaces removed, and returns nothing rather than
// a wrong guess otherwise.
//
// A full booking URL or an exact slug is passed straight through, so the
// reliable way to add an unusual venue is to paste its reservation link.
func Search(client *http.Client, query string, logger func(string)) ([]platform.SearchHit, error) {
	candidates := slugCandidates(query)
	if len(candidates) == 0 {
		return nil, nil
	}

	type result struct {
		venue *Venue
		err   error
	}
	results := make([]result, len(candidates))
	var wg sync.WaitGroup
	for i, slug := range candidates {
		wg.Add(1)
		go func(i int, slug string) {
			defer wg.Done()
			v, err := GetVenue(client, slug, logger)
			results[i] = result{venue: v, err: err}
		}(i, slug)
	}
	wg.Wait()

	var (
		hits []platform.SearchHit
		seen = map[string]bool{}
		// A 404 just means "that guess was wrong". Any other failure is
		// worth surfacing, but only if nothing matched at all — a
		// transient error on one guess shouldn't discard a real hit.
		firstErr error
	)
	for _, r := range results {
		if r.err != nil {
			if !IsUnknownVenue(r.err) && firstErr == nil {
				firstErr = r.err
			}
			continue
		}
		if seen[r.venue.Slug] {
			continue
		}
		seen[r.venue.Slug] = true
		hits = append(hits, platform.SearchHit{
			Platform: platform.SevenRooms,
			ID:       r.venue.Slug,
			Name:     r.venue.Name,
			City:     r.venue.City,
		})
	}
	if len(hits) == 0 && firstErr != nil {
		return nil, firstErr
	}
	// hits already follow candidate order, which is deliberately
	// best-guess-first: several guesses can all be real venues ("The
	// Old Mill" probes both "theoldmill" and "theold", and
	// both exist), so the ordering is the only thing distinguishing them.
	return hits, nil
}

// slugCandidates derives the slugs worth probing for a query, most likely
// first. A query that is already a slug or a booking URL yields just
// itself.
func slugCandidates(query string) []string {
	query = strings.TrimSpace(query)
	if query == "" {
		return nil
	}
	// A URL, or a single bare token that's already slug-shaped, is taken
	// at its word rather than permuted.
	if strings.Contains(query, "/") {
		return []string{SlugFromURL(query)}
	}

	words := slugWords(query)
	if len(words) == 0 {
		return nil
	}

	var out []string
	add := func(s string) {
		if s == "" || len(out) >= maxCandidates {
			return
		}
		for _, existing := range out {
			if existing == s {
				return
			}
		}
		out = append(out, s)
	}

	joined := strings.Join(words, "")
	add(joined)
	add(strings.Join(words, "-"))

	// Venues routinely fold a leading article into the slug, or drop one
	// the name has ("The Old Mill" -> "theoldmill"), so try both
	// directions.
	if words[0] == "the" {
		rest := words[1:]
		add(strings.Join(rest, ""))
		add(strings.Join(rest, "-"))
	} else {
		add("the" + joined)
	}

	// A trailing city or "nyc"-style qualifier is often not in the slug.
	if len(words) > 1 {
		add(strings.Join(words[:len(words)-1], ""))
	}
	return out
}

// slugWords lowercases a query and splits it into the alphanumeric runs a
// slug could be built from, so "Jane's Kitchen" becomes ["janes", "kitchen"].
func slugWords(query string) []string {
	var (
		words []string
		cur   strings.Builder
	)
	flush := func() {
		if cur.Len() > 0 {
			words = append(words, cur.String())
			cur.Reset()
		}
	}
	for _, r := range strings.ToLower(query) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			cur.WriteRune(r)
		case r == '\'' || r == '’':
			// Apostrophes vanish inside a word rather than splitting it.
		default:
			flush()
		}
	}
	flush()
	return words
}
