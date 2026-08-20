package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/satoricorp/tennis/summarize"
)

// A card is a readable markdown summary of one conversation, written to
// ~/tennis as it is imported.
//
// tennis writes cards and never reads them back. The documents in SQLite are
// the record; a card is a rendering of one. That one-way rule is what makes
// them safe to edit, move, delete, or paste into another tool without
// corrupting the archive — and it is why they are not indexed. Indexing them
// would rank a conversation twice, once as its short on-topic summary and once
// as the long transcript that actually holds the answer, and the summary would
// usually win.

// defaultCardDir is where cards go. It honors TENNIS_CARDS the way defaultDB
// honors TENNIS_DB, so tests and sandboxes can redirect it — without that, a
// test that runs an import writes into the user's real home.
const defaultCardRoot = "~/tennis"

func defaultCardDir() string {
	if p := os.Getenv("TENNIS_CARDS"); p != "" {
		return p
	}
	return defaultCardRoot
}

// cardConcurrency is how many summaries are in flight at once. Summarizing is
// the slow part of an import by orders of magnitude — a first run is hundreds
// of API calls against seconds of parsing — but the rate limit is on the other
// end, so this stays polite.
const cardConcurrency = 4

// cardWriter turns conversations into cards in the background while the import
// keeps reading. Conversations arrive one at a time from the readers; doing the
// API call inline would serialize hundreds of round trips behind a parser that
// is already finished.
type cardWriter struct {
	dir string
	sum summarize.Summarizer

	work chan conversation
	wg   sync.WaitGroup
	ctx  context.Context

	// close runs both at the end of a successful import and from a deferred
	// safety net covering the error returns between here and there. Closing a
	// channel twice panics, so which one gets there first must not matter.
	once sync.Once

	mu      sync.Mutex
	written int
	failed  int
}

// newCardWriter starts the workers. A nil Summarizer is allowed: cards are
// still written, carrying the opening message instead of prose.
func newCardWriter(ctx context.Context, dir string, sum summarize.Summarizer) (*cardWriter, error) {
	expanded, err := expandHome(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(expanded, 0o755); err != nil {
		return nil, err
	}

	w := &cardWriter{dir: expanded, sum: sum, ctx: ctx, work: make(chan conversation)}
	for i := 0; i < cardConcurrency; i++ {
		w.wg.Add(1)
		go func() {
			defer w.wg.Done()
			for c := range w.work {
				w.one(c)
			}
		}()
	}
	return w, nil
}

func (w *cardWriter) add(c conversation) {
	if w == nil || len(c.turns) == 0 {
		return
	}
	w.work <- c
}

// close waits for every queued card. Called before the import reports, so the
// counts it prints are true, and again from a defer in case it never got there.
func (w *cardWriter) close() {
	if w == nil {
		return
	}
	w.once.Do(func() {
		close(w.work)
		w.wg.Wait()
	})
}

// one summarizes and writes a single card.
//
// A failed summary costs that card its prose and nothing else. An import is
// hundreds of calls, and a refusal, timeout, or rate limit at number 300 must
// not discard the 299 already written — so every failure degrades to the
// opening message and is counted.
func (w *cardWriter) one(c conversation) {
	text := summarize.Fallback(firstUserTurn(c))
	if w.sum != nil {
		got, err := w.sum.Summarize(w.ctx, summarize.Input{
			Source:     c.source,
			Title:      c.title,
			Project:    attrText(c.extra, "project", "cwd"),
			Turns:      len(c.turns),
			Transcript: c.transcript(),
		})
		switch {
		case err == nil && strings.TrimSpace(got) != "":
			text = got
		case err != nil:
			w.mu.Lock()
			w.failed++
			w.mu.Unlock()
			fmt.Fprintf(os.Stderr, "tennis: summarizing %s: %v\n", c.id, err)
		}
	}
	if err := writeCard(w.dir, c, text); err != nil {
		fmt.Fprintf(os.Stderr, "tennis: card for %s: %v\n", c.id, err)
		return
	}
	w.mu.Lock()
	w.written++
	w.mu.Unlock()
}

// transcript renders the conversation for the summarizer. Consecutive turns
// from the same speaker are merged: an agent session is mostly runs of
// assistant turns, and a heading per turn produces a transcript that is largely
// headings.
func (c conversation) transcript() string {
	var b strings.Builder
	for i := 0; i < len(c.turns); {
		role := c.turns[i].role
		var texts []string
		j := i
		for ; j < len(c.turns) && c.turns[j].role == role; j++ {
			if t := strings.TrimSpace(c.turns[j].text); t != "" {
				texts = append(texts, t)
			}
		}
		i = j
		if len(texts) == 0 {
			continue
		}
		fmt.Fprintf(&b, "## %s\n\n%s\n\n", role, strings.Join(texts, "\n\n"))
	}
	return strings.TrimSpace(b.String())
}

func firstUserTurn(c conversation) string {
	for _, t := range c.turns {
		if t.role == "user" || t.role == "human" {
			if s := strings.TrimSpace(t.text); s != "" {
				return s
			}
		}
	}
	return ""
}

func attrText(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if s, ok := m[k].(string); ok && s != "" {
			return s
		}
	}
	return ""
}

