// Package state persists resz-check's cooldown and health state between runs.
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"resz/internal/respath"
)

// HealthState describes the last-observed reachability of the resy api.
type HealthState string

const (
	HealthOK    HealthState = "ok"
	HealthError HealthState = "error"
)

// Schedule is the exponential backoff ladder applied by RecordError.
// In seconds: 30m, 60m, 120m, 240m, 480m, 960m, 1920m, 3840m (64h cap).
var Schedule = []int{1800, 3600, 7200, 14400, 28800, 57600, 115200, 230400}

// State is the persisted check-loop state.
type State struct {
	Version           int         `json:"version"`
	LastCheckTime     time.Time   `json:"last_check_time,omitempty"`
	LastErrorTime     time.Time   `json:"last_error_time,omitempty"`
	LastSuccessTime   time.Time   `json:"last_success_time,omitempty"`
	ErrorCount        int         `json:"error_count"`
	CurrentBackoffSec int         `json:"current_backoff_seconds"`
	Health            HealthState `json:"health"`
}

// Path returns the on-disk location of state.json, under the workspace's
// state/ subdirectory.
func Path() (string, error) {
	dir, err := respath.State()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "state.json"), nil
}

// Load reads state.json. A missing file yields a fresh State with Health=ok.
func Load() (*State, error) {
	p, err := Path()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return &State{Version: 1, Health: HealthOK}, nil
		}
		return nil, err
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	if s.Health == "" {
		s.Health = HealthOK
	}
	return &s, nil
}

// Save writes state.json atomically (via a temp file rename).
func Save(s *State) error {
	p, err := Path()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		return err
	}
	s.Version = 1
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	tmp := p + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, p)
}

// InCooldown reports whether enough time has passed since the last error.
// Returns true and the remaining wait if still in cooldown.
func (s *State) InCooldown(now time.Time) (bool, time.Duration) {
	if s.CurrentBackoffSec == 0 || s.LastErrorTime.IsZero() {
		return false, 0
	}
	deadline := s.LastErrorTime.Add(time.Duration(s.CurrentBackoffSec) * time.Second)
	if now.Before(deadline) {
		return true, deadline.Sub(now)
	}
	return false, 0
}

// RecordSuccess marks the check as successful and resets the backoff.
// Returns the previous health for caller's flip-detection logic.
func (s *State) RecordSuccess(now time.Time) HealthState {
	prev := s.Health
	s.LastCheckTime = now
	s.LastSuccessTime = now
	s.ErrorCount = 0
	s.CurrentBackoffSec = 0
	s.Health = HealthOK
	return prev
}

// RecordError advances one step along Schedule and marks health as error.
// Returns the previous health for caller's flip-detection logic.
func (s *State) RecordError(now time.Time) HealthState {
	prev := s.Health
	s.LastCheckTime = now
	s.LastErrorTime = now
	idx := s.ErrorCount
	if idx < 0 {
		idx = 0
	}
	if idx >= len(Schedule) {
		idx = len(Schedule) - 1
	}
	s.CurrentBackoffSec = Schedule[idx]
	s.ErrorCount++
	s.Health = HealthError
	return prev
}

// FormatBackoff renders a backoff window as "30m" or "64h".
func FormatBackoff(secs int) string {
	if secs == 0 {
		return ""
	}
	if secs >= 3600 {
		return fmt.Sprintf("%dh", secs/3600)
	}
	return fmt.Sprintf("%dm", secs/60)
}
