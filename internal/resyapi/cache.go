package resyapi

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"resz/internal/respath"
)

type CachedAuth struct {
	Token           string `json:"token"`
	PaymentMethodID int64  `json:"payment_method_id"`
	ExpiresAt       int64  `json:"expires_at"`
}

func CachePath() (string, error) {
	dir, err := respath.State()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "auth.json"), nil
}

func LoadAuth() (*CachedAuth, error) {
	p, err := CachePath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	var a CachedAuth
	if err := json.Unmarshal(data, &a); err != nil {
		return nil, err
	}
	return &a, nil
}

func SaveAuth(a *CachedAuth) error {
	p, err := CachePath()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(a, "", "  ")
	if err != nil {
		return err
	}
	// Write atomically (temp file + rename) like state.Save and
	// venueconfig.Save, so a crash or a concurrent writer can never leave a
	// torn auth.json that a subsequent LoadAuth fails to parse.
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// ClearAuth removes the cached auth file. Returns nil if it does not exist.
func ClearAuth() error {
	p, err := CachePath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// IsValid reports whether the cached token is present and not within
// 60s of expiring.
func (a *CachedAuth) IsValid() bool {
	if a == nil || a.Token == "" {
		return false
	}
	return time.Now().Unix()+60 < a.ExpiresAt
}

func jwtExp(token string) (int64, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return 0, fmt.Errorf("not a 3-part JWT")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return 0, err
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return 0, err
	}
	if claims.Exp == 0 {
		return 0, fmt.Errorf("no exp claim")
	}
	return claims.Exp, nil
}
