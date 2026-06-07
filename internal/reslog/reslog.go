// Package reslog provides a simple file-backed logger for resz commands.
// Logs live under the workspace's logs/ subdirectory (see internal/respath).
// On each Open call, a log file larger than maxBytes is rotated to
// <name>.log.old, then a fresh file is started.
package reslog

import (
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"

	"resz/internal/respath"
)

// maxBytes is the size threshold above which a log file rotates on Open.
const maxBytes = 1 << 20 // 1 MiB

// Open returns a *log.Logger writing to <name>.log, optionally also to
// stderr if alsoStderr is true. The returned *os.File should be Close()'d
// by the caller.
func Open(name string, alsoStderr bool) (*log.Logger, *os.File, error) {
	dir, err := respath.Logs()
	if err != nil {
		return nil, nil, err
	}
	p := filepath.Join(dir, name+".log")
	if info, err := os.Stat(p); err == nil && info.Size() > maxBytes {
		_ = os.Rename(p, p+".old")
	}
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, nil, err
	}
	var w io.Writer = f
	if alsoStderr {
		w = io.MultiWriter(f, os.Stderr)
	}
	prefix := fmt.Sprintf("resz-%s ", name)
	lg := log.New(w, prefix, log.LstdFlags|log.LUTC)
	return lg, f, nil
}
