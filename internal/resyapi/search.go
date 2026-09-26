package resyapi

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"resz/internal/platform"
)

const searchURL = BaseURL + "/3/venuesearch/search"

type hit struct {
	Name string `json:"name"`
	ID   struct {
		Resy json.Number `json:"resy"`
	} `json:"id"`
	Location struct {
		Name string `json:"name"`
	} `json:"location"`
}

type searchResponse struct {
	Search struct {
		Hits []hit `json:"hits"`
	} `json:"search"`
}

// Search runs the venuesearch query biased to the given (lat, long).
// Most callers should pass coordinates from a City in Cities.
func Search(query string, lat, long float64) ([]platform.SearchHit, error) {
	body := map[string]any{
		"availability": true,
		"venue_filter": map[string]any{},
		"slot_filter": map[string]any{
			"party_size": 2,
			"day":        time.Now().Format("2006-01-02"),
		},
		"query": query,
		"geo": map[string]any{
			"latitude":  lat,
			"longitude": long,
			"user": map[string]any{
				"latitude":  lat,
				"longitude": long,
			},
			"radius": 40000,
		},
		"per_page": 75,
	}

	buf, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}

	req, err := http.NewRequest("POST", searchURL, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	setResyHeaders(req, "")
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	var out searchResponse
	dec := json.NewDecoder(resp.Body)
	dec.UseNumber()
	if err := dec.Decode(&out); err != nil {
		return nil, fmt.Errorf("decode: %w", err)
	}
	hits := make([]platform.SearchHit, 0, len(out.Search.Hits))
	for _, h := range out.Search.Hits {
		hits = append(hits, platform.SearchHit{
			Platform: platform.Resy,
			ID:       h.ID.Resy.String(),
			Name:     h.Name,
			City:     h.Location.Name,
		})
	}
	return hits, nil
}
