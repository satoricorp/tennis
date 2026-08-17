package main

import (
	"flag"
	"reflect"
	"strings"
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

// --- the namespace ----------------------------------------------------------

func TestResolveNS(t *testing.T) {
	t.Setenv("TENNIS_NS", "")
	if got := resolveNS(""); got != defaultNamespace {
		t.Errorf("nothing set: got %q, want %q", got, defaultNamespace)
	}
	if got := resolveNS("work"); got != "work" {
		t.Errorf("--ns should win: got %q", got)
	}
	t.Setenv("TENNIS_NS", "fromenv")
	if got := resolveNS(""); got != "fromenv" {
		t.Errorf("$TENNIS_NS should be used: got %q", got)
	}
	// The flag is the more specific statement of intent, so it outranks the
	// environment the same way --db outranks $TENNIS_DB.
	if got := resolveNS("work"); got != "work" {
		t.Errorf("--ns should outrank $TENNIS_NS: got %q", got)
	}
}

// --- the result line --------------------------------------------------------

func TestCitation(t *testing.T) {
	cases := []struct {
		name string
		r    tennis.Result
		want string
	}{
		{
			"an imported session names its service and day",
			tennis.Result{Score: 0.0231, Attributes: map[string]any{
				"source": "chatgpt", "created": "2025-03-15T09:12:00Z",
			}},
			"ChatGPT [2025-03-15] 0.0231",
		},
		{
			"the hyphenated source is spelled the way it is said",
			tennis.Result{Score: 0.5, Attributes: map[string]any{
				"source": "claude-code", "created": "2026-06-09T19:25:08Z",
			}},
			"Claude Code [2026-06-09] 0.5000",
		},
		{
			"codex",
			tennis.Result{Score: 0.25, Attributes: map[string]any{
				"source": "codex", "created": "2026-06-09T19:25:08Z",
			}},
			"Codex [2026-06-09] 0.2500",
		},
		{
			// A seeded file has no source attribute at all.
			"a file falls back to its name and mtime",
			tennis.Result{Score: 0.125, Attributes: map[string]any{
				"name": "auth.md", "modified": "2026-08-17T00:00:00Z",
			}},
			"auth.md [2026-08-17] 0.1250",
		},
		{
			// Undated documents are real: put accepts anything with an id.
			"no date leaves the brackets off rather than printing an empty one",
			tennis.Result{Score: 0.75, Attributes: map[string]any{"source": "claude"}},
			"Claude 0.7500",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := citation(c.r); got != c.want {
				t.Errorf("citation() = %q, want %q", got, c.want)
			}
		})
	}
}

// A result's text is a chunk of a transcript, so it arrives with the newlines
// and runs of spaces it was written with. The answer line is one line.
func TestOneLine(t *testing.T) {
	if got := oneLine("You stayed at\n  Hotel Esencia\n\nin Tulum.\n"); got != "You stayed at Hotel Esencia in Tulum." {
		t.Errorf("oneLine() = %q", got)
	}
}

func TestWrap(t *testing.T) {
	cases := []struct {
		name        string
		in          string
		width       int
		first, rest string
		want        string
	}{
		{
			name:  "folds at the measure and indents the continuation",
			in:    "one two three four five six",
			width: 14, first: "> ", rest: "  ",
			want: "> one two\n  three four\n  five six",
		},
		{
			// Smart quotes are three bytes and one column; counting bytes
			// wraps early and leaves the right margin ragged.
			name:  "counts runes, not bytes",
			in:    "I’m fine ok",
			width: 11, first: "", rest: "",
			want: "I’m fine ok",
		},
		{
			name:  "keeps the author's own line breaks",
			in:    "para one\n\npara two",
			width: 40, first: "  ", rest: "  ",
			want: "  para one\n\n  para two",
		},
		{
			// Breaking a URL somewhere arbitrary is worse than overrunning.
			name:  "lets an overlong word overrun",
			in:    "see https://example.com/a/very/long/path now",
			width: 12, first: "", rest: "",
			want: "see\nhttps://example.com/a/very/long/path\nnow",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := wrap(c.in, c.width, c.first, c.rest); got != c.want {
				t.Errorf("wrap()\n got: %q\nwant: %q", got, c.want)
			}
		})
	}
}

// A blank line must not carry the indent out as trailing whitespace.
func TestWrapLeavesNoTrailingSpace(t *testing.T) {
	got := wrap("a\n\nb", 40, "  ", "  ")
	for _, line := range strings.Split(got, "\n") {
		if line != strings.TrimRight(line, " ") {
			t.Errorf("line has trailing space: %q", line)
		}
	}
}
