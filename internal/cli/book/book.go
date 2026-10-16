// Package book implements the `resz book` subcommand.
package book

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"strings"

	"resz/internal/bookflow"
	"resz/internal/cliflag"
	"resz/internal/reslog"
	"resz/internal/resyapi"
	"resz/internal/sevenroomsapi"
	"resz/internal/venueconfig"
)

var verbose bool

func vlog(msg string) {
	if verbose {
		fmt.Fprintln(os.Stderr, msg)
	}
}

// Main runs the book subcommand. Returns a process exit code.
func Main(args []string) int {
	fs := flag.NewFlagSet("resz book", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	verboseFlag := fs.Bool("v", false, "print URLs and full response bodies on errors")
	emailFlag := fs.String("e", os.Getenv("RESY_EMAIL"), "email for token refresh (defaults to $RESY_EMAIL)")
	authorizeFlag := fs.Float64("authorize", 0, "authorize a charge of up to this many dollars for this booking")
	configFlag := fs.String("c", "", "venues config to update after booking (default <workspace>/config/venues.json)")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: resz book [-v] [-e EMAIL] [-c CONFIG] [--authorize N] <rgs://config_id | sr://...>\n")
		fmt.Fprintln(os.Stderr, "Books a slot reference printed by `resz availability`.")
		fmt.Fprintln(os.Stderr, "On success the venue stops being watched and the date is blacked out")
		fmt.Fprintln(os.Stderr, "in the venues config, as after a check or snipe booking. Every attempt")
		fmt.Fprintln(os.Stderr, "is logged to logs/book.log.")
		fmt.Fprintln(os.Stderr, "  rgs:// (Resy)       token is loaded from cache or refreshed;")
		fmt.Fprintln(os.Stderr, "                      the password is read from $RESY_PASSWORD.")
		fmt.Fprintln(os.Stderr, "  sr://  (SevenRooms) guest checkout; the diner's details are read")
		fmt.Fprintln(os.Stderr, "                      from config/account.json.")
		fmt.Fprintln(os.Stderr, "")
		fmt.Fprintln(os.Stderr, "A slot that charges money at booking (a deposit or prepayment, on either")
		fmt.Fprintln(os.Stderr, "platform) is refused unless --authorize names an amount at least as")
		fmt.Fprintln(os.Stderr, "large as the charge, including the tax and fees the venue adds. Pass the")
		fmt.Fprintln(os.Stderr, "figure you were quoted: if the price has moved since, the booking is")
		fmt.Fprintln(os.Stderr, "refused rather than silently costing more.")
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

	if !sevenroomsapi.IsRef(ref) && !strings.HasPrefix(ref, "rgs://") {
		fmt.Fprintln(os.Stderr, "error: argument must be an rgs:// (Resy) or sr:// (SevenRooms) slot reference")
		return 2
	}

	// Every attempt is logged, so a booking made from the command line —
	// typically after an approval — leaves the same trail check and snipe
	// leave for theirs.
	lg, lf, lerr := reslog.Open("book", false)
	if lerr != nil {
		fmt.Fprintln(os.Stderr, "warning: log open:", lerr)
	} else {
		defer lf.Close()
	}
	logf := func(format string, a ...any) {
		if lg != nil {
			lg.Printf(format, a...)
		}
	}
	if *authorizeFlag > 0 {
		logf("attempt %s (authorized up to $%.2f)", ref, *authorizeFlag)
	} else {
		logf("attempt %s", ref)
	}
	// Each step (hold, quote, Stripe intent) goes to the log as well, so a
	// charge can be traced afterwards. Nothing on this path logs the card
	// unredacted.
	steps := func(msg string) {
		logf("  %s", msg)
		vlog(msg)
	}

	var (
		body   []byte
		res    *sevenroomsapi.Reservation
		detail *resyapi.Detail
		err    error
	)
	if sevenroomsapi.IsRef(ref) {
		res, err = bookflow.BookSevenRoomsAuthorized(nil, ref, *authorizeFlag, steps)
		if res != nil {
			body = res.Raw
		}
	} else {
		body, detail, err = bookflow.AuthAndBookAuthorized(nil, ref, *emailFlag, os.Getenv("RESY_PASSWORD"), *authorizeFlag, steps)
	}
	if err != nil {
		logf("FAILED %s: %v", ref, err)
		fmt.Fprintln(os.Stderr, trimErr(err))
		if verbose && body != nil {
			fmt.Fprintln(os.Stderr, "response:", string(body))
		}
		return 1
	}
	fmt.Println(string(body))

	b, perr := parseBooked(ref)
	if perr != nil {
		// The booking stands; only the bookkeeping is lost.
		logf("BOOKED %s; could not read venue and date from the reference: %v", ref, perr)
		return 0
	}
	var (
		resyBody   []byte
		resyCharge float64
	)
	if res == nil {
		resyBody = body
		if detail != nil {
			resyCharge = detail.Charge
		}
	}
	line := summary(ref, b, res, resyBody, resyCharge)
	logf("%s", line)
	fmt.Fprintln(os.Stderr, line)
	updateConfig(*configFlag, b, logf)
	return 0
}

// updateConfig records a booking in venues.json; see recordBooking.
// Failures are logged and reported but never fail the command: the
// booking is already made.
func updateConfig(path string, b booked, logf func(string, ...any)) {
	warn := func(format string, a ...any) {
		logf(format, a...)
		fmt.Fprintf(os.Stderr, "warning: "+format+"\n", a...)
	}
	if path == "" {
		p, err := venueconfig.DefaultPath()
		if err != nil {
			warn("config path: %v", err)
			return
		}
		path = p
	}
	cfg, err := venueconfig.Load(path)
	if errors.Is(err, fs.ErrNotExist) {
		return
	}
	if err != nil {
		warn("load %s: %v", path, err)
		return
	}
	removed, blackedOut := recordBooking(cfg, b)
	if len(removed) == 0 {
		return
	}
	if err := venueconfig.Save(path, cfg); err != nil {
		warn("save %s: %v (still watching %s)", path, err, strings.Join(removed, ", "))
		return
	}
	for _, name := range removed {
		logf("[%s] removed from venues.json", name)
		fmt.Fprintf(os.Stderr, "stopped watching %s\n", name)
	}
	if blackedOut {
		logf("added %s to blackout_dates", b.date)
		fmt.Fprintf(os.Stderr, "blocked %s from further bookings\n", b.date)
	}
}

func trimErr(err error) string {
	s := err.Error()
	if !verbose && len(s) > 300 {
		return s[:300] + "...(truncated)"
	}
	return s
}
