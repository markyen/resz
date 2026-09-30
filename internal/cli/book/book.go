// Package book implements the `resz book` subcommand.
package book

import (
	"flag"
	"fmt"
	"os"
	"strings"

	"resz/internal/bookflow"
	"resz/internal/cliflag"
	"resz/internal/sevenroomsapi"
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
	authorizeFlag := fs.Float64("authorize", 0, "authorize a charge of up to this many dollars for this booking")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz book [-v] [-e EMAIL] [--authorize N] <rgs://config_id | sr://...>\n")
		fmt.Fprintln(os.Stderr, "Books a slot reference printed by `resz availability`.")
		fmt.Fprintln(os.Stderr, "  rgs:// (Resy)       token is loaded from cache or refreshed;")
		fmt.Fprintln(os.Stderr, "                      the password is read from $RESY_PASSWORD.")
		fmt.Fprintln(os.Stderr, "  sr://  (SevenRooms) guest checkout; the diner's details are read")
		fmt.Fprintln(os.Stderr, "                      from config/account.json.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "A slot that charges money at booking is refused unless --authorize")
		fmt.Fprintln(os.Stderr, "names an amount at least as large as the charge. Pass the figure you")
		fmt.Fprintln(os.Stderr, "were quoted: if the price has moved since, the booking is refused")
		fmt.Fprintln(os.Stderr, "rather than silently costing more.")
		cliflag.PrintDefaults(fs)
	}
	if err := fs.Parse(cliflag.Expand(fs, args)); err != nil {
		return 2
	}
	verbose = *verboseFlag

	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	ref := fs.Arg(0)

	var (
		body []byte
		err  error
	)
	switch {
	case sevenroomsapi.IsRef(ref):
		body, err = bookflow.BookSevenRoomsAuthorized(nil, ref, *authorizeFlag, vlogger())
	case strings.HasPrefix(ref, "rgs://"):
		body, err = bookflow.AuthAndBook(nil, ref, *emailFlag, os.Getenv("RESY_PASSWORD"), vlogger())
	default:
		fmt.Fprintln(os.Stderr, "error: argument must be an rgs:// (Resy) or sr:// (SevenRooms) slot reference")
		return 2
	}
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
