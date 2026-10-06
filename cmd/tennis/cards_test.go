package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/satoricorp/tennis/summarize"
)

func conv(title string, turns ...turn) conversation {
	return conversation{
		source: "claude-code", id: "s1", title: title,
		create: "2026-08-16T14:32:05Z",
		extra:  map[string]any{"project": "/Users/joe/git/tennis"},
		turns:  turns,
	}
}

func user(text string) turn      { return turn{id: "u", role: "user", text: text} }
func assistant(text string) turn { return turn{id: "a", role: "assistant", text: text} }

// TestCardCarriesFrontmatterAndPointer: a card has to be readable on its own
// and say how to get the rest, since tennis never reads it back.
func TestCardCarriesFrontmatterAndPointer(t *testing.T) {
	dir := t.TempDir()
	c := conv("Bug: retry loop never exits", user("why does this hang"), assistant("the backoff never resets"))

	if err := writeCard(dir, c.card(), "The retry loop never exited because the backoff was not reset.", ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("wrote %d files, want 1", len(entries))
	}
	body, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	got := string(body)

	// A title with a colon unquoted turns one YAML field into a nested map.
	if !strings.Contains(got, `title: "Bug: retry loop never exits"`) {
		t.Errorf("title with a colon was not quoted:\n%s", got)
	}
	for _, want := range []string{
		`session: "claude-code:s1"`, "source: claude-code", "turns: 2",
		"project: /Users/joe/git/tennis", "created: 2026-08-16T14:32:05Z",
		"tennis search --where 'session=s1'",
		"The retry loop never exited",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("card is missing %q:\n%s", want, got)
		}
	}
}

// TestCardSweepsStaleTitle: Claude Code names a session several turns in, so
// importing twice can produce two slugs for one conversation. Without the
// sweep the archive grows a second card every time a session gets retitled.
func TestCardSweepsStaleTitle(t *testing.T) {
	dir := t.TempDir()
	c := conv("Draft title", user("hello"))
	if err := writeCard(dir, c.card(), "one", ""); err != nil {
		t.Fatal(err)
	}
	c.title = "The eventual title"
	if err := writeCard(dir, c.card(), "two", ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("one conversation left %d cards: %v", len(entries), names)
	}
	if !strings.Contains(entries[0].Name(), "the-eventual-title") {
		t.Errorf("surviving card is %q, want the current title", entries[0].Name())
	}
}

