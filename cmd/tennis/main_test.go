package main

import (
	"encoding/json"
	"flag"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

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

// TestParseWhereQuotedValues: a card's last line filters on a path or a
// session id, and either can hold a comma, which ends a clause. A double-quoted
// value keeps its commas, reads \" and \\ as a quote and a backslash, and is
// text even when it looks like a number. Unquoted values and every operator
// read as they always did.
func TestParseWhereQuotedValues(t *testing.T) {
	cases := []struct {
		in   string
		want tennis.Filter
	}{
		{`path="/n/budget, 2026.md"`, tennis.Eq("path", "/n/budget, 2026.md")},
		{`path="/n/say \"hi\".md"`, tennis.Eq("path", `/n/say "hi".md`)},
		{`path="/n/back\\slash.md"`, tennis.Eq("path", `/n/back\slash.md`)},
		{`path="C:\Users\joe"`, tennis.Eq("path", `C:\Users\joe`)}, // any other backslash is kept
		{`path="/n/café, 日本.md"`, tennis.Eq("path", "/n/café, 日本.md")},
		{`path="/n/it's"`, tennis.Eq("path", "/n/it's")},
		{`session="2026"`, tennis.Eq("session", "2026")},
		{`session=2026`, tennis.Eq("session", float64(2026))},
		{`title=""`, tennis.Eq("title", "")},
		{` a = "x, y" , b!=y `, tennis.And(tennis.Eq("a", "x, y"), tennis.NotEq("b", "y"))},
		{`a="x",b>=2`, tennis.And(tennis.Eq("a", "x"), tennis.Gte("b", float64(2)))},
		{`a<1,b>2,c<=3,d>=4,e!=5,f=6`, tennis.And(
			tennis.Lt("a", float64(1)), tennis.Gt("b", float64(2)), tennis.Lte("c", float64(3)),
			tennis.Gte("d", float64(4)), tennis.NotEq("e", float64(5)), tennis.Eq("f", float64(6)))},
		{`cost>5,status!=failed,name<=z`, tennis.And(
			tennis.Gt("cost", float64(5)), tennis.NotEq("status", "failed"), tennis.Lte("name", "z"))},
		// The first operator is the clause's; the value may hold another.
		{`path=/a>=b`, tennis.Eq("path", "/a>=b")},
		{`path!=/a=b`, tennis.NotEq("path", "/a=b")},
		// A quote that does not open the value is part of it, as before.
		{`title=say "hi`, tennis.Eq("title", `say "hi`)},
		{`status=merged,`, tennis.Eq("status", "merged")},
	}
	for _, c := range cases {
		got, err := parseWhere(c.in)
		if err != nil {
			t.Errorf("parseWhere(%q): %v", c.in, err)
			continue
		}
		if !reflect.DeepEqual(got, c.want) {
			t.Errorf("parseWhere(%q) = %#v, want %#v", c.in, got, c.want)
		}
	}
	for _, bad := range []string{
		`path="/n/no closing quote`,
		`path="/n/a" trailing`,
		`path="/n/a\"`,
		`no operator here`,
		`=5`,
		`a=1,junk`,
		`a!b`,
	} {
		if f, err := parseWhere(bad); err == nil {
			t.Errorf("parseWhere(%q) = %#v, want an error", bad, f)
		}
	}
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

func TestRenderResultsNumbering(t *testing.T) {
	r := func(text string, kw, sem int) tennis.Result {
		return tennis.Result{
			Text: text, Score: 0.5, KeywordRank: kw, SemanticRank: sem,
			Attributes: map[string]any{"source": "codex", "created": "2026-06-19T00:00:00Z"},
		}
	}

	t.Run("one result is an answer, not a list of one", func(t *testing.T) {
		var b strings.Builder
		renderResults(&b, []tennis.Result{r("alpha", 1, 1)}, 60, styler{})
		got := b.String()
		if !strings.HasPrefix(got, "> alpha") {
			t.Errorf("want the answer form, got:\n%s", got)
		}
		if strings.Contains(got, " 1. ") {
			t.Errorf("a lone result should not be numbered, got:\n%s", got)
		}
	})

	t.Run("many results are numbered from one", func(t *testing.T) {
		var b strings.Builder
		renderResults(&b, []tennis.Result{r("alpha", 1, 1), r("beta", 2, 2)}, 60, styler{})
		got := b.String()
		// The regression this guards: the list used to start at 2, under an
		// unnumbered top result, and read as though item 1 had gone missing.
		if !strings.Contains(got, " 1. ") {
			t.Errorf("the best result must be numbered 1, got:\n%s", got)
		}
		if !strings.Contains(got, " 2. ") {
			t.Errorf("want a second numbered result, got:\n%s", got)
		}
		if strings.Contains(got, "> alpha") {
			t.Errorf("the answer form should not appear in a list, got:\n%s", got)
		}
		// Every entry carries its ranker tag, the top one included.
		if strings.Count(got, "kw#") != 2 {
			t.Errorf("both results should carry ranker tags, got:\n%s", got)
		}
	})
}

// listsCommand reports whether the help screen has a line whose first word is
// name — a plain substring check would match "--ns <name>" when looking for
// the ns command, and "import" inside a sentence about importing.
func listsCommand(help, name string) bool {
	for _, line := range strings.Split(help, "\n") {
		if fields := strings.Fields(line); len(fields) > 0 && fields[0] == name {
			return true
		}
	}
	return false
}

// TestHelpTextHidesAndReveals pins the two screens apart. The short one is the
// commands a person should reach for; the long one is everything that still
// works. Getting this backwards is silent — the command runs either way — so
// the split is worth a test rather than a convention.
func TestHelpTextHidesAndReveals(t *testing.T) {
	short, long := helpText(false), helpText(true)

	for _, cmd := range []string{"add", "search", "ls", "rm", "ns", "version"} {
		if !listsCommand(short, cmd) {
			t.Errorf("default help does not list %q", cmd)
		}
		if !listsCommand(long, cmd) {
			t.Errorf("--agents help does not list %q", cmd)
		}
	}
	for _, cmd := range []string{"serve", "seed", "import", "match"} {
		if listsCommand(short, cmd) {
			t.Errorf("default help advertises %q, which is meant to be hidden", cmd)
		}
		if !listsCommand(long, cmd) {
			t.Errorf("--agents help does not list %q", cmd)
		}
	}
	// Hidden is not the same as undiscoverable: an agent has to be able to
	// find the long screen from the short one.
	if !strings.Contains(short, "--agents") {
		t.Error("default help does not say how to reach the full surface")
	}
}

func TestWantsAgents(t *testing.T) {
	for _, c := range []struct {
		args []string
		want bool
	}{
		{nil, false},
		{[]string{"--agents"}, true},
		{[]string{"-agents"}, true},
		{[]string{"search", "--agents"}, true},
		{[]string{"--json"}, false},
	} {
		if got := wantsAgents(c.args); got != c.want {
			t.Errorf("wantsAgents(%q) = %v, want %v", c.args, got, c.want)
		}
	}
}

// --- reading: search with a filter and no query ------------------------------

// readTestDB adds documents through add --ndjson, the way a person would write
// them by hand, and returns the database they went into.
func readTestDB(t *testing.T, docs ...tennis.Document) string {
	t.Helper()
	t.Setenv("TENNIS_CACHE", ndjsonTestCache(t))
	t.Setenv("TENNIS_CARDS", t.TempDir())
	t.Setenv("TENNIS_NS", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	dbPath := filepath.Join(t.TempDir(), "read.sqlite")

	var lines strings.Builder
	for _, d := range docs {
		b, err := json.Marshal(d)
		if err != nil {
			t.Fatal(err)
		}
		lines.Write(b)
		lines.WriteByte('\n')
	}
	withStdin(t, lines.String())
	if out, err := captureStdout(t, func() error {
		return cmdAdd([]string{"--db", dbPath, "--json", "--ndjson"})
	}); err != nil {
		t.Fatalf("add --ndjson: %v\noutput: %s", err, out)
	}
	return dbPath
}

// readFixture is two files and two sessions, every one dated so that oldest
// first is neither ID order nor path order, and a session with two subagents
// whose turns are numbered from zero like the main thread's. All of it carries
// batch=t, so one filter matches every unit.
func readFixture() []tennis.Document {
	doc := func(id, text string, attrs map[string]any) tennis.Document {
		attrs["batch"] = "t"
		return tennis.Document{ID: id, Text: text, Attributes: attrs}
	}
	file := func(path, text, modified string) tennis.Document {
		return doc(path, text, map[string]any{"kind": "file", "path": path, "modified": modified})
	}
	zz := func(id, role string, index int, created, text string, subagent string) tennis.Document {
		a := map[string]any{
			"source": "claude-code", "session": "zz", "title": "Rotating keys",
			"kind": "message", "role": role, "index": index, "created": created,
		}
		if subagent != "" {
			a["subagent"] = subagent
		}
		return doc(id, text, a)
	}
	aa := func(id, role string, index int, created, text string) tennis.Document {
		return doc(id, text, map[string]any{
			"source": "codex", "session": "aa", "kind": "message",
			"role": role, "index": index, "created": created,
		})
	}
	return []tennis.Document{
		file("/notes/alpha.md", "alpha notes", "2026-03-01T00:00:00Z"),
		file("/notes/zeta.md", "zeta notes\n", "2026-01-01T00:00:00Z"),
		aa("aa:1", "user", 0, "2026-02-15T09:00:00Z", "untitled question"),
		aa("aa:2", "assistant", 1, "2026-02-15T09:00:01Z", "untitled answer"),
		// The main thread's IDs sort against its index order.
		zz("zz:c", "user", 0, "2026-02-01T10:00:00Z", "how do I rotate keys", ""),
		zz("zz:b", "assistant", 1, "2026-02-01T10:00:03Z", "use the rotate command", ""),
		zz("zz:a", "assistant", 2, "2026-02-01T10:00:05Z", "then check the log", ""),
		// agent-b started first, so it comes first, against its name.
		zz("zz:sub-a:0", "user/subagent", 0, "2026-02-01T10:00:02Z", "read the docs", "agent-a"),
		zz("zz:sub-a:1", "assistant/subagent", 1, "2026-02-01T10:00:02Z", "weekly, says the docs", "agent-a"),
		zz("zz:sub-b:0", "user/subagent", 0, "2026-02-01T10:00:01Z", "find the callers", "agent-b"),
		zz("zz:sub-b:1", "assistant/subagent", 1, "2026-02-01T10:00:01Z", "three callers", "agent-b"),
	}
}

// readAll is what --where batch=t prints: every unit oldest first, each file
// headed as head(1) heads one of several, each session under its title or,
// untitled, its source and ID, and each subagent after the main thread.
const readAll = `==> /notes/zeta.md <==
zeta notes

# Rotating keys

## user

how do I rotate keys

## assistant

use the rotate command

then check the log

## subagent agent-b

## user/subagent

find the callers

## assistant/subagent

three callers

## subagent agent-a

## user/subagent

read the docs

## assistant/subagent

weekly, says the docs

# codex session aa

## user

untitled question

## assistant

untitled answer

==> /notes/alpha.md <==
alpha notes
`

// TestReadSeveralUnits: a filter that matches several files and sessions
// printed them joined by nothing but a blank line, so where one ended was
// guesswork; sessions came back in ID order, which for UUIDs is no order;
// and a session's subagents, numbered from zero like the main thread,
// were shuffled into it turn by turn.
func TestReadSeveralUnits(t *testing.T) {
	dbPath := readTestDB(t, readFixture()...)
	read := func(args ...string) string {
		t.Helper()
		out, err := captureStdout(t, func() error {
			return cmdSearch(append([]string{"--db", dbPath}, args...))
		})
		if err != nil {
			t.Fatalf("search %q: %v\noutput: %s", args, err, out)
		}
		return out
	}

	if got := read("--where", "batch=t"); got != readAll {
		t.Errorf("--where batch=t printed:\n%s\nwant:\n%s", got, readAll)
	}

	// --json is the same documents in the same order.
	var docs []tennis.Document
	if err := json.Unmarshal([]byte(read("--where", "batch=t", "--json")), &docs); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for _, d := range docs {
		ids = append(ids, d.ID)
	}
	want := []string{
		"/notes/zeta.md", "zz:c", "zz:b", "zz:a", "zz:sub-b:0", "zz:sub-b:1",
		"zz:sub-a:0", "zz:sub-a:1", "aa:1", "aa:2", "/notes/alpha.md",
	}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("--json order:\n got %v\nwant %v", ids, want)
	}

	// One unit is what a card's command asks for, and it prints as it always
	// has: a file as its text, a session under its title if it has one, and
	// no heading telling it apart from nothing.
	for _, c := range []struct{ where, want string }{
		{"path=/notes/zeta.md", "zeta notes\n"},
		{"session=aa", "## user\n\nuntitled question\n\n## assistant\n\nuntitled answer\n"},
		{"session=zz,role=user", "# Rotating keys\n\n## user\n\nhow do I rotate keys\n"},
	} {
		if got := read("--where", c.where); got != c.want {
			t.Errorf("--where %s printed:\n%q\nwant:\n%q", c.where, got, c.want)
		}
	}

	// -k caps what is printed, and the headings go with what is printed: one
	// unit alone has none. -k 0 is no cap, as ls -n 0 is.
	if got := read("--where", "batch=t", "-k", "1"); got != "zeta notes\n" {
		t.Errorf("-k 1 printed %q, want the oldest file alone", got)
	}
	if got, want := read("--where", "batch=t", "-k", "2"),
		"==> /notes/zeta.md <==\nzeta notes\n\n# Rotating keys\n\n## user\n\nhow do I rotate keys\n"; got != want {
		t.Errorf("-k 2 printed:\n%q\nwant:\n%q", got, want)
	}
	if got := read("--where", "batch=t", "-k", "0"); got != readAll {
		t.Errorf("-k 0 printed:\n%s\nwant every match:\n%s", got, readAll)
	}
	if got := read("--where", "batch=t", "--mode", "hybrid"); got != readAll {
		t.Errorf("--mode hybrid, the default, should read as before; printed:\n%s", got)
	}
}

// TestSearchFlagChecks: a negative -k and an unknown --mode were taken
// without complaint, and the mode was not even looked at without a query.
// Each is an error now, in both modes, and so is a ranker named when there
// is nothing to rank.
func TestSearchFlagChecks(t *testing.T) {
	dbPath := readTestDB(t, readFixture()...)
	run := func(cmd func([]string) error, args ...string) error {
		t.Helper()
		_, err := captureStdout(t, func() error { return cmd(append([]string{"--db", dbPath}, args...)) })
		return err
	}
	for _, c := range []struct {
		name string
		cmd  func([]string) error
		args []string
		want string // in the error; empty for none
	}{
		{"negative -k reading", cmdSearch, []string{"--where", "batch=t", "-k", "-1"}, "-k cannot be negative"},
		{"negative -n searching", cmdSearch, []string{"rotate", "-n", "-2"}, "-k cannot be negative"},
		{"negative -k in match", cmdMatch, []string{defaultNamespace, "--where", "batch=t", "-k", "-1"}, "-k cannot be negative"},
		{"unknown mode reading", cmdSearch, []string{"--where", "batch=t", "--mode", "bogus"}, `not "bogus"`},
		{"unknown mode searching", cmdSearch, []string{"rotate", "--mode", "bogus"}, `not "bogus"`},
		{"keyword with nothing to rank", cmdSearch, []string{"--where", "batch=t", "--mode", "keyword"}, "--mode keyword needs a query"},
		{"semantic with nothing to rank", cmdMatch, []string{defaultNamespace, "--where", "batch=t", "--mode", "semantic"}, "--mode semantic needs a query"},
		{"keyword with a query", cmdSearch, []string{"rotate", "--mode", "keyword"}, ""},
		{"-k 0 with a query", cmdSearch, []string{"rotate", "-k", "0"}, ""},
	} {
		err := run(c.cmd, c.args...)
		switch {
		case c.want == "" && err != nil:
			t.Errorf("%s: %v", c.name, err)
		case c.want != "" && (err == nil || !strings.Contains(err.Error(), c.want)):
			t.Errorf("%s: got %v, want an error saying %q", c.name, err, c.want)
		}
	}
}

// TestReadOrderTies covers what the fixture above does not: subagents that
// started together go by name, an undated unit follows the dated ones, and a
// date written as Unix seconds is ordered among RFC3339 ones by when it is,
// not by its type.
func TestReadOrderTies(t *testing.T) {
	info := func(id string, attrs map[string]any) tennis.DocumentInfo {
		return tennis.DocumentInfo{ID: id, Attributes: attrs}
	}
	at := "2026-05-01T00:00:00Z"
	got := readOrder([]tennis.DocumentInfo{
		info("undated", map[string]any{}),
		info("s:sub-y", map[string]any{"session": "s", "subagent": "agent-y", "index": 0.0, "created": at}),
		info("s:sub-x", map[string]any{"session": "s", "subagent": "agent-x", "index": 0.0, "created": at}),
		info("s:main", map[string]any{"session": "s", "index": 0.0, "created": "2026-05-01T00:00:09Z"}),
		info("later", map[string]any{"modified": "2026-06-01T00:00:00Z"}),
		info("epoch", map[string]any{"session": "e", "created": float64(time.Date(2026, 4, 1, 0, 0, 0, 0, time.UTC).Unix())}),
	})
	var ids []string
	for _, g := range got {
		ids = append(ids, g.ID)
	}
	want := []string{"epoch", "s:main", "s:sub-x", "s:sub-y", "later", "undated"}
	if !reflect.DeepEqual(ids, want) {
		t.Errorf("readOrder:\n got %v\nwant %v", ids, want)
	}
}
