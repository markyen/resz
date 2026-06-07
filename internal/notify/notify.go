// Package notify sends user-facing alerts via a configurable external
// command. The transport is described in config/notify.json so the resz
// codebase has no knowledge of any specific messaging service. The
// message text is passed as the final argv when invoking the command.
package notify

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"resz/internal/respath"
)

// Config describes the notification transport.
type Config struct {
	// Command is the argv vector to invoke. The message text is appended
	// as the final element on each Send call. Example:
	//
	//   {"command": ["mycli", "send", "--to", "+15555550100", "--message"]}
	//
	// would execute: mycli send --to +15555550100 --message "<the message>"
	Command []string `json:"command"`
}

// ErrNoTransport is returned by Send when notify.json is missing or has
// an empty Command. Callers may treat this as a soft error.
var ErrNoTransport = errors.New("no notification transport configured")

// ConfigPath returns the on-disk path of notify.json.
func ConfigPath() (string, error) {
	dir, err := respath.Config()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "notify.json"), nil
}

// LoadConfig reads notify.json. Returns (nil, nil) if the file does not
// exist; returns an error for any other read or parse failure.
func LoadConfig() (*Config, error) {
	p, err := ConfigPath()
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(p)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var c Config
	if err := json.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("parse %s: %w", p, err)
	}
	return &c, nil
}

// Send dispatches a message via the configured command. Returns
// ErrNoTransport if no transport is configured.
func Send(message string) error {
	cfg, err := LoadConfig()
	if err != nil {
		return err
	}
	if cfg == nil || len(cfg.Command) == 0 {
		return ErrNoTransport
	}
	args := append(append([]string{}, cfg.Command[1:]...), message)
	cmd := exec.Command(cfg.Command[0], args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s: %w (%s)", cfg.Command[0], err, strings.TrimSpace(string(out)))
	}
	return nil
}
