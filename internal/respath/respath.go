// Package respath resolves the on-disk layout for resz. All state, config,
// and logs live in subdirectories of the directory containing the running
// binary, so the layout is identical on every platform and travels with
// the workspace.
package respath

import (
	"os"
	"path/filepath"
)

// Root returns the directory containing the running resz binary, with
// symlinks resolved. It panics only if os.Executable returns an error,
// which on supported platforms it does not.
func Root() (string, error) {
	exe, err := os.Executable()
	if err != nil {
		return "", err
	}
	exe, err = filepath.EvalSymlinks(exe)
	if err != nil {
		return "", err
	}
	return filepath.Dir(exe), nil
}

// Config returns the directory for user-edited config files (venues.json,
// notify.json). Created on demand.
func Config() (string, error) { return ensure("config") }

// State returns the directory for machine-managed state (state.json,
// auth.json). Created on demand.
func State() (string, error) { return ensure("state") }

// Logs returns the directory for resz log files. Created on demand.
func Logs() (string, error) { return ensure("logs") }

func ensure(sub string) (string, error) {
	root, err := Root()
	if err != nil {
		return "", err
	}
	p := filepath.Join(root, sub)
	if err := os.MkdirAll(p, 0o700); err != nil {
		return "", err
	}
	return p, nil
}