// TestCardNonLatinTitle: a title that slugs to nothing must still produce an
// identifiable filename rather than a bare timestamp.
func TestCardNonLatinTitle(t *testing.T) {
	dir := t.TempDir()
	c := conv("日本語のタイトル", user("hello"))
	c.id = "abc123"
	if err := writeCard(dir, c.card(), "s", ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if !strings.Contains(entries[0].Name(), "abc123") {
		t.Errorf("filename %q should fall back to the conversation id", entries[0].Name())
	}
}

// TestTranscriptMergesRuns: an agent session is mostly runs of assistant turns,
// and a heading per turn produces a transcript that is largely headings — which
// wastes the summarizer's context on formatting.
func TestTranscriptMergesRuns(t *testing.T) {
	c := conv("x",
		user("fix the retry loop"),
		assistant("Looking at it."),
		assistant("Found it."),
		assistant("Fixed the backoff."),
		user("thanks"),
	)
	got := c.transcript()
	if n := strings.Count(got, "## assistant"); n != 1 {
		t.Errorf("got %d assistant headings, want 1 — consecutive turns should merge\n%s", n, got)
	}
	if n := strings.Count(got, "## user"); n != 2 {
		t.Errorf("got %d user headings, want 2", n)
	}
	for _, want := range []string{"Looking at it.", "Found it.", "Fixed the backoff."} {
		if !strings.Contains(got, want) {
			t.Errorf("prose from a merged run was dropped: %q missing", want)
		}
	}
}

// TestFirstUserTurnSkipsAssistantOpeners: the fallback summary is the opening
// *user* message, which is not always the first turn in the file.
func TestFirstUserTurnSkipsAssistantOpeners(t *testing.T) {
	c := conv("x", assistant("I'll start."), user("explain the wash sale rule"))
	if got, want := firstUserTurn(c), "explain the wash sale rule"; got != want {
		t.Errorf("firstUserTurn = %q, want %q", got, want)
	}
	if got := firstUserTurn(conv("x", assistant("only me"))); got != "" {
		t.Errorf("firstUserTurn with no user turn = %q, want empty", got)
	}
}

// TestCardWriterDegradesOnSummaryFailure is the property that makes a large
// import survivable: a failing summarizer costs each card its prose, not the
// card, and not the import.
func TestCardWriterDegradesOnSummaryFailure(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("TENNIS_CARDS", dir)

	w, err := newCardWriter(t.Context(), dir, failingSummarizer{})
	if err != nil {
		t.Fatal(err)
	}
	w.add(conv("A chat", user("how do I rotate the signing key")).card())
	w.close()

	if w.written != 1 {
		t.Fatalf("wrote %d cards, want 1 despite the summarizer failing", w.written)
	}
	if w.failed != 1 {
		t.Errorf("counted %d summary failures, want 1", w.failed)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("found %d cards on disk, want 1", len(entries))
	}
	body, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if !strings.Contains(string(body), "how do I rotate the signing key") {
		t.Errorf("card did not fall back to the opening message:\n%s", body)
	}
}

// TestCardWriterCloseIsIdempotent: close runs at the end of an import and again
// from a deferred safety net, and closing a channel twice panics.
func TestCardWriterCloseIsIdempotent(t *testing.T) {
	w, err := newCardWriter(t.Context(), t.TempDir(), nil)
	if err != nil {
		t.Fatal(err)
	}
	w.close()
	w.close() // must not panic
}

// TestFileCardIsNamedByPath: a file's card has to land on the same name after
// every edit, or re-adding a folder grows a second card per touched file; and
// two files that share a name in different folders must not share a card.
func TestFileCardIsNamedByPath(t *testing.T) {
	first := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	a := fileCard("/Users/joe/notes/budget.xlsx", "budget.xlsx", "x", first, 10)
	edited := fileCard("/Users/joe/notes/budget.xlsx", "budget.xlsx", "y", first.AddDate(0, 3, 0), 12)
	sameName := fileCard("/Users/joe/other/budget.xlsx", "budget.xlsx", "x", first, 10)

	stem, prefix := cardStem(a)
	if got, _ := cardStem(edited); got != stem {
		t.Errorf("editing the file renamed its card: %q then %q", stem, got)
	}
	if got, _ := cardStem(sameName); got == stem {
		t.Errorf("two files named budget.xlsx share the card %q", stem)
	}
	if !strings.HasPrefix(stem, "budget-xlsx-") {
		t.Errorf("stem %q should start with the file's name", stem)
	}
	if prefix != "" {
		t.Errorf("a file card has nothing to sweep, got prefix %q", prefix)
	}
	if got, _ := cardStem(fileCard("/x/日本語", "日本語", "x", first, 1)); !strings.HasPrefix(got, "file-") {
		t.Errorf("a name that slugs to nothing should still produce a stem: %q", got)
	}
}

// TestFileCardFrontmatter: the card says what and where the file is, and how
// to get its text back out of tennis — and carries none of a thread's fields.
func TestFileCardFrontmatter(t *testing.T) {
	dir := t.TempDir()
	c := fileCard("/Users/joe/notes/budget.xlsx", "budget.xlsx", "## Budget\n\nRent\t1200",
		time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), 2048)
	if err := writeCard(dir, c, "A monthly budget with rent and utilities.", ""); err != nil {
		t.Fatal(err)
	}
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("wrote %d files, want 1", len(entries))
	}
	body, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	got := string(body)
	for _, want := range []string{
		"file: /Users/joe/notes/budget.xlsx", "source: file", "title: budget.xlsx",
		"modified: 2026-01-02T03:04:05Z", "size: 2048",
		"# budget.xlsx", "A monthly budget with rent and utilities.",
		"tennis search --where 'path=/Users/joe/notes/budget.xlsx'",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("card is missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "turns:") || strings.Contains(got, "session:") {
		t.Errorf("a file card carried a thread's fields:\n%s", got)
	}
}

// TestCardWriterSummarizesFilesAsFiles: the summarizer is told it is looking
// at a file, and where, so it asks the right question of it.
func TestCardWriterSummarizesFilesAsFiles(t *testing.T) {
	dir := t.TempDir()
	rec := &recordingSummarizer{}
	w, err := newCardWriter(t.Context(), dir, rec)
	if err != nil {
		t.Fatal(err)
	}
	w.add(fileCard("/n/auth.md", "auth.md", "# Session handling\nkeep the user signed in", time.Time{}, 40))
	w.add(conv("A chat", user("how do I rotate the signing key")).card())
	w.close()

	if len(rec.seen) != 2 {
		t.Fatalf("summarizer saw %d inputs, want 2", len(rec.seen))
	}
	var file, chat summarize.Input
	for _, in := range rec.seen {
		if in.Kind == summarize.KindFile {
			file = in
		} else {
			chat = in
		}
	}
	if file.Title != "auth.md" || file.Path != "/n/auth.md" || !strings.Contains(file.Transcript, "keep the user signed in") {
		t.Errorf("file input was not described as the file: %+v", file)
	}
	if chat.Kind != summarize.KindConversation || chat.Turns != 1 || chat.Source != "claude-code" {
		t.Errorf("conversation input changed shape: %+v", chat)
	}
	body, _ := os.ReadFile(filepath.Join(dir, "auth-md-"+shortHash("/n/auth.md")+".md"))
	if !strings.Contains(string(body), "recorded") {
		t.Errorf("file card did not carry the summary:\n%s", body)
	}
}

