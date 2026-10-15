package resyapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

// rewriteTransport sends every request to srv, whatever host it names, so
// code that calls Resy's fixed URLs can be pointed at a test server.
type rewriteTransport struct{ srv *httptest.Server }

func (rt rewriteTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	u, _ := url.Parse(rt.srv.URL)
	r.URL.Scheme, r.URL.Host = u.Scheme, u.Host
	return http.DefaultTransport.RoundTrip(r)
}

func detailsAgainst(t *testing.T, status int, body string) error {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	_, err := Details(&http.Client{Transport: rewriteTransport{srv}}, "tok", "rgs://resy/1/2/1/2026-11-14/2026-11-14/15:30:00/2/Table", nil)
	return err
}

// Resy's response for a slot type that requires add-ons.
const addOnsBody = `{"status": 412, "code": "invalid_item_selection", "message": "Must select required add-ons.", "data": {"validate_response": {"code": 400, "details": ["Missing required option group"], "error_text": "Validation Error", "status": null}}, "title": null}`

func TestDetailsReportsRequiredAddOns(t *testing.T) {
	err := detailsAgainst(t, 412, addOnsBody)
	if !errors.Is(err, ErrAddOnsRequired) {
		t.Fatalf("err = %v; want ErrAddOnsRequired", err)
	}
	// The API error stays reachable for callers that inspect it.
	var ae *APIError
	if !errors.As(err, &ae) || ae.Status != 412 {
		t.Errorf("err = %v; want it to wrap the 412 APIError", err)
	}
}

func TestDetailsOtherErrorsAreNotAddOns(t *testing.T) {
	for _, tc := range []struct {
		status int
		body   string
	}{
		{412, `{"status": 412, "code": "something_else", "message": "nope"}`},
		{401, `{"message": "Unauthorized"}`},
		{500, `not json`},
	} {
		err := detailsAgainst(t, tc.status, tc.body)
		if err == nil || errors.Is(err, ErrAddOnsRequired) {
			t.Errorf("%d %s: err = %v; want a non-add-ons error", tc.status, tc.body, err)
		}
	}
	if err := detailsAgainst(t, 401, `{}`); !IsAuthError(err) {
		t.Errorf("401 should still be an auth error: %v", err)
	}
}

func TestTemplateID(t *testing.T) {
	for ref, want := range map[string]string{
		"rgs://resy/10003/3000003/1/2026-11-14/2026-11-14/15:30:00/2/Table": "3000003",
		"rgs://resy/10003":                     "",
		"sr://venueb/2026-11-14T19:00:00?cc=1": "",
		"":                                     "",
	} {
		if got := TemplateID(ref); got != want {
			t.Errorf("TemplateID(%q) = %q; want %q", ref, got, want)
		}
	}
}
