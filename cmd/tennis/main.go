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
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/term"

	"github.com/satoricorp/tennis"
	"github.com/satoricorp/tennis/embed"
)

const usage = `tennis — local hybrid search. Keyword + semantic, one file, no server.

USAGE
  tennis <command> [flags]

COMMANDS
  add <path...>                index sessions or files
  search <query>               search
  put <namespace>              ingest NDJSON documents from stdin
  get <namespace> <id>         print one document
  rm <namespace> <id...>       delete documents
  ns [list|create|drop]        manage namespaces
  serve                        start the local HTTP API
  version

SOURCES
  tennis add --chatgpt <export.zip>       a ChatGPT export
  tennis add --claude <export.zip>        a Claude export
  tennis add --claude-code ~/.claude      Claude Code transcripts
  tennis add --codex ~/.codex             Codex transcripts
  tennis add --files ~/Documents/notes    plain files
  Without a source flag, add reads the path and works out which it is.

COMMON FLAGS
  --db <path>    database file (default ~/.tennis/db.sqlite, or $TENNIS_DB)
  --ns <name>    namespace for add and search (default ` + defaultNamespace + `, or $TENNIS_NS)
  --json         machine-readable output

NAMING THE NAMESPACE POSITIONALLY
  seed <namespace> <path...>   like add --files
  import <namespace> <path...> like add
  match <namespace> <query>    like search

Run 'tennis <command> --help' for command flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	cmd := os.Args[1]
	args := os.Args[2:]

	var err error
	switch cmd {
	case "add":
		err = cmdAdd(args)
	case "search":
		err = cmdSearch(args)
	case "seed":
		err = cmdSeed(args)
	case "import":
		err = cmdImport(args)
	case "put":
		err = cmdPut(args)
	case "match":
		err = cmdMatch(args)
	case "get":
		err = cmdGet(args)
	case "rm":
		err = cmdRm(args)
	case "ns":
		err = cmdNS(args)
	case "serve":
		err = cmdServe(args)
	case "version":
		fmt.Println("tennis " + version)
	case "-h", "--help", "help":
		fmt.Print(usage)
	default:
		fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

// version is stamped by the release build (-ldflags "-X main.version=...");
// a from-source build reports dev.
var version = "dev"

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

	// The best hit is the answer, so it is printed as one: the words in full,
	// then where they came from. Everything below it is the runners-up, and
	// those keep the ranked-list shape because comparing them is the point.
	width := textWidth()
	top := results[0]
	fmt.Println(wrap(top.Text, width, "> ", "  "))
	fmt.Printf("\n  %s\n", citation(top))

	for i, r := range results[1:] {
		// Showing which ranker found a result makes the ranking legible: a hit
		// only the semantic side surfaced is a different kind of answer than
		// one both agreed on.
		var found []string
		if r.KeywordRank > 0 {
			found = append(found, fmt.Sprintf("kw#%d", r.KeywordRank))
		}
		if r.SemanticRank > 0 {
			found = append(found, fmt.Sprintf("sem#%d", r.SemanticRank))
		}
		fmt.Println()
		fmt.Printf("%2d. %s  [%s]\n", i+2, citation(r), strings.Join(found, " "))
		fmt.Println(wrap(r.Text, width, "    ", "    "))
	}
	return nil
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

func oneLine(s string) string {
	return strings.Join(strings.Fields(s), " ")
}

func cmdGet(args []string) error {
	fs_ := flag.NewFlagSet("get", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	asJSON := fs_.Bool("json", false, "machine-readable output")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) != 2 {
		return fmt.Errorf("usage: tennis get <namespace> <id>")
	}
	db, err := open(*dbPath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	ns, err := db.Namespace(ctx, pos[0])
	if err != nil {
		return err
	}
	doc, err := ns.Get(ctx, pos[1])
	if err != nil {
		return err
	}
	if *asJSON {
		return emit(doc)
	}
	fmt.Println(doc.Text)
	return nil
}

func cmdRm(args []string) error {
	fs_ := flag.NewFlagSet("rm", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	asJSON := fs_.Bool("json", false, "machine-readable output")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: tennis rm <namespace> <id...>")
	}
	db, err := open(*dbPath, true)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()
	ns, err := db.Namespace(ctx, pos[0])
	if err != nil {
		return err
	}
	n, err := ns.Delete(ctx, pos[1:])
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
		fmt.Printf("%-20s %-34s %6s %8s %8s\n", "NAMESPACE", "EMBEDDER", "DIMS", "DOCS", "CHUNKS")
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

	case "drop":
		if len(pos) < 2 {
			return fmt.Errorf("usage: tennis ns drop <name>")
		}
		if err := db.DropNamespace(ctx, pos[1]); err != nil {
			return err
		}
		fmt.Printf("dropped %q\n", pos[1])
		return nil
	}
	return fmt.Errorf("unknown subcommand %q (want list, create, drop)", sub)
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

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if n <= 1 {
		return s[:n]
	}
	return s[:n-1] + "…"
}

func emit(v any) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
