// Package notify implements the `resz notify` subcommand: an ad-hoc
// notification sender using the transport in config/notify.json.
package notify

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"

	transport "resz/internal/notify"
	"resz/internal/reslog"
)

// Main runs the notify subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz notify", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz notify <message...>\n")
		fmt.Fprintln(os.Stderr, "Sends via the transport in config/notify.json.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	if fs.NArg() == 0 {
		fs.Usage()
		return 2
	}
	msg := strings.Join(fs.Args(), " ")

	lg, f, err := reslog.Open("notify", false)
	if err != nil {
		fmt.Fprintln(os.Stderr, "log open:", err)
		return 1
	}
	defer f.Close()

	if err := transport.Send(msg); err != nil {
		if errors.Is(err, transport.ErrNoTransport) {
			lg.Printf("skipped (no transport configured) msg=%q", msg)
			fmt.Fprintln(os.Stderr, "skipped: no transport configured")
			return 0
		}
		lg.Printf("send FAIL: %v", err)
		fmt.Fprintln(os.Stderr, "send:", err)
		return 1
	}
	lg.Printf("send ok msg=%q", msg)
	return 0
}
