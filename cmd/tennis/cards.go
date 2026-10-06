package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"

	"github.com/satoricorp/tennis/summarize"
)

// A card is a readable markdown summary of one conversation or one file,
// written to ~/tennis as it is imported.
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

// card is one entry in the card directory before it is rendered: the thing
// being described, reduced to what the filename, the frontmatter, and the
// summarizer need. A conversation and a file both become one, so the writer,
// the stale-name sweep, and the no-model fallback are written once.
type card struct {
	kind   string         // summarize.KindConversation or summarize.KindFile
	source string         // chatgpt | claude | claude-code | codex | file
	id     string         // the conversation id, or the file's document id
	title  string         // the thread's title or the file's name; may be empty
	stamp  string         // created (thread) or modified (file), as the source gave it
	when   time.Time      // the stamp parsed, or the earliest turn; names a thread's card
	turns  int            // conversations only
	meta   map[string]any // extra frontmatter: project, cwd, branch, model, size

	text     string // what the summarizer reads
	fallback string // what the card carries instead when there is no summary
	pointer  string // the line that says how to get the rest
}

// card is the conversation as the card writer sees it.
func (c conversation) card() card {
	title := c.title
	if title == "" {
		title = firstLine(firstUserTurn(c), 80)
	}
	when, err := time.Parse(time.RFC3339, c.create)
	if err != nil {
		when = earliestTurn(c)
	}
	return card{
		kind: summarize.KindConversation, source: c.source, id: c.id,
		title: title, stamp: c.create, when: when, turns: len(c.turns), meta: c.extra,
		text:     c.transcript(),
		fallback: summarize.Fallback(firstUserTurn(c)),
		pointer:  fmt.Sprintf("Full conversation: `tennis search --where 'session=%s'`", c.id),
	}
}

// fileCard describes an indexed file the way conversation.card describes a
// thread. Its opening lines stand in for the opening message when there is no
// model: the start of a document is usually what it is about.
func fileCard(id, name, text string, modified time.Time, size int64) card {
	stamp := ""
	if !modified.IsZero() {
		stamp = modified.UTC().Format(time.RFC3339)
	}
	return card{
		kind: summarize.KindFile, source: "file", id: id,
		title: name, stamp: stamp, when: modified, meta: map[string]any{"size": size},
		text:     text,
		fallback: excerpt(text),
		pointer:  fmt.Sprintf("Full text: `tennis search --where 'path=%s'`", id),
	}
}

// excerpt is a file's no-model fallback: its first lines as they are, in a
// code fence so a spreadsheet's rows stay rows on the card. The word budget
// matches summarize.Fallback; the line cap keeps a sheet of one-word cells
// from running on.
func excerpt(text string) string {
	const maxWords, maxLines, maxLine = 60, 12, 120
	var lines []string
	words, more := 0, false
	for _, line := range strings.Split(strings.TrimSpace(text), "\n") {
		line = strings.TrimRight(line, " \t\r")
		if line == "" {
			continue
		}
		if words >= maxWords || len(lines) >= maxLines {
			more = true
			break
		}
		lines = append(lines, truncate(line, maxLine))
		words += len(strings.Fields(line))
	}
	if len(lines) == 0 {
		return ""
	}
	if more {
		lines = append(lines, "…")
	}
	return "```\n" + strings.Join(lines, "\n") + "\n```"
}

// cardWriter turns cards into files in the background while the import keeps
// reading. Conversations and files arrive one at a time from the readers;
// doing the API call inline would serialize hundreds of round trips behind a
// parser that is already finished.
type cardWriter struct {
	dir string
	sum summarize.Summarizer

	work chan card
	wg   sync.WaitGroup
	ctx  context.Context

	// close runs both at the end of the import, including one cut short by a
	// bad path, and from a deferred safety net covering the error returns
	// between here and there. Closing a channel twice panics, so which one
	// gets there first must not matter.
	once sync.Once

	mu      sync.Mutex
	written int
	failed  int
}

