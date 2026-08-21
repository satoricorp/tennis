// Command tennis is the command-line interface to a local hybrid search index.
//
// The verbs borrow from the sport, which is not purely a joke: match is the
// SQLite full-text operator, seed is what tournaments call ranking, and serve
// starts the daemon. The nouns stay boring — a namespace is a namespace —
// because those line up with a remote vector store's vocabulary, and matching
// it is what lets the same code talk to either.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/satoricorp/tennis"
	"github.com/satoricorp/tennis/embed"
)

// The help screen is the product's table of contents, so it lists what a
// person should reach for and nothing else. Everything still supported but not
// worth a newcomer's attention lives behind --agents: an agent reading the
// help wants the whole surface, a person wants the six commands that matter.
// Both are printed from the same pieces so the two can never drift.
const usageHead = `tennis — local hybrid search. Keyword + semantic, one file, no server.

USAGE
  tennis <command> [flags]

COMMANDS
  add <path...>                index sessions or files
  search <query>               search
  ls [source]                  list what tennis has, newest first
  rm <id...>                   delete documents
  ns [list|create|rm]          manage namespaces
  version
`

// usageAgents is the rest of the surface: supported, tested, and deliberately
// not advertised. serve is a niche escape hatch rather than the normal way to
// use tennis, and seed/import/match are the same commands as add and search
// with the namespace spelled first.
const usageAgents = `
ALSO AVAILABLE
  serve                        start the local HTTP API on 127.0.0.1:8817
  seed <namespace> <path...>   like add --files, namespace first
  import <namespace> <path...> like add, namespace first
  match <namespace> <query>    like search, namespace first
`

const usageTail = `
SOURCES
  tennis add --chatgpt <export.zip>       a ChatGPT export
  tennis add --claude <export.zip>        a Claude export
  tennis add --claude-code ~/.claude      Claude Code transcripts
  tennis add --codex ~/.codex             Codex transcripts
  tennis add --files ~/Documents/notes    plain files
  tennis add --ndjson < docs.ndjson       documents from a program, on stdin
  Without a source flag, add reads the path and works out which it is.

COMMON FLAGS
  --db <path>    database file (default ~/.tennis/db.sqlite, or $TENNIS_DB)
  --cards <dir>  where summary cards are written (default ~/tennis, or $TENNIS_CARDS)
  --ns <name>    namespace for add, search and rm (default ` + defaultNamespace + `, or $TENNIS_NS)
  --json         machine-readable output

Run 'tennis <command> --help' for command flags.
`

// helpText assembles the screen. The pointer to --agents is only printed on
// the short one, because on the long one there is nothing left to point at.
func helpText(agents bool) string {
	if agents {
		return usageHead + usageAgents + usageTail
	}
	return usageHead + usageTail + "Run 'tennis --agents' for the full command surface.\n"
}

// wantsAgents reports whether --agents appears in args, so it can be written
// either way round: `tennis --agents` and `tennis help --agents` both work.
func wantsAgents(args []string) bool {
	for _, a := range args {
		if a == "--agents" || a == "-agents" {
			return true
		}
	}
	return false
}

func main() {
	if len(os.Args) < 2 {
		writeLogo(os.Stderr)
		fmt.Fprint(os.Stderr, colorizeHelp(helpText(false), newStyler(os.Stderr)))
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "add":
		err = cmdAdd(args)
	case "ls":
		err = cmdLS(args)
	case "search":
		err = cmdSearch(args)
	case "seed":
		err = cmdSeed(args)
	case "import":
		err = cmdImport(args)
	case "match":
		err = cmdMatch(args)
	case "rm":
		err = cmdRm(args)
	case "ns":
		err = cmdNS(args)
	case "serve":
		err = cmdServe(args)
	case "version":
		fmt.Println("tennis " + buildVersion())
	case "-h", "--help", "help":
		// The block-letter mark is for a person opening the tool. --agents
		// output is read by a program, so it gets the text and nothing else.
		if wantsAgents(args) {
			fmt.Print(helpText(true))
			break
		}
		writeLogo(os.Stdout)
		fmt.Print(colorizeHelp(helpText(false), newStyler(os.Stdout)))
	case "--agents", "-agents":
		fmt.Print(helpText(true))
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, colorizeHelp(helpText(false), newStyler(os.Stderr)))
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, newStyler(os.Stderr).red("error:"), err)
		os.Exit(1)
	}
}

