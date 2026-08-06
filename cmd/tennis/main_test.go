package main

import (
	"flag"
	"reflect"
	"testing"

	"github.com/satoricorp/tennis"
)

// newTestFS builds a FlagSet mirroring the real subcommands' flags but with
// ContinueOnError, so a parse failure is a test assertion rather than
// os.Exit(2) taking the whole test binary down.
func newTestFS() (*flag.FlagSet, *string, *int, *bool) {
	fs := flag.NewFlagSet("test", flag.ContinueOnError)
	openai := fs.String("openai", "", "")
	n := fs.Int("n", 10, "")
	asJSON := fs.Bool("json", false, "")
	return fs, openai, n, asJSON
}

// Regression for the worst bug of the first review round: Go's flag package
// stops at the first positional, so `ns create cloud --openai X` silently
// dropped --openai and bound the namespace to the wrong embedder forever.
func TestParseInterleavedFlagsAfterPositionals(t *testing.T) {
	fs, openai, _, _ := newTestFS()
	pos, err := parseInterleaved(fs, []string{"create", "cloud", "--openai", "text-embedding-3-small"})
	if err != nil {
		t.Fatal(err)
	}
	if *openai != "text-embedding-3-small" {
		t.Errorf("--openai after positionals was dropped: got %q", *openai)
	}
	if !reflect.DeepEqual(pos, []string{"create", "cloud"}) {
		t.Errorf("positionals: got %v", pos)
	}
}

func TestParseInterleavedMixedOrder(t *testing.T) {
	fs, openai, n, asJSON := newTestFS()
	pos, err := parseInterleaved(fs, []string{"--json", "notes", "-n", "5", "query", "words", "--openai", "x"})
	if err != nil {
		t.Fatal(err)
	}
	if !*asJSON || *n != 5 || *openai != "x" {
		t.Errorf("flags: json=%v n=%d openai=%q", *asJSON, *n, *openai)
	}
	if !reflect.DeepEqual(pos, []string{"notes", "query", "words"}) {
		t.Errorf("positionals: got %v", pos)
	}
}

// "--" must end flag parsing permanently, even though this parser re-invokes
// Parse after each positional. Without the tail check, "match ns -- a -n"
// would try to parse -n as a flag again on the second pass.
func TestParseInterleavedDoubleDashStaysTerminal(t *testing.T) {
	fs, _, n, _ := newTestFS()
	pos, err := parseInterleaved(fs, []string{"notes", "--", "some", "-n", "-weird"})
	if err != nil {
		t.Fatal(err)
	}
	if *n != 10 {
		t.Errorf("-n after -- should stay positional, but n=%d", *n)
	}
	if !reflect.DeepEqual(pos, []string{"notes", "some", "-n", "-weird"}) {
		t.Errorf("positionals: got %v", pos)
	}
}

func TestParseInterleavedUnknownFlagErrors(t *testing.T) {
	fs, _, _, _ := newTestFS()
	if _, err := parseInterleaved(fs, []string{"notes", "--nope"}); err == nil {
		t.Error("unknown flag should error, not vanish")
	}
}

func TestParseWhere(t *testing.T) {
	if f, err := parseWhere(""); err != nil || f != nil {
		t.Errorf("empty where: f=%v err=%v", f, err)
	}
	if f, err := parseWhere("status=merged"); err != nil || f == nil {
		t.Errorf("simple where: f=%v err=%v", f, err)
	}
	if f, err := parseWhere("cost>5,status!=failed,name<=z"); err != nil || f == nil {
		t.Errorf("compound where: f=%v err=%v", f, err)
	}
	if _, err := parseWhere("no operator here"); err == nil {
		t.Error("garbage where should error")
	}
	// The library rejects hostile attribute names when the filter is used; the
	// parser's job is only to not blow up building it.
	if _, err := parseWhere("a=1"); err != nil {
		t.Error(err)
	}
	var _ tennis.Filter // keep the import honest
}

func TestCoerce(t *testing.T) {
	cases := []struct {
		in   string
		want any
	}{
		{"5", float64(5)},
		{"3.5", 3.5},
		{"-2", float64(-2)},
		{"05", "05"},   // leading zero: not the same number back, keep as text
		{"1e3", "1e3"}, // scientific input: round-trip differs, keep as text
		{"merged", "merged"},
		{"", ""},
	}
	for _, c := range cases {
		if got := coerce(c.in); got != c.want {
			t.Errorf("coerce(%q) = %v (%T), want %v (%T)", c.in, got, got, c.want, c.want)
		}
	}
}

func TestTruncate(t *testing.T) {
	if got := truncate("hello", 10); got != "hello" {
		t.Errorf("short: %q", got)
	}
	if got := truncate("hello world", 8); got != "hello w…" {
		t.Errorf("cut: %q", got)
	}
}
