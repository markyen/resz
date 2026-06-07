// Command resz is the top-level entry point. It dispatches the first
// positional argument to a subcommand defined in internal/cli/<name>.
//
//	resz <command> [args...]
//
// Run `resz help` (or `resz` with no args) for the command list, and
// `resz <command> -h` for command-specific flags.
package main

import (
	"fmt"
	"os"

	"resz/internal/cli/availability"
	"resz/internal/cli/book"
	"resz/internal/cli/check"
	"resz/internal/cli/notify"
	"resz/internal/cli/search"
	"resz/internal/cli/snipe"
	"resz/internal/cli/tick"
)

type subcommand struct {
	name string
	desc string
	run  func(args []string) int
}

var subs = []subcommand{
	{"search", "find a venue id by name", search.Main},
	{"availability", "print the best slot in the target window", availability.Main},
	{"book", "book a slot by its rgs:// config_id", book.Main},
	{"snipe", "wait for a venue's snipe window, then auto-book", snipe.Main},
	{"check", "scheduled availability check + auto-book", check.Main},
	{"tick", "single cron entrypoint: housekeeping, then check + snipe", tick.Main},
	{"notify", "send an ad-hoc notification", notify.Main},
}

func main() {
	if len(os.Args) < 2 {
		usage(os.Stdout)
		os.Exit(2)
	}
	name := os.Args[1]
	switch name {
	case "-h", "--help", "help":
		usage(os.Stdout)
		return
	}
	for _, s := range subs {
		if s.name == name {
			os.Exit(s.run(os.Args[2:]))
		}
	}
	fmt.Fprintf(os.Stderr, "resz: unknown command %q\n\n", name)
	usage(os.Stderr)
	os.Exit(2)
}

func usage(w *os.File) {
	fmt.Fprintln(w, "usage: resz <command> [args...]")
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Commands:")
	for _, s := range subs {
		fmt.Fprintf(w, "  %-14s %s\n", s.name, s.desc)
	}
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Run 'resz <command> -h' for command-specific help.")
}
