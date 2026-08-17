package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

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

	if err := writeCard(dir, c, "The retry loop never exited because the backoff was not reset."); err != nil {
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
	if err := writeCard(dir, c, "one"); err != nil {
		t.Fatal(err)
	}
	c.title = "The eventual title"
	if err := writeCard(dir, c, "two"); err != nil {
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
	if err := writeCard(dir, c, "s"); err != nil {
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
	w.add(conv("A chat", user("how do I rotate the signing key")))
	w.close()

	if w.written != 1 {
		t.Fatalf("wrote %d cards, want 1 despite the summarizer failing", w.written)
	}
	if w.failed != 1 {
		t.Errorf("counted %d summary failures, want 1", w.failed)
	}
	entries, _ := os.ReadDir(filepath.Join(dir, "sessions"))
	if len(entries) != 1 {
		t.Fatalf("found %d cards on disk, want 1", len(entries))
	}
	body, _ := os.ReadFile(filepath.Join(dir, "sessions", entries[0].Name()))
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

type failingSummarizer struct{}

func (failingSummarizer) Provider() string { return "test:always-fails" }
func (failingSummarizer) Summarize(_ context.Context, _ summarize.Input) (string, error) {
	return "", errors.New("rate limited")
}