// writeCard writes or replaces the card for a conversation.
func writeCard(dir string, c conversation, summary string) error {
	stem, prefix := cardStem(c)
	path := filepath.Join(dir, stem+".md")

	// Sweep cards from an earlier import of this same conversation whose title
	// — and therefore slug — has since changed. Claude Code names a session
	// several turns in, so importing twice can produce two different names for
	// one conversation. The timestamp-and-source prefix is stable.
	if stale, _ := filepath.Glob(filepath.Join(dir, prefix+"*.md")); len(stale) > 0 {
		for _, s := range stale {
			if s != path {
				os.Remove(s)
			}
		}
	}
	return os.WriteFile(path, []byte(renderCard(c, summary)), 0o644)
}

// cardStem is the filename without extension, plus the stable prefix used to
// find earlier names for the same conversation. The volatile part — the title
// slug — has to come last for that sweep to work.
func cardStem(c conversation) (stem, prefix string) {
	ts := time.Now()
	if t, err := time.Parse(time.RFC3339, c.create); err == nil {
		ts = t
	} else if t := earliestTurn(c); !t.IsZero() {
		ts = t
	}
	prefix = ts.UTC().Format("2006-01-02-150405") + "-" + slug(c.source, 20) + "-"

	title := c.title
	if title == "" {
		title = firstLine(firstUserTurn(c), 80)
	}
	if s := slug(title, 60); s != "" {
		return prefix + s, prefix
	}
	// A title that slugs to nothing — non-Latin, or all punctuation — falls
	// back to the conversation id so the file is still identifiable on disk.
	return prefix + slug(c.id, 24), prefix
}

func earliestTurn(c conversation) time.Time {
	var out time.Time
	for _, t := range c.turns {
		parsed, err := time.Parse(time.RFC3339, t.created)
		if err != nil {
			continue
		}
		if out.IsZero() || parsed.Before(out) {
			out = parsed
		}
	}
	return out
}

// renderCard builds the markdown. The document ID prefix is in the frontmatter
// and the retrieval command is in the body, because the card's whole job is to
// be readable on its own and to say how to get the rest.
func renderCard(c conversation, summary string) string {
	title := c.title
	if title == "" {
		title = firstLine(firstUserTurn(c), 80)
	}
	if title == "" {
		title = "untitled"
	}

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "session: %s\n", yamlString(c.source+":"+c.id))
	fmt.Fprintf(&b, "source: %s\n", c.source)
	fmt.Fprintf(&b, "title: %s\n", yamlString(title))
	if c.create != "" {
		fmt.Fprintf(&b, "created: %s\n", c.create)
	}
	fmt.Fprintf(&b, "turns: %d\n", len(c.turns))
	for _, k := range []string{"project", "cwd", "branch", "model"} {
		if v := attrText(c.extra, k); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, yamlString(v))
		}
	}
	b.WriteString("---\n\n")

	fmt.Fprintf(&b, "# %s\n\n", title)
	if s := strings.TrimSpace(summary); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	fmt.Fprintf(&b, "Full conversation: `tennis search --where 'session=%s'`\n", c.id)
	return b.String()
}

// yamlString quotes a scalar when it would otherwise be misparsed. Titles are
// arbitrary user text and routinely contain colons ("Bug: retry loop"), which
// unquoted would turn one field into a nested map.
func yamlString(s string) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if s == "" {
		return `""`
	}
	if strings.ContainsAny(s, `:#{}[]&*!|>'"%@`+"`") || strings.TrimSpace(s) != s {
		return `"` + strings.ReplaceAll(strings.ReplaceAll(s, `\`, `\\`), `"`, `\"`) + `"`
	}
	return s
}

func firstLine(s string, max int) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncate(strings.Join(strings.Fields(s), " "), max)
}

// slug renders a title as a filename fragment: lowercase, ASCII, dashes.
//
// Non-ASCII runes are dropped rather than transliterated. A title written
// entirely in another script therefore slugs to empty, which the caller handles
// by falling back to the conversation id — a filename honest about carrying no
// title beats a mojibake one that looks like it does.
func slug(s string, max int) string {
	var b strings.Builder
	lastDash := true // leading dashes are suppressed
	for _, r := range strings.ToLower(s) {
		switch {
		case r < unicode.MaxASCII && (unicode.IsLetter(r) || unicode.IsDigit(r)):
			b.WriteRune(r)
			lastDash = false
		case !lastDash && b.Len() < max:
			b.WriteByte('-')
			lastDash = true
		}
		if b.Len() >= max {
			break
		}
	}
	return strings.Trim(b.String(), "-")
}

func expandHome(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}

// newSummarizer resolves a provider, or reports that there is none.
//
// Summaries are on when a key is present and quietly off when it is not. An
// import that refused to run without a credential would break every existing
// use of this command; one that silently produced no cards would be worse. The
// note is the middle: it says what did not happen and how to make it happen.
func newSummarizer(quiet bool) summarize.Summarizer {
	sum, err := summarize.New()
	switch {
	case err == nil:
		if !quiet {
			fmt.Fprintf(os.Stderr, "tennis: summarizing cards with %s\n", sum.Provider())
		}
		return sum
	case errors.Is(err, summarize.ErrNoKey):
		if !quiet {
			fmt.Fprintln(os.Stderr,
				"tennis: no ANTHROPIC_API_KEY or OPENAI_API_KEY; cards will carry the opening message instead of a summary")
		}
	default:
		fmt.Fprintln(os.Stderr, "tennis:", err)
	}
	return nil
}
