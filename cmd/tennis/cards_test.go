package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
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

	if err := writeCard(dir, c.card(), "The retry loop never exited because the backoff was not reset."); err != nil {
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
	if err := writeCard(dir, c.card(), "one"); err != nil {
		t.Fatal(err)
	}
	c.title = "The eventual title"
	if err := writeCard(dir, c.card(), "two"); err != nil {
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
	if err := writeCard(dir, c.card(), "s"); err != nil {
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
	if err := writeCard(dir, c, "A monthly budget with rent and utilities."); err != nil {
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
