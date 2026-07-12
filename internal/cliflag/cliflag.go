// Package cliflag augments the standard library's flag package with
// getopt-style clustering of single-character short flags, so boolean
// flags can be combined (e.g. "-ai" for "-a -i", or "-ais2" for
// "-a -i -s 2"). Call Expand on the raw args before FlagSet.Parse.
package cliflag

import (
	"flag"
	"fmt"
	"reflect"
	"strings"
)

// Expand rewrites clustered single-dash short flags into their separate
// forms using getopt semantics. It must be called after all flags are
// defined on fs and before fs.Parse.
//
// Rules, applied per argument:
//   - "--" and everything after it pass through unchanged.
//   - A token that is already a defined flag name (e.g. "-city", "-c")
//     passes through unchanged, so single-dash long flags are preserved.
//   - Otherwise, for a "-xyz" token, each character is matched left to
//     right: a boolean flag emits "-x" and continues; a value flag
//     consumes the rest of the cluster as its value ("-s2" -> "-s 2") or,
//     if nothing remains, defers to the following arg ("-s" then "2").
//   - A cluster whose first character is not a known flag passes through
//     unchanged, leaving the flag package to report the error.
func Expand(fs *flag.FlagSet, args []string) []string {
	out := make([]string, 0, len(args))
	for i, arg := range args {
		if arg == "--" {
			return append(out, args[i:]...)
		}
		// Only single-dash clusters of length >= 2, with no '='.
		if len(arg) < 3 || arg[0] != '-' || arg[1] == '-' || strings.ContainsRune(arg, '=') {
			out = append(out, arg)
			continue
		}
		body := arg[1:]
		// A token that is itself a defined flag (e.g. "-city") is not a
		// cluster; leave it for the flag package.
		if fs.Lookup(body) != nil || fs.Lookup(string(body[0])) == nil {
			out = append(out, arg)
			continue
		}
		if expanded, ok := splitCluster(fs, body); ok {
			out = append(out, expanded...)
		} else {
			out = append(out, arg)
		}
	}
	return out
}

// splitCluster expands the characters of a short-flag cluster (without the
// leading dash). It returns ok=false if it hits an unknown character, so
// the caller can leave the original token untouched.
func splitCluster(fs *flag.FlagSet, body string) ([]string, bool) {
	var out []string
	for j := 0; j < len(body); j++ {
		name := string(body[j])
		f := fs.Lookup(name)
		if f == nil {
			return nil, false
		}
		if isBoolFlag(f) {
			out = append(out, "-"+name)
			continue
		}
		// Value flag: the remainder of the cluster is its value; if empty,
		// the following arg supplies it (handled by the flag package).
		if rest := body[j+1:]; rest != "" {
			out = append(out, "-"+name, rest)
		} else {
			out = append(out, "-"+name)
		}
		return out, true
	}
	return out, true
}

func isBoolFlag(f *flag.Flag) bool {
	bf, ok := f.Value.(interface{ IsBoolFlag() bool })
	return ok && bf.IsBoolFlag()
}

// PrintDefaults writes the flag listing to fs.Output() like the standard
// flag.FlagSet.PrintDefaults, except multi-character (long) flag names are
// rendered with a double dash ("--city") and single-character short flags
// keep a single dash ("-c"). Use it in fs.Usage so the auto-generated help
// matches the "-short / --long" convention.
func PrintDefaults(fs *flag.FlagSet) {
	fs.VisitAll(func(f *flag.Flag) {
		var b strings.Builder
		fmt.Fprintf(&b, "  %s%s", dashFor(f.Name), f.Name)
		name, usage := flag.UnquoteUsage(f)
		if name != "" {
			b.WriteString(" ")
			b.WriteString(name)
		}
		// Short entries share a line; longer ones wrap, matching the
		// stdlib's alignment heuristic.
		if b.Len() <= 4 {
			b.WriteString("\t")
		} else {
			b.WriteString("\n    \t")
		}
		b.WriteString(strings.ReplaceAll(usage, "\n", "\n    \t"))
		if !defaultIsZero(f) {
			if flagKind(f) == reflect.String {
				fmt.Fprintf(&b, " (default %q)", f.DefValue)
			} else {
				fmt.Fprintf(&b, " (default %v)", f.DefValue)
			}
		}
		fmt.Fprint(fs.Output(), b.String(), "\n")
	})
}

// dashFor returns the dash prefix for a flag name: "--" for long names,
// "-" for single-character short names.
func dashFor(name string) string {
	if len(name) > 1 {
		return "--"
	}
	return "-"
}

// flagKind reports the underlying kind of a flag's value (String, Bool,
// Int, ...) by unwrapping the pointer the flag package stores.
func flagKind(f *flag.Flag) reflect.Kind {
	rv := reflect.ValueOf(f.Value)
	for rv.Kind() == reflect.Ptr {
		rv = rv.Elem()
	}
	return rv.Kind()
}

// defaultIsZero reports whether a flag's default equals its type's zero
// value, in which case the listing omits the "(default ...)" suffix (as the
// stdlib does).
func defaultIsZero(f *flag.Flag) bool {
	switch flagKind(f) {
	case reflect.String:
		return f.DefValue == ""
	case reflect.Bool:
		return f.DefValue == "false"
	default:
		return f.DefValue == "0"
	}
}