// version and commit are stamped by the release build
// (-ldflags "-X main.version=... -X main.commit=..."); a from-source build
// leaves them alone and buildVersion recovers what it can.
var (
	version = "dev"
	commit  = ""
)

// buildVersion is the line `tennis version` prints: the tag, and the commit it
// was actually built from.
//
// The tag alone does not identify a binary. Most tennis binaries in existence
// come from `go build ./cmd/tennis` in a working tree, where the tag is the
// literal string "dev" and the commit is the only thing telling one apart from
// another. Go already stamps vcs.revision into anything built inside a
// checkout, so the fact is in the file; this reads it back out.
//
// A binary from `go install ...@version` carries no VCS stamp — the module
// cache is not a checkout — but does carry the module version, which answers
// the same question by another name.
func buildVersion() string {
	v, c, dirty := version, commit, false
	bi, ok := debug.ReadBuildInfo()
	if ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if c == "" {
					c = s.Value
				}
			case "vcs.modified":
				dirty = s.Value == "true"
			}
		}
	}
	// bi.Main.Version is only consulted when there is no VCS stamp, which is
	// precisely the `go install ...@v1.2.3` case it answers. Consulting it for
	// a working-tree build instead yields the module pseudo-version — a string
	// like v0.1.1-0.20260820143053-b489e77ca87b that already ends in the same
	// commit this function is about to print, twice over.
	if ok && c == "" && v == "dev" && bi.Main.Version != "" && bi.Main.Version != "(devel)" {
		v = bi.Main.Version
	}
	if c == "" {
		return v
	}
	if len(c) > 12 {
		c = c[:12]
	}
	if dirty {
		return fmt.Sprintf("%s (%s, dirty)", v, c)
	}
	return fmt.Sprintf("%s (%s)", v, c)
}

// parseInterleaved parses args allowing flags before, between, or after
// positional arguments, returning the positionals in order.
//
// Go's flag package stops at the first non-flag token, so with a plain Parse,
// `tennis ns create cloud --openai X` silently ignores --openai — and then
// creates a namespace permanently bound to the wrong embedder. A flag that is
// dropped rather than rejected is the worst kind of CLI bug, because the
// command still "works".
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		rest := fs.Args()
		// "--" must stay terminal across iterations. flag.Parse consumes it,
		// but this loop re-Parses the remainder, which would resurrect
		// flag-looking positionals after it ("match ns -- a -n" would try to
		// parse -n again). fs.Args() is always a tail of the input, so if the
		// token just before that tail was "--", Parse stopped there and
		// everything left is positional by declaration.
		if consumed := len(args) - len(rest); consumed > 0 && args[consumed-1] == "--" {
			return append(pos, rest...), nil
		}
		if len(rest) == 0 {
			return pos, nil
		}
		pos = append(pos, rest[0])
		args = rest[1:]
	}
}

// defaultDB resolves the database path from the flag, the environment, or the
// conventional location, in that order.
func defaultDB() string {
	if p := os.Getenv("TENNIS_DB"); p != "" {
		return p
	}
	return "~/.tennis/db.sqlite"
}

// defaultNamespace is where add and search go when nothing says otherwise.
//
// Namespaces earn their keep when you are keeping separate things separate,
// but that is a second question, and asking it before the first import is what
// makes a search tool feel like a database. One place to put things is the
// honest default; --ns is there the moment one place stops being enough.
const defaultNamespace = "context"

