package resyapi

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
)

// authMu serializes EnsureAuth across the per-venue snipe goroutines so a
// batch of concurrent callers that all find the cache expired produce a
// single Login (and a single cache write) instead of a thundering herd of
// logins racing on auth.json.
var authMu sync.Mutex

const passwordURL = BaseURL + "/3/auth/password"

type LoginResult struct {
	Token           string
	PaymentMethodID int64
	ExpiresAt       int64
}

type passwordResponse struct {
	Token           string `json:"token"`
	PaymentMethodID int64  `json:"payment_method_id"`
}

func Login(email, password string) (*LoginResult, error) {
	form := url.Values{}
	form.Set("email", email)
	form.Set("password", password)

	req, err := http.NewRequest("POST", passwordURL, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	setResyHeaders(req, "")
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=utf-8")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("http %d: %s", resp.StatusCode, string(body))
	}

	var pr passwordResponse
	if err := json.NewDecoder(resp.Body).Decode(&pr); err != nil {
		return nil, err
	}
	if pr.Token == "" {
		return nil, fmt.Errorf("empty token in response")
	}
	exp, err := jwtExp(pr.Token)
	if err != nil {
		return nil, fmt.Errorf("parse jwt exp: %w", err)
	}
	return &LoginResult{
		Token:           pr.Token,
		PaymentMethodID: pr.PaymentMethodID,
		ExpiresAt:       exp,
	}, nil
}

// EnsureAuth returns a non-expired CachedAuth, refreshing via Login and
// updating the on-disk cache when needed. logger, if non-nil, receives
// short status messages (e.g. "using cached auth", "refreshing token").
func EnsureAuth(email, password string, logger func(string)) (*CachedAuth, error) {
	authMu.Lock()
	defer authMu.Unlock()
	if a, err := LoadAuth(); err == nil && a.IsValid() {
		if logger != nil {
			logger("using cached auth")
		}
		return a, nil
	} else if logger != nil {
		switch {
		case err != nil && !os.IsNotExist(err):
			logger("cache load error: " + err.Error())
		case a != nil && !a.IsValid():
			logger("cached auth expired")
		default:
			logger("no cached auth")
		}
	}

	if email == "" {
		return nil, fmt.Errorf("email required to refresh auth")
	}
	if password == "" {
		return nil, fmt.Errorf("password required to refresh auth")
	}
	if logger != nil {
		logger("logging in as " + email)
	}
	res, err := Login(email, password)
	if err != nil {
		return nil, err
	}
	a := &CachedAuth{
		Token:           res.Token,
		PaymentMethodID: res.PaymentMethodID,
		ExpiresAt:       res.ExpiresAt,
	}
	if err := SaveAuth(a); err != nil {
		return nil, fmt.Errorf("save cache: %w", err)
	}
	if logger != nil {
		p, _ := CachePath()
		logger("saved auth to " + p)
	}
	return a, nil
}
