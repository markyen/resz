// Package book implements the `resz book` subcommand.
package book

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"resz/internal/resyflow"
)

var verbose bool

func vlog(msg string) {
	if verbose {
		fmt.Fprintln(os.Stderr, msg)
	}
}

func vlogger() func(string) {
	if verbose {
		return vlog
	}
	return nil
}

// Main runs the book subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz book", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	verboseFlag := fs.Bool("v", false, "print URLs and full response bodies on errors")
	emailFlag := fs.String("e", os.Getenv("RESY_EMAIL"), "email for token refresh (defaults to $RESY_EMAIL)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz book [-v] [-e EMAIL] <rgs://config_id>\n")
		fmt.Fprintln(os.Stderr, "Token is loaded from cache or refreshed; password is read from $RESY_PASSWORD.")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}
	verbose = *verboseFlag

	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	configID := fs.Arg(0)
	if !strings.HasPrefix(configID, "rgs://") {
		fmt.Fprintln(os.Stderr, "error: argument must be an rgs:// config_id")
		return 2
	}

	body, err := resyflow.AuthAndBook(nil, configID, *emailFlag, os.Getenv("RESY_PASSWORD"), vlogger())
	if err != nil {
		fmt.Fprintln(os.Stderr, trimErr(err))
		if verbose && body != nil {
			fmt.Fprintln(os.Stderr, "response:", string(body))
		}
		return 1
	}
	fmt.Println(string(body))
	return 0
}

func trimErr(err error) string {
	s := err.Error()
	if !verbose && len(s) > 300 {
		return s[:300] + "...(truncated)"
	}
	return s
}