// resolveNS reads the namespace from the flag, the environment, or the
// default, in that order — the same precedence defaultDB uses, so the two
// knobs behave alike.
func resolveNS(flagVal string) string {
	if flagVal != "" {
		return flagVal
	}
	if v := os.Getenv("TENNIS_NS"); v != "" {
		return v
	}
	return defaultNamespace
}

// open connects and reports model downloads to stderr, so progress never
// contaminates piped stdout.
func open(path string, quiet bool) (*tennis.DB, error) {
	db, err := tennis.Open(path)
	if err != nil {
		return nil, err
	}
	if !quiet {
		db.Progress = func(msg string) { fmt.Fprintln(os.Stderr, "tennis:", msg) }
	}
	return db, nil
}

func cmdSeed(args []string) error {
	fs_ := flag.NewFlagSet("seed", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	asJSON := fs_.Bool("json", false, "machine-readable output")
	ext := fs_.String("ext", ".md,.txt", "comma-separated file extensions to index")
	model := fs_.String("model", "", "built-in model for a new namespace (default "+embed.DefaultModel+")")
	openaiModel := fs_.String("openai", "", "use an OpenAI model instead of the built-in one (requires OPENAI_API_KEY)")
	chunkSize := fs_.Int("chunk", 0, "chunk size in characters for a new namespace")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: tennis seed <namespace> <path...>")
	}
	nsName, paths := pos[0], pos[1:]

	db, err := open(*dbPath, *asJSON)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	ns, err := db.Namespace(ctx, nsName)
	switch {
	case errors.Is(err, tennis.ErrNamespaceNotFound):
		// Creating on first seed is the ergonomic choice, and it is safe
		// because the embedder is bound here and enforced forever after.
		ns, err = db.CreateNamespace(ctx, nsName, tennis.NamespaceOptions{
			Model: *model, OpenAIModel: *openaiModel, ChunkSize: *chunkSize,
		})
		if err != nil {
			return err
		}
		if !*asJSON {
			fmt.Fprintf(os.Stderr, "tennis: created namespace %q bound to %s\n", nsName, ns.EmbedderID())
		}
	case err != nil:
		// Any other failure — a missing OPENAI_API_KEY, a model mismatch — must
		// surface as itself, not as "already exists" from a doomed create.
		return err
	}

	wanted := map[string]bool{}
	for _, e := range strings.Split(*ext, ",") {
		if e = strings.TrimSpace(e); e != "" {
			wanted[e] = true
		}
	}

	var docs []tennis.Document
	skippedFiles := 0
	skip := func(p, why string) {
		skippedFiles++
		if !*asJSON {
			fmt.Fprintf(os.Stderr, "tennis: skipping %s (%s)\n", p, why)
		}
	}
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if len(wanted) > 0 && !wanted[filepath.Ext(p)] {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			// The size cap runs before the read, so a stray multi-GB log
			// costs a stat rather than a slurp.
			if info.Size() > maxSeedFileSize {
				skip(p, fmt.Sprintf("%.1fMB is over the %dMB cap", float64(info.Size())/(1<<20), maxSeedFileSize/(1<<20)))
				return nil
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			if isBinary(body) {
				skip(p, "binary content")
				return nil
			}
			abs, err := filepath.Abs(p)
			if err != nil {
				return fmt.Errorf("resolving absolute path for %s: %w", p, err)
			}
			attrs := map[string]any{
				"path": abs, "name": d.Name(),
				"modified": info.ModTime().UTC().Format(time.RFC3339),
				"size":     info.Size(),
			}
			docs = append(docs, tennis.Document{ID: abs, Text: string(body), Attributes: attrs})
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(docs) == 0 {
		return fmt.Errorf("no indexable files under %s (looking for %s, %d skipped)", strings.Join(paths, ", "), *ext, skippedFiles)
	}

	res, err := ns.Write(ctx, docs)
	if err != nil {
		return err
	}
	if *asJSON {
		return emit(map[string]any{"written": res.Written, "skipped": res.Skipped, "chunks": res.Chunks, "skipped_files": skippedFiles})
	}
	fmt.Printf("seeded %d, skipped %d unchanged, %d chunks in %q", res.Written, res.Skipped, res.Chunks, nsName)
	if skippedFiles > 0 {
		fmt.Printf(" (%d files skipped)", skippedFiles)
	}
	fmt.Println()
	return nil
}

// maxSeedFileSize bounds what seed will read. Files past this are almost never
// prose someone wants ranked — they are logs, dumps, and datasets — and one of
// them would dominate both embedding time and the index.
const maxSeedFileSize = 10 << 20

// isBinary reports whether content looks like something other than text, using
// the same heuristic git uses: a NUL byte in the leading window. Indexing a
// PDF's raw bytes never errors — it just quietly pollutes every future ranking
// with garbage chunks, which is worse.
//
// Known gap: a binary file shorter than the window with no NUL byte in it
// (some single-frame image headers, for instance) passes this check and gets
// indexed. git accepts the same gap for the same reason — a byte-proportion
// heuristic catches more but also misclassifies legitimate text that happens
// to be terse and symbol-heavy, and that false positive is worse here than a
// rare missed binary, since it would silently drop real content from seed.
func isBinary(content []byte) bool {
	window := content
	if len(window) > 8192 {
		window = window[:8192]
	}
	for _, b := range window {
		if b == 0 {
			return true
		}
	}
	return false
}

// cmdSearch answers a question against the default namespace.
func cmdSearch(args []string) error {
	fs_ := flag.NewFlagSet("search", flag.ExitOnError)
	var o searchOpts
	registerSearchFlags(fs_, &o)
	nsName := fs_.String("ns", "", "namespace (default "+defaultNamespace+", or $TENNIS_NS)")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: tennis search <query>")
	}
	return runSearch(resolveNS(*nsName), strings.Join(pos, " "), o)
}

// cmdMatch is search with the namespace named positionally.
func cmdMatch(args []string) error {
	fs_ := flag.NewFlagSet("match", flag.ExitOnError)
	var o searchOpts
	registerSearchFlags(fs_, &o)
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: tennis match <namespace> <query>")
	}
	return runSearch(pos[0], strings.Join(pos[1:], " "), o)
}