// newCardWriter starts the workers. A nil Summarizer is allowed: cards are
// still written, carrying the opening of the thread or file instead of prose.
func newCardWriter(ctx context.Context, dir string, sum summarize.Summarizer) (*cardWriter, error) {
	expanded, err := expandHome(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(expanded, 0o755); err != nil {
		return nil, err
	}

	w := &cardWriter{dir: expanded, sum: sum, ctx: ctx, work: make(chan card)}
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

func (w *cardWriter) add(c card) {
	if w == nil {
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
func (w *cardWriter) one(c card) {
	text := c.fallback
	if w.sum != nil {
		in := summarize.Input{
			Kind:       c.kind,
			Source:     c.source,
			Title:      c.title,
			Project:    attrText(c.meta, "project", "cwd"),
			Turns:      c.turns,
			Transcript: c.text,
		}
		if c.kind == summarize.KindFile {
			in.Path = c.id
		}
		got, err := w.sum.Summarize(w.ctx, in)
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

// writeCard writes or replaces the card.
func writeCard(dir string, c card, summary string) error {
	stem, prefix := cardStem(c)
	path := filepath.Join(dir, stem+".md")

	// Sweep cards from an earlier import of this same conversation whose title
	// — and therefore slug — has since changed. Claude Code names a session
	// several turns in, so importing twice can produce two different names for
	// one conversation. The timestamp-and-source prefix is stable.
	if prefix != "" {
		if stale, _ := filepath.Glob(filepath.Join(dir, prefix+"*.md")); len(stale) > 0 {
			for _, s := range stale {
				if s != path {
					os.Remove(s)
				}
			}
		}
	}
	return os.WriteFile(path, []byte(renderCard(c, summary)), 0o644)
}

// cardStem is the filename without extension, plus the stable prefix used to
// find earlier names for the same conversation. The volatile part — the title
// slug — has to come last for that sweep to work.
//
// A file's card is named by the file, not by when it was touched: the same
// path has to land on the same card after every edit, and the hash keeps two
// notes.md in different folders apart. Nothing in the name is volatile, so
// there is no prefix to sweep.
func cardStem(c card) (stem, prefix string) {
	if c.kind == summarize.KindFile {
		name := slug(c.title, 60)
		if name == "" {
			name = "file"
		}
		return name + "-" + shortHash(c.id), ""
	}

	ts := c.when
	if ts.IsZero() {
		ts = time.Now()
	}
	prefix = ts.UTC().Format("2006-01-02-150405") + "-" + slug(c.source, 20) + "-"
	if s := slug(c.title, 60); s != "" {
		return prefix + s, prefix
	}
	// A title that slugs to nothing — non-Latin, or all punctuation — falls
	// back to the conversation id so the file is still identifiable on disk.
	return prefix + slug(c.id, 24), prefix
}

// shortHash is enough of a digest to tell two paths apart in a filename.
func shortHash(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:4])
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

// renderCard builds the markdown. The document ID is in the frontmatter and
// the retrieval command is in the body, because the card's whole job is to be
// readable on its own and to say how to get the rest.
func renderCard(c card, summary string) string {
	title := c.title
	if title == "" {
		title = "untitled"
	}
	isFile := c.kind == summarize.KindFile

	var b strings.Builder
	b.WriteString("---\n")
	if isFile {
		fmt.Fprintf(&b, "file: %s\n", yamlString(c.id))
	} else {
		fmt.Fprintf(&b, "session: %s\n", yamlString(c.source+":"+c.id))
	}
	fmt.Fprintf(&b, "source: %s\n", c.source)
	fmt.Fprintf(&b, "title: %s\n", yamlString(title))
	switch {
	case c.stamp == "":
	case isFile:
		fmt.Fprintf(&b, "modified: %s\n", c.stamp)
	default:
		fmt.Fprintf(&b, "created: %s\n", c.stamp)
	}
	if !isFile {
		fmt.Fprintf(&b, "turns: %d\n", c.turns)
	}
	for _, k := range []string{"project", "cwd", "branch", "model", "size"} {
		if v := yamlScalar(c.meta[k]); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	b.WriteString("---\n\n")

	fmt.Fprintf(&b, "# %s\n\n", title)
	if s := strings.TrimSpace(summary); s != "" {
		b.WriteString(s)
		b.WriteString("\n\n")
	}
	b.WriteString(c.pointer)
	b.WriteString("\n")
	return b.String()
}

// yamlScalar renders a frontmatter value, or nothing for one that is absent or
// empty. Strings are quoted when they need to be; a number stays a number.
func yamlScalar(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case string:
		if v == "" {
			return ""
		}
		return yamlString(v)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	default:
		return yamlString(fmt.Sprint(v))
	}
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
