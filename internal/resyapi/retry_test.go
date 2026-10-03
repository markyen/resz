package resyapi

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestIsRetriable(t *testing.T) {
	retriable := []string{"http 500: ", "http 502: ", "http 503: upstream", "http 504: "}
	for _, m := range retriable {
		if !isRetriable(fmt.Errorf("%s", m)) {
			t.Errorf("%q should be retriable", m)
		}
	}
	// A 4xx is the request's own fault; retrying repeats the mistake.
	for _, m := range []string{"http 400: bad", "http 401: ", "http 404: ", "http 419: expired"} {
		if isRetriable(fmt.Errorf("%s", m)) {
			t.Errorf("%q should not be retriable", m)
		}
	}
	if isRetriable(nil) {
		t.Error("nil is not retriable")
	}
}

// The endpoint fails intermittently, so a run of 500s followed by a 200
// must still produce the 200.
func TestGetJSONRetryingSurvivesFlakiness(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if calls < 3 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Write([]byte(`{"results":{"venues":[{"slots":[]}]}}`))
	}))
	defer srv.Close()

	var out findResponse
	if err := getJSONRetrying(srv.Client(), srv.URL, &out, 5, nil); err != nil {
		t.Fatalf("should have recovered: %v", err)
	}
	if calls != 3 {
		t.Errorf("made %d calls, want 3 (two failures then success)", calls)
	}
}

// A 4xx must not be retried — it would just repeat a bad request.
func TestGetJSONRetryingDoesNotRetryClientErrors(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer srv.Close()

	var out findResponse
	err := getJSONRetrying(srv.Client(), srv.URL, &out, 5, nil)
	if err == nil {
		t.Fatal("expected the 400 to surface")
	}
	if calls != 1 {
		t.Errorf("made %d calls, want 1 — a 400 must not be retried", calls)
	}
}

// Exhausting the attempts reports how many were made, so a persistent
// outage is distinguishable from a single unlucky call.
func TestGetJSONRetryingGivesUp(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	var out findResponse
	err := getJSONRetrying(srv.Client(), srv.URL, &out, 3, nil)
	if err == nil {
		t.Fatal("expected failure")
	}
	if calls != 3 {
		t.Errorf("made %d calls, want 3", calls)
	}
	if !strings.Contains(err.Error(), "after 3 attempts") {
		t.Errorf("error should say how many attempts were made: %v", err)
	}
}