type searchOpts struct {
	dbPath string
	asJSON bool
	topK   int
	mode   string
	where  string
}

func registerSearchFlags(fs_ *flag.FlagSet, o *searchOpts) {
	fs_.StringVar(&o.dbPath, "db", defaultDB(), "database file")
	fs_.BoolVar(&o.asJSON, "json", false, "machine-readable output")
	// One result by default, printed in full. Asking a question and being
	// handed ten truncated lines means reading none of them; the answer you
	// wanted is almost always the first, so that is what you get, whole.
	// Raising -k is how you ask to compare.
	fs_.IntVar(&o.topK, "k", 1, "how many results")
	fs_.IntVar(&o.topK, "n", 1, "how many results (alias for -k)")
	fs_.StringVar(&o.mode, "mode", "hybrid", "hybrid | keyword | semantic")
	fs_.StringVar(&o.where, "where", "", "attribute filter, e.g. status=merged (repeat with commas)")
}

func runSearch(nsName, query string, o searchOpts) error {
	db, err := open(o.dbPath, o.asJSON)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	ns, err := db.Namespace(ctx, nsName)
	if err != nil {
		return err
	}
	filter, err := parseWhere(o.where)
	if err != nil {
		return err
	}

	results, err := ns.Query(ctx, tennis.Query{
		Text: query, TopK: o.topK, Mode: tennis.Mode(o.mode), Filter: filter,
	})
	if err != nil {
		return err
	}
	if o.asJSON {
		return emit(results)
	}
	if len(results) == 0 {
		fmt.Println("no matches")
		return nil
	}

	renderResults(os.Stdout, results, textWidth(), newStyler(os.Stdout))
	return nil
}