// TestCardWriterFallsBackToFileOpening: with no model, a file's card opens
// with the start of the file, line by line, so a spreadsheet's rows read as
// rows rather than one run-on line.
func TestCardWriterFallsBackToFileOpening(t *testing.T) {
	dir := t.TempDir()
	w, err := newCardWriter(t.Context(), dir, nil)
	if err != nil {
		t.Fatal(err)
	}
	w.add(fileCard("/n/expenses.csv", "expenses.csv", "date,amount\n2026-09-01,842.50\n2026-09-03,35.00\n", time.Time{}, 40))
	w.close()
	entries, _ := os.ReadDir(dir)
	if len(entries) != 1 {
		t.Fatalf("found %d cards on disk, want 1", len(entries))
	}
	body, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
	if !strings.Contains(string(body), "```\ndate,amount\n2026-09-01,842.50\n2026-09-03,35.00\n```") {
		t.Errorf("card did not carry the file's opening lines as lines:\n%s", body)
	}
}

// TestExcerptIsBounded: the fallback has to stay card-sized whatever the
// file is — a sheet of short cells, a minified one-liner, or an empty file.
func TestExcerptIsBounded(t *testing.T) {
	tall := strings.Repeat("row\n", 40)
	got := excerpt(tall)
	if n := strings.Count(got, "\n"); n > 15 {
		t.Errorf("a tall file produced %d lines of excerpt:\n%s", n, got)
	}
	if !strings.HasSuffix(got, "…\n```") {
		t.Errorf("a cut excerpt should end with an ellipsis:\n%s", got)
	}
	wide := strings.Repeat("x", 500)
	if got := excerpt(wide); len(got) > 200 {
		t.Errorf("a wide line was not cut: %d bytes", len(got))
	}
	if got := excerpt("  \n\n "); got != "" {
		t.Errorf("an empty file produced an excerpt: %q", got)
	}
	if got := excerpt("one\ntwo"); strings.Contains(got, "…") {
		t.Errorf("a short file was marked as cut:\n%s", got)
	}
}

// addCards runs one source through the import the way `tennis add` does, with
// sum writing the cards into dir, and returns the writer for its counts. Each
// call is a fresh writer, as each add is a fresh process.
func addCards(t *testing.T, src, format string, sum summarize.Summarizer, dir string) *cardWriter {
	t.Helper()
	w, err := newCardWriter(t.Context(), dir, sum)
	if err != nil {
		t.Fatal(err)
	}
	sink := &docSink{capture: func(string, string, map[string]any) {}, cards: w}
	if _, err := importPath(src, format, perTurn, defaultExt, sink, func(string) {}, true); err != nil {
		t.Fatalf("importPath(%s): %v", src, err)
	}
	w.close()
	return w
}

