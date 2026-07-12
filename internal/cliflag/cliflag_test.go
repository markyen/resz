package cliflag

import (
	"bytes"
	"flag"
	"reflect"
	"strings"
	"testing"
)

// newFS builds a FlagSet mirroring the shape of resz's real flags: short
// bool flags (a, i, v), short value flags (c, s), and single-dash long
// flags (city, list-cities) that must survive expansion untouched.
func newFS() *flag.FlagSet {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	fs.Bool("a", false, "")
	fs.Bool("i", false, "")
	fs.Bool("v", false, "")
	fs.String("c", "", "")
	fs.Int("s", 2, "")
	fs.String("city", "new-york-ny", "")
	fs.Bool("list-cities", false, "")
	return fs
}

func TestExpand(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want []string
	}{
		{"bool cluster", []string{"-ai"}, []string{"-a", "-i"}},
		{"bool cluster three", []string{"-aiv"}, []string{"-a", "-i", "-v"}},
		{"cluster then value attached", []string{"-ais2"}, []string{"-a", "-i", "-s", "2"}},
		{"cluster then value separate", []string{"-ais", "2"}, []string{"-a", "-i", "-s", "2"}},
		{"value flag attached", []string{"-s2"}, []string{"-s", "2"}},
		{"single long flag preserved", []string{"-city", "nyc"}, []string{"-city", "nyc"}},
		{"double dash long flag preserved", []string{"--city", "nyc"}, []string{"--city", "nyc"}},
		{"double dash long bool preserved", []string{"--list-cities"}, []string{"--list-cities"}},
		{"long bool flag preserved", []string{"-list-cities"}, []string{"-list-cities"}},
		{"already separate", []string{"-a", "-i"}, []string{"-a", "-i"}},
		{"equals form untouched", []string{"-s=4"}, []string{"-s=4"}},
		{"double dash passthrough", []string{"-ai", "--", "-iv"}, []string{"-a", "-i", "--", "-iv"}},
		{"unknown cluster untouched", []string{"-xy"}, []string{"-xy"}},
		{"value flag consumes unknown rest", []string{"-cfoo"}, []string{"-c", "foo"}},
		{"plain arg", []string{"carbone"}, []string{"carbone"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Expand(newFS(), tc.in)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("Expand(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// TestExpandThenParse verifies the expanded args actually parse into the
// expected flag values end to end.
func TestExpandThenParse(t *testing.T) {
	fs := newFS()
	a := fs.Bool("A", false, "") // extra to ensure no cross-talk; unused
	_ = a
	if err := fs.Parse(Expand(fs, []string{"-ais4", "-city", "boston-ma", "carbone"})); err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !fs.Lookup("a").Value.(flag.Getter).Get().(bool) {
		t.Error("a not set")
	}
	if !fs.Lookup("i").Value.(flag.Getter).Get().(bool) {
		t.Error("i not set")
	}
	if got := fs.Lookup("s").Value.(flag.Getter).Get().(int); got != 4 {
		t.Errorf("s = %d, want 4", got)
	}
	if got := fs.Lookup("city").Value.String(); got != "boston-ma" {
		t.Errorf("city = %q, want boston-ma", got)
	}
	if got := fs.Args(); len(got) != 1 || got[0] != "carbone" {
		t.Errorf("args = %q, want [carbone]", got)
	}
}

func TestPrintDefaults(t *testing.T) {
	fs := newFS()
	var buf bytes.Buffer
	fs.SetOutput(&buf)
	PrintDefaults(fs)
	out := buf.String()

	// Long flags render with a double dash...
	for _, want := range []string{"--city", "--list-cities"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n%s", want, out)
		}
	}
	// ...and never with a single dash.
	for _, bad := range []string{"  -city", "  -list-cities"} {
		if strings.Contains(out, bad) {
			t.Errorf("output has single-dash long flag %q\n%s", bad, out)
		}
	}
	// Short flags keep a single dash.
	if !strings.Contains(out, "  -c ") {
		t.Errorf("output missing single-dash short flag -c\n%s", out)
	}
	// String defaults are quoted; the -s int default is not.
	if !strings.Contains(out, `(default "new-york-ny")`) {
		t.Errorf("output missing quoted string default\n%s", out)
	}
	if !strings.Contains(out, "(default 2)") {
		t.Errorf("output missing unquoted int default\n%s", out)
	}
}