// renderResults writes the human-readable form of a result set. The styling
// keeps to the parts a reader consults rather than reads: the citation and
// ranker tags recede, the rank numbers take the accent, and the words — the
// thing being searched for — stay exactly the terminal's own color.
func renderResults(w io.Writer, results []tennis.Result, width int, st styler) {
	// One result is an answer, not a list of one: there is no rank to compare
	// it against, so it is printed as the words and where they came from.
	if len(results) == 1 {
		top := results[0]
		fmt.Fprintln(w, wrap(top.Text, width, "> ", "  "))
		fmt.Fprintf(w, "\n  %s\n", st.dim(citation(top)))
		return
	}

	// More than one and the ranking is the thing being shown, so every result
	// is numbered — the best one included. Numbering the runners-up 2, 3, 4
	// under an unnumbered answer reads as a list that lost its first item.
	for i, r := range results {
		if i > 0 {
			fmt.Fprintln(w)
		}
		fmt.Fprintf(w, "%s %s  %s\n", st.ball(fmt.Sprintf("%2d.", i+1)), citation(r), st.dim("["+rankers(r)+"]"))
		fmt.Fprintln(w, wrap(r.Text, width, "    ", "    "))
	}
}

// rankers names which side of the search found a result, and where it placed.
// A hit only the semantic ranker surfaced is a different kind of answer than
// one both agreed on, and the tag is what makes that legible.
func rankers(r tennis.Result) string {
	var found []string
	if r.KeywordRank > 0 {
		found = append(found, fmt.Sprintf("kw#%d", r.KeywordRank))
	}
	if r.SemanticRank > 0 {
		found = append(found, fmt.Sprintf("sem#%d", r.SemanticRank))
	}
	return strings.Join(found, " ")
}

// textWidth is the measure result text is folded to.
//
// Prose stops being easy to read much past the high seventies, so a wide
// terminal is capped rather than filled; a narrow one is honoured exactly.
// When stdout is not a terminal — a pipe, a file, $(…) — there is no width to
// ask for and 80 is the conventional answer.
func textWidth() int {
	w, _, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil || w <= 0 {
		w = 80
	}
	if w > 84 {
		w = 84
	}
	if w < 32 {
		w = 32
	}
	return w
}

// wrap folds s to width, opening with the first prefix and indenting every
// line after it with rest.
//
// Existing line breaks are kept, because the text is usually somebody's
// message and its paragraphs, lists and code blocks are structure worth
// keeping. A word longer than the measure — a URL, a hash — is allowed to
// overrun rather than be broken somewhere meaningless.
//
// Columns are counted in runes, not bytes. Transcripts are full of smart
// quotes and em-dashes, and every one of them is three bytes wide and one
// column wide; counting bytes wraps those lines early and leaves the right
// margin visibly ragged.
func wrap(s string, width int, first, rest string) string {
	var b strings.Builder
	prefix := first
	newline := func() {
		b.WriteString("\n")
		prefix = rest
	}
	for i, line := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if i > 0 {
			newline()
		}
		words := strings.Fields(line)
		if len(words) == 0 {
			b.WriteString(strings.TrimRight(prefix, " "))
			continue
		}
		col := 0
		for j, w := range words {
			n := utf8.RuneCountInString(w)
			switch {
			case j == 0:
				b.WriteString(prefix)
				col = utf8.RuneCountInString(prefix)
			case col+1+n > width:
				newline()
				b.WriteString(prefix)
				col = utf8.RuneCountInString(prefix)
			default:
				b.WriteString(" ")
				col++
			}
			b.WriteString(w)
			col += n
		}
	}
	return b.String()
}

// citation is the line under a result: where it came from, when, how strongly
// it matched.
func citation(r tennis.Result) string {
	if d := resultDate(r); d != "" {
		return fmt.Sprintf("%s [%s] %.4f", sourceLabel(r), d, r.Score)
	}
	return fmt.Sprintf("%s %.4f", sourceLabel(r), r.Score)
}