// TestReaddSummarizesOnlyWhatChanged: re-adding ~/.claude is hundreds of
// sessions that have not changed, and with a key set every card written again
// is a model call that says what the card already says. Only a changed file,
// or a card that is no longer there, is worth one.
func TestReaddSummarizesOnlyWhatChanged(t *testing.T) {
	cards := t.TempDir()
	notes := t.TempDir()
	for name, body := range map[string]string{
		"auth.md":   "# Session handling\nkeep the user signed in",
		"budget.md": "rent 1200, utilities 140",
	} {
		if err := os.WriteFile(filepath.Join(notes, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	session := writeZip(t, "codex.zip", map[string]string{"sessions/rollout-S9.jsonl": codexSession})

	add := func(src, format string) (*cardWriter, *recordingSummarizer) {
		t.Helper()
		rec := &recordingSummarizer{}
		return addCards(t, src, format, rec, cards), rec
	}

	if w, rec := add(notes, formatFiles); len(rec.seen) != 2 || w.written != 2 {
		t.Fatalf("first add: %d summaries, %d cards; want 2 and 2", len(rec.seen), w.written)
	}
	if w, rec := add(session, formatAuto); len(rec.seen) != 1 || w.written != 1 {
		t.Fatalf("first add of a session: %d summaries, %d cards; want 1 and 1", len(rec.seen), w.written)
	}

	// The same files and the same session again: nothing to say.
	if w, rec := add(notes, formatFiles); len(rec.seen) != 0 || w.written != 0 || w.unchanged != 2 {
		t.Errorf("identical re-add: %d summaries, %d written, %d unchanged; want 0, 0, 2", len(rec.seen), w.written, w.unchanged)
	}
	if w, rec := add(session, formatAuto); len(rec.seen) != 0 || w.written != 0 || w.unchanged != 1 {
		t.Errorf("identical session re-add: %d summaries, %d written, %d unchanged; want 0, 0, 1", len(rec.seen), w.written, w.unchanged)
	}

	// One file changes: one summary, of that file.
	if err := os.WriteFile(filepath.Join(notes, "budget.md"), []byte("rent 1250, utilities 140"), 0o644); err != nil {
		t.Fatal(err)
	}
	w, rec := add(notes, formatFiles)
	if len(rec.seen) != 1 || w.written != 1 || w.unchanged != 1 {
		t.Fatalf("re-add after one edit: %d summaries, %d written, %d unchanged; want 1, 1, 1", len(rec.seen), w.written, w.unchanged)
	}
	if !strings.Contains(rec.seen[0].Transcript, "rent 1250") {
		t.Errorf("summarized the wrong file: %+v", rec.seen[0])
	}

	// A card the person deleted comes back, and one they wrote in is left
	// alone while what it describes is unchanged.
	auth := filepath.Join(cards, "auth-md-"+shortHash(filepath.Join(notes, "auth.md"))+".md")
	budget := filepath.Join(cards, "budget-md-"+shortHash(filepath.Join(notes, "budget.md"))+".md")
	if err := os.Remove(auth); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(budget)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(budget, append(body, "\nmy note: ask about the deposit\n"...), 0o644); err != nil {
		t.Fatal(err)
	}
	if w, rec := add(notes, formatFiles); len(rec.seen) != 1 || w.written != 1 {
		t.Errorf("re-add after deleting a card: %d summaries, %d written; want 1 and 1", len(rec.seen), w.written)
	}
	if _, err := os.Stat(auth); err != nil {
		t.Errorf("the deleted card was not written again: %v", err)
	}
	if body, _ := os.ReadFile(budget); !strings.Contains(string(body), "ask about the deposit") {
		t.Errorf("an unchanged file's card lost the person's note:\n%s", body)
	}
}

// TestCardWriterRetriesWhatFellBack: a card that carries the opening because
// there was no key, or because its summary failed, is summarized on the next
// import that can — and a run with no key never trades a summary an earlier
// run paid for back for the opening.
func TestCardWriterRetriesWhatFellBack(t *testing.T) {
	dir := t.TempDir()
	c := conv("A chat", user("how do I rotate the signing key"), assistant("with kid headers"))
	run := func(sum summarize.Summarizer) *cardWriter {
		t.Helper()
		w, err := newCardWriter(t.Context(), dir, sum)
		if err != nil {
			t.Fatal(err)
		}
		w.add(c.card())
		w.close()
		return w
	}
	read := func() string {
		t.Helper()
		entries, _ := os.ReadDir(dir)
		if len(entries) != 1 {
			t.Fatalf("found %d cards on disk, want 1", len(entries))
		}
		body, _ := os.ReadFile(filepath.Join(dir, entries[0].Name()))
		return string(body)
	}

	run(nil)
	if got := read(); strings.Contains(got, "summarizer:") {
		t.Fatalf("a card with no summary claims a summarizer:\n%s", got)
	}
	if w := run(nil); w.written != 0 {
		t.Errorf("with still no key, an unchanged card was written again")
	}
	if w := run(failingSummarizer{}); w.written != 1 || w.failed != 1 {
		t.Errorf("a key appeared: %d written, %d failed; want the card tried (1, 1)", w.written, w.failed)
	}

	rec := &recordingSummarizer{}
	if w := run(rec); len(rec.seen) != 1 || w.written != 1 {
		t.Errorf("after a failed summary: %d summaries, %d written; want the card tried again (1, 1)", len(rec.seen), w.written)
	}
	got := read()
	if !strings.Contains(got, "recorded") || !strings.Contains(got, `summarizer: "test:recording"`) {
		t.Fatalf("the retried card does not carry its summary:\n%s", got)
	}

	if w := run(nil); w.written != 0 || w.unchanged != 1 {
		t.Errorf("a run with no key rewrote a summarized card: %d written, %d unchanged", w.written, w.unchanged)
	}
	if w := run(&recordingSummarizer{}); w.written != 0 {
		t.Errorf("a summarized, unchanged card was summarized again")
	}
	if !strings.Contains(read(), "recorded") {
		t.Errorf("the summary did not survive:\n%s", read())
	}
}

type recordingSummarizer struct {
	mu   sync.Mutex
	seen []summarize.Input
}

func (r *recordingSummarizer) Provider() string { return "test:recording" }
func (r *recordingSummarizer) Summarize(_ context.Context, in summarize.Input) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seen = append(r.seen, in)
	return "recorded", nil
}

type failingSummarizer struct{}

func (failingSummarizer) Provider() string { return "test:always-fails" }
func (failingSummarizer) Summarize(_ context.Context, _ summarize.Input) (string, error) {
	return "", errors.New("rate limited")
}

// TestCardPointerRuns: the last line of every card is a command, and it has to
// work. It is taken off real cards from a real import, split into words by sh
// exactly as a person pasting it would have it split, and run with nothing
// added — the database comes from $TENNIS_DB, so not even a flag. A nil error
// is what makes main exit 0.
//
// What it prints has to be the rest of the record, not a ranked fragment of
// it: the whole conversation, every turn in the order it was said, and the
// whole file, which here is long enough to be several chunks and too long for
// its card to carry.
func TestCardPointerRuns(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_NS", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	cardDir := t.TempDir()
	t.Setenv("TENNIS_CARDS", cardDir)
	t.Setenv("TENNIS_DB", filepath.Join(t.TempDir(), "cards.sqlite"))

	var note strings.Builder
	note.WriteString("# Tulum trip\n\n")
	for i := 1; i <= 40; i++ {
		fmt.Fprintf(&note, "Day %d: walked to the cenote, then lunch near Hotel Esencia.\n", i)
	}
	notes := t.TempDir()
	// A space in the name, so the quoting on the card is part of what is tested.
	if err := os.WriteFile(filepath.Join(notes, "trip notes.md"), []byte(note.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	session := writeZip(t, "sessions.zip", map[string]string{
		"projects/-Users-joe-git-tennis/S1.jsonl": claudeCodeSession,
	})
	for _, args := range [][]string{{"--claude-code", session}, {"--files", notes}} {
		if _, err := captureStderr(t, func() error {
			_, err := captureStdout(t, func() error { return cmdAdd(args) })
			return err
		}); err != nil {
			t.Fatalf("add %v: %v", args, err)
		}
	}

	entries, err := os.ReadDir(cardDir)
	if err != nil || len(entries) != 2 {
		t.Fatalf("want a card for the session and one for the file, got %v (%v)", entries, err)
	}
	pointer := regexp.MustCompile("`(tennis [^`]+)`\\s*$")
	for _, e := range entries {
		body, err := os.ReadFile(filepath.Join(cardDir, e.Name()))
		if err != nil {
			t.Fatal(err)
		}
		m := pointer.FindStringSubmatch(string(body))
		if m == nil {
			t.Errorf("%s does not end in a command:\n%s", e.Name(), body)
			continue
		}
		words, err := exec.Command("sh", "-c", `printf '%s\0' `+m[1]).Output()
		if err != nil {
			t.Fatalf("sh could not split %q: %v", m[1], err)
		}
		argv := strings.Split(strings.TrimSuffix(string(words), "\x00"), "\x00")
		if len(argv) < 2 || argv[0] != "tennis" || argv[1] != "search" {
			t.Fatalf("%s points at %q, which this test does not know how to run", e.Name(), m[1])
		}
		out, err := captureStdout(t, func() error { return cmdSearch(argv[2:]) })
		if err != nil {
			t.Errorf("%s: `%s` failed: %v", e.Name(), m[1], err)
			continue
		}

		if strings.Contains(string(body), "\nsource: file\n") {
			if strings.TrimSpace(out) != strings.TrimSpace(note.String()) {
				t.Errorf("`%s` did not print the file as it was:\n%s", m[1], out)
			}
			continue
		}
		// The title, then every turn in index order. The documents' IDs sort
		// differently (claude-code:S1:a1 before :u1), so ID order would fail.
		want := []string{
			"# Fixing the flaky auth test",
			"## summary\n\nFixing the flaky auth test",
			"## user\n\nthe auth test is flaky",
			"## assistant\n\nlook at the token refresh window",
			"## assistant/subagent\n\nsubagent found the race in the clock",
		}
		at := 0
		for _, w := range want {
			i := strings.Index(out[at:], w)
			if i < 0 {
				t.Errorf("`%s` is missing %q after byte %d:\n%s", m[1], w, at, out)
				break
			}
			at += i + len(w)
		}
	}
}
