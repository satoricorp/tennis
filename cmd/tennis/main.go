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
	"sort"
	"strings"
	"time"

	"github.com/joelachance/tennis"
	"github.com/joelachance/tennis/embed"
)

const usage = `tennis — local hybrid search. Keyword + semantic, one file, no server.

USAGE
  tennis <command> [flags]

COMMANDS
  seed <namespace> <path...>   index files or directories
  match <namespace> <query>    search
  get <namespace> <id>         print one document
  rm <namespace> <id...>       delete documents
  ns [list|create|drop]        manage namespaces
  serve                        start the local HTTP API
  version

COMMON FLAGS
  --db <path>    database file (default ~/.tennis/db.sqlite, or $TENNIS_DB)
  --json         machine-readable output

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
	case "seed":
		err = cmdSeed(args)
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

const version = "0.1.0"

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
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
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
	for _, root := range paths {
		err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			if len(wanted) > 0 && !wanted[filepath.Ext(p)] {
				return nil
			}
			body, err := os.ReadFile(p)
			if err != nil {
				return err
			}
			abs, _ := filepath.Abs(p)
			info, _ := d.Info()
			attrs := map[string]any{"path": abs, "name": d.Name()}
			if info != nil {
				attrs["modified"] = info.ModTime().UTC().Format(time.RFC3339)
				attrs["size"] = info.Size()
			}
			docs = append(docs, tennis.Document{ID: abs, Text: string(body), Attributes: attrs})
			return nil
		})
		if err != nil {
			return err
		}
	}
	if len(docs) == 0 {
		return fmt.Errorf("no matching files under %s (looking for %s)", strings.Join(paths, ", "), *ext)
	}

	res, err := ns.Write(ctx, docs)
	if err != nil {
		return err
	}
	if *asJSON {
		return emit(res)
	}
	fmt.Printf("seeded %d, skipped %d unchanged, %d chunks in %q\n", res.Written, res.Skipped, res.Chunks, nsName)
	return nil
}

func cmdMatch(args []string) error {
	fs_ := flag.NewFlagSet("match", flag.ExitOnError)
	dbPath := fs_.String("db", defaultDB(), "database file")
	asJSON := fs_.Bool("json", false, "machine-readable output")
	topK := fs_.Int("n", 10, "how many results")
	mode := fs_.String("mode", "hybrid", "hybrid | keyword | semantic")
	where := fs_.String("where", "", "attribute filter, e.g. status=merged (repeat with commas)")
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: tennis match <namespace> <query>")
	}
	nsName := pos[0]
	query := strings.Join(pos[1:], " ")

	db, err := open(*dbPath, *asJSON)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	ns, err := db.Namespace(ctx, nsName)
	if err != nil {
		return err
	}
	filter, err := parseWhere(*where)
	if err != nil {
		return err
	}

	results, err := ns.Query(ctx, tennis.Query{
		Text: query, TopK: *topK, Mode: tennis.Mode(*mode), Filter: filter,
	})
	if err != nil {
		return err
	}
	if *asJSON {
		return emit(results)
	}
	if len(results) == 0 {
		fmt.Println("no matches")
		return nil
	}
	for i, r := range results {
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
		fmt.Printf("%2d. %-28s %.4f  [%s]\n", i+1, truncate(displayID(r), 28), r.Score, strings.Join(found, " "))
		fmt.Printf("    %s\n", truncate(strings.ReplaceAll(r.Text, "\n", " "), 100))
	}
	return nil
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

var _ = sort.Strings