// sourceLabel names where a result came from the way a person would say it
// out loud, rather than the way it is spelled in an attribute.
func sourceLabel(r tennis.Result) string {
	s, _ := r.Attributes["source"].(string)
	switch s {
	case formatChatGPT:
		return "ChatGPT"
	case formatClaude:
		return "Claude"
	case formatClaudeCode:
		return "Claude Code"
	case formatCodex:
		return "Codex"
	case "":
		// A seeded file has no source. Its name is what identifies it.
		return truncate(displayID(r), 40)
	}
	return s
}

// resultDate reduces a timestamp to the day, which is the precision a person
// reads a citation at.
func resultDate(r tennis.Result) string {
	s, _ := r.Attributes["created"].(string)
	if s == "" {
		s, _ = r.Attributes["modified"].(string)
	}
	if s == "" {
		return ""
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t.Format("2006-01-02")
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

// plural renders a count with its noun. "1 documents" in a warning about
// destroying data reads as a bug in the thing doing the destroying, which is
// not what you want a person reading at that particular moment.
func plural(n int, one string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %ss", n, one)
}

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

// cmdRm deletes documents. The namespace is a flag here, matching add and
// search, because rm is a document command — `tennis ns rm` is the one that
// removes a namespace.
func cmdRm(args []string) error {
	fs_ := flag.NewFlagSet("rm", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	asJSON := fs_.Bool("json", false, "machine-readable output")
	nsName := fs_.String("ns", "", "namespace (default "+defaultNamespace+", or $TENNIS_NS)")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) < 1 {
		return fmt.Errorf("usage: tennis rm <id...> [--ns name]")
	}
	db, err := open(*dbPath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	// rm used to take the namespace positionally. Left alone, the old spelling
	// still runs — against the wrong namespace, deleting a document named
	// after the right one. Since the tell is unambiguous (a bare first argument
	// that is itself a namespace, with more arguments behind it), refuse rather
	// than delete something nobody asked to delete.
	if *nsName == "" && len(pos) > 1 {
		if _, err := db.Namespace(ctx, pos[0]); err == nil {
			return fmt.Errorf("rm takes ids, not a namespace: try `tennis rm %s --ns %s`",
				strings.Join(pos[1:], " "), pos[0])
		}
	}

	ns, err := db.Namespace(ctx, resolveNS(*nsName))
	if err != nil {
		return err
	}
	n, err := ns.Delete(ctx, pos)
	if err != nil {
		return err
	}
	if *asJSON {
		return emit(map[string]int{"deleted": n})
	}
	fmt.Printf("deleted %d\n", n)
	return nil
}

func cmdNS(args []string) error {
	fs_ := flag.NewFlagSet("ns", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	asJSON := fs_.Bool("json", false, "machine-readable output")
	model := fs_.String("model", "", "built-in model (default "+embed.DefaultModel+")")
	openaiModel := fs_.String("openai", "", "use an OpenAI model (requires OPENAI_API_KEY)")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	sub := "list"
	if len(pos) > 0 {
		sub = pos[0]
	}
	db, err := open(*dbPath, *asJSON)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	switch sub {
	case "list":
		infos, err := db.ListNamespaces(ctx)
		if err != nil {
			return err
		}
		if *asJSON {
			return emit(infos)
		}
		if len(infos) == 0 {
			fmt.Println("no namespaces yet — try: tennis seed notes ./docs")
			return nil
		}
		st := newStyler(os.Stdout)
		fmt.Println(st.dim(fmt.Sprintf("%-20s %-34s %6s %8s %8s", "NAMESPACE", "EMBEDDER", "DIMS", "DOCS", "CHUNKS")))
		for _, i := range infos {
			fmt.Printf("%-20s %-34s %6d %8d %8d\n", i.Name, i.EmbedderID, i.Dims, i.Documents, i.Chunks)
		}
		return nil

	case "create":
		if len(pos) < 2 {
			return fmt.Errorf("usage: tennis ns create <name> [--model M | --openai M]")
		}
		ns, err := db.CreateNamespace(ctx, pos[1], tennis.NamespaceOptions{Model: *model, OpenAIModel: *openaiModel})
		if err != nil {
			return err
		}
		fmt.Printf("created %q bound to %s\n", ns.Name(), ns.EmbedderID())
		return nil

	case "rm":
		if len(pos) < 2 {
			return fmt.Errorf("usage: tennis ns rm <name>")
		}
		name := pos[1]

		// A namespace is not an empty container being tidied away: it holds
		// every document ever written to it, and dropping it takes all of them.
		// The count is looked up first so the warning can name what is at
		// stake while it is still at stake — said afterwards it is a receipt,
		// not a warning.
		infos, err := db.ListNamespaces(ctx)
		if err != nil {
			return err
		}
		var info tennis.NamespaceInfo
		found := false
		for _, i := range infos {
			if i.Name == name {
				info, found = i, true
				break
			}
		}
		if !found {
			return fmt.Errorf("namespace %q: %w", name, tennis.ErrNamespaceNotFound)
		}
		if !*asJSON {
			fmt.Fprintf(os.Stderr, "tennis: removing %q takes its %s (%s) with it\n",
				name, plural(info.Documents, "document"), plural(info.Chunks, "chunk"))
		}
		if err := db.DropNamespace(ctx, name); err != nil {
			return err
		}
		if *asJSON {
			return emit(map[string]any{
				"removed": name, "documents": info.Documents, "chunks": info.Chunks,
			})
		}
		fmt.Printf("removed %q\n", name)
		return nil

	}
	return fmt.Errorf("unknown subcommand %q (want list, create, rm)", sub)
}

// parseWhere turns "status=merged,cost>5" into a filter. Deliberately tiny:
// anything more expressive belongs in the SDK, where a real expression is
// clearer than a string that has to be escaped through a shell.
func parseWhere(s string) (tennis.Filter, error) {
	if strings.TrimSpace(s) == "" {
		return nil, nil
	}
	var parts []tennis.Filter
	for _, clause := range strings.Split(s, ",") {
		clause = strings.TrimSpace(clause)
		if clause == "" {
			continue
		}
		for _, op := range []string{">=", "<=", "!=", "=", ">", "<"} {
			if i := strings.Index(clause, op); i > 0 {
				key := strings.TrimSpace(clause[:i])
				val := coerce(strings.TrimSpace(clause[i+len(op):]))
				switch op {
				case "=":
					parts = append(parts, tennis.Eq(key, val))
				case "!=":
					parts = append(parts, tennis.NotEq(key, val))
				case ">":
					parts = append(parts, tennis.Gt(key, val))
				case ">=":
					parts = append(parts, tennis.Gte(key, val))
				case "<":
					parts = append(parts, tennis.Lt(key, val))
				case "<=":
					parts = append(parts, tennis.Lte(key, val))
				}
				goto next
			}
		}
		return nil, fmt.Errorf("cannot parse filter %q (want key=value, key>value, ...)", clause)
	next:
	}
	if len(parts) == 1 {
		return parts[0], nil
	}
	return tennis.And(parts...), nil
}

// coerce makes "5" a number so numeric comparisons work, while leaving
// anything non-numeric as text.
func coerce(s string) any {
	var f float64
	if _, err := fmt.Sscanf(s, "%g", &f); err == nil {
		if s == fmt.Sprintf("%g", f) {
			return f
		}
	}
	return s
}

func displayID(r tennis.Result) string {
	if p, ok := r.Attributes["name"].(string); ok && p != "" {
		return p
	}
	// Imported history has no filename, and its ID is a pair of UUIDs. The
	// conversation's title is the only part of it a person recognizes.
	if t, ok := r.Attributes["title"].(string); ok && t != "" {
		return t
	}
	return r.ID
}

// truncate cuts to n runes, not n bytes. A title carrying an em-dash or any
// CJK text would otherwise be sliced mid-character, and the column would print
// a replacement glyph where the ellipsis should be.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}

func emit(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
