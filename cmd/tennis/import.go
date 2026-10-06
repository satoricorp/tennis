package main

import (
	"archive/zip"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/satoricorp/tennis"
	"github.com/satoricorp/tennis/embed"
)

// The formats import knows how to read. "auto" is the default and the only
// one most people should ever type; the rest exist so a misdetected archive
// stays importable without waiting for a release.
const (
	formatAuto       = "auto"
	formatClaude     = "claude"
	formatChatGPT    = "chatgpt"
	formatClaudeCode = "claude-code"
	formatCodex      = "codex"
	formatFiles      = "files"
)

// Document granularity. A turn is the unit people actually remember ("what
// did it say about the timeout") and the unit yeet jumps to; a conversation
// is the unit people remember when the thread matters more than the line.
const (
	perTurn         = "turn"
	perConversation = "conversation"
)

// cmdImport ingests an existing archive of conversations — the history that
// already happened.
//
// seed reads files and put reads a program's output, and neither covers the
// case that matters when you are starting out: everything you said to an
// assistant before tennis existed. That history cannot be captured by pointing
// a client at a proxy or a different base URL, because those only ever see
// traffic from the moment they are configured; the only copy of the past is
// the export archive the vendor hands you. So import takes that archive
// directly — a zip, an unzipped directory, or a single transcript — sniffs
// which export it is, and turns it into documents.
func cmdImport(args []string) error {
	fs_ := flag.NewFlagSet("import", flag.ExitOnError)
	var o importOpts
	registerImportFlags(fs_, &o)
	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}
	if len(pos) < 2 {
		return fmt.Errorf("usage: tennis import <namespace> <path...>")
	}
	return runImport(pos[0], pos[1:], o)
}

// cmdAdd is the front door: the same machinery as import, spelled the way the
// source is spelled out loud. "--chatgpt" says what the archive is, which is
// the thing a person knows, where "--format chatgpt" asks them to know that
// tennis has a notion of formats first.
func cmdAdd(args []string) error {
	fs_ := flag.NewFlagSet("add", flag.ExitOnError)
	var o importOpts
	registerImportFlags(fs_, &o)
	nsName := fs_.String("ns", "", "namespace (default "+defaultNamespace+", or $TENNIS_NS)")

	sources := map[string]*bool{
		formatChatGPT:    fs_.Bool(formatChatGPT, false, "read the source as a ChatGPT export"),
		formatClaude:     fs_.Bool(formatClaude, false, "read the source as a Claude export"),
		formatClaudeCode: fs_.Bool(formatClaudeCode, false, "read the source as Claude Code transcripts"),
		formatCodex:      fs_.Bool(formatCodex, false, "read the source as Codex transcripts"),
		formatFiles:      fs_.Bool(formatFiles, false, "read the source as plain files"),
	}
	// --ndjson is a source like the others but not a format like the others:
	// it reads stdin rather than a path, and its documents are not
	// conversations, so it never reaches the archive readers at all.
	ndjson := fs_.Bool("ndjson", false, "read NDJSON documents from stdin, one per line")

	pos, err := parseInterleaved(fs_, args)
	if err != nil {
		return err
	}

	// Two source flags is not a request tennis can honour, and picking one
	// silently would import the archive as the wrong thing.
	var chosen []string
	for _, name := range sortedKeys(sources) {
		if *sources[name] {
			chosen = append(chosen, name)
		}
	}
	switch {
	case len(chosen) > 1:
		return fmt.Errorf("--%s and --%s name different sources; pick one", chosen[0], chosen[1])
	case len(chosen) == 1:
		if o.format != formatAuto && o.format != chosen[0] {
			return fmt.Errorf("--format %s contradicts --%s", o.format, chosen[0])
		}
		o.format = chosen[0]
	}

	if *ndjson {
		if len(chosen) > 0 {
			return fmt.Errorf("--ndjson and --%s name different sources; pick one", chosen[0])
		}
		if o.format != formatAuto {
			return fmt.Errorf("--format %s contradicts --ndjson", o.format)
		}
		// "-" is the conventional spelling of stdin and costs nothing to
		// accept. A real path is refused rather than read, because reading it
		// would silently ignore the stdin the flag promises.
		if len(pos) > 1 || (len(pos) == 1 && pos[0] != "-") {
			return fmt.Errorf("--ndjson reads stdin; drop the path (or spell it \"-\")")
		}
		return runNDJSON(resolveNS(*nsName), o)
	}

	if len(pos) < 1 {
		return fmt.Errorf("usage: tennis add [--chatgpt|--claude|--claude-code|--codex|--files|--ndjson] <path...>")
	}
	return runImport(resolveNS(*nsName), pos, o)
}

// importOpts is what import and add both collect; they differ only in how the
// namespace and the source are spelled.
type importOpts struct {
	dbPath      string
	asJSON      bool
	format      string
	per         string
	ext         string
	model       string
	openaiModel string
	chunkSize   int
	noCards     bool
	cardDir     string
}

func registerImportFlags(fs_ *flag.FlagSet, o *importOpts) {
	fs_.StringVar(&o.dbPath, "db", defaultDB(), "database file")
	fs_.BoolVar(&o.asJSON, "json", false, "machine-readable output")
	fs_.StringVar(&o.format, "format", formatAuto, "auto | chatgpt | claude | claude-code | codex | files")
	fs_.StringVar(&o.per, "per", perTurn, "one document per turn | conversation")
	fs_.StringVar(&o.ext, "ext", defaultExt, "only these extensions, comma-separated (default: everything tennis can read)")
	fs_.StringVar(&o.model, "model", "", "built-in model for a new namespace (default "+embed.DefaultModel+")")
	fs_.StringVar(&o.openaiModel, "openai", "", "use an OpenAI model instead of the built-in one (requires OPENAI_API_KEY)")
	fs_.IntVar(&o.chunkSize, "chunk", 0, "chunk size in characters for a new namespace")
	fs_.BoolVar(&o.noCards, "no-cards", false, "skip the readable markdown summaries")
	fs_.StringVar(&o.cardDir, "cards", defaultCardDir(), "where summary cards are written")
}

func runImport(nsName string, paths []string, o importOpts) error {
	dbPath, asJSON, format, per, ext := &o.dbPath, &o.asJSON, &o.format, &o.per, &o.ext
	model, openaiModel, chunkSize := &o.model, &o.openaiModel, &o.chunkSize

	if err := validFormat(*format); err != nil {
		return err
	}
	granularity, err := validPer(*per)
	if err != nil {
		return err
	}
	// Every path is checked before anything is created. A typo in an archive
	// name would otherwise leave an empty namespace behind, bound to an
	// embedder, for an import that never happened.
	for _, p := range paths {
		if _, err := os.Stat(p); err != nil {
			return err
		}
	}

	db, err := open(*dbPath, *asJSON)
	if err != nil {
		return err
	}
	defer db.Close()
	ctx := context.Background()

	ns, err := db.Namespace(ctx, nsName)
	switch {
	case errors.Is(err, tennis.ErrNamespaceNotFound):
		// Same ergonomics as seed and put: create on first use, bind the
		// embedder here, enforce it on every write and query after.
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
		return err
	}

	sink := &docSink{ctx: ctx, ns: ns}
	if !o.noCards {
		cards, err := newCardWriter(ctx, o.cardDir, nsName, newSummarizer(*asJSON))
		if err != nil {
			return err
		}
		sink.cards = cards
		defer cards.close()
	}
	if !*asJSON {
		sink.progress = func(done int) { fmt.Fprintf(os.Stderr, "tennis: %d documents in…\n", done) }
	}
	warn := func(msg string) { fmt.Fprintln(os.Stderr, "tennis: import:", msg) }

	var (
		reports      []map[string]any
		failed       int
		skippedFiles int
		skippedDirs  int
		pathErr      error
	)
	for _, p := range paths {
		rep, err := importPath(p, *format, granularity, *ext, sink, warn, *asJSON)
		if err != nil {
			// A bad path ends the run, but not retroactively. The paths are
			// independent of one another — the stat loop above already turned
			// away the ones that do not exist — so what the earlier ones read
			// is exactly as good as it would have been had this one not
			// failed, and the card writer has been filing summaries for it the
			// whole time. Returning from here with the batch unwritten would
			// leave those cards in ~/tennis describing conversations the index
			// never received. So the sink is flushed and the report printed
			// below like any other, and the error is what the run exits on.
			pathErr = err
			break
		}
		failed += rep["failed"].(int)
		if n, ok := rep["skipped_files"].(int); ok {
			skippedFiles += n
		}
		if n, ok := rep["skipped_dirs"].(int); ok {
			skippedDirs += n
		}
		reports = append(reports, rep)
	}
	if err := sink.flush(); err != nil {
		if pathErr != nil {
			return errors.Join(pathErr, err)
		}
		return err
	}
	sink.cards.close()

	// Nothing read, nothing to report. An "imported 0" line ahead of the error
	// would dress a one-path mistake up as a partial success.
	if pathErr != nil && len(reports) == 0 && sink.count() == 0 {
		return pathErr
	}

	if *asJSON {
		out := map[string]any{
			"sources": reports,
			"written": sink.res.Written, "skipped": sink.res.Skipped,
			"chunks": sink.res.Chunks, "failed": failed, "skipped_files": skippedFiles,
			"skipped_dirs": skippedDirs,
		}
		if sink.cards != nil {
			out["cards"] = sink.cards.written
			out["cards_unsummarized"] = sink.cards.failed
			out["cards_unchanged"] = sink.cards.unchanged
		}
		if pathErr != nil {
			// sources lists only what went in, so without this a reader of
			// stdout alone would see a clean run.
			out["error"] = pathErr.Error()
		}
		if err := emit(out); err != nil {
			return err
		}
	} else {
		fmt.Printf("imported %d, skipped %d unchanged, %d chunks in %q",
			sink.res.Written, sink.res.Skipped, sink.res.Chunks, nsName)
		if sink.cards != nil && sink.cards.written > 0 {
			fmt.Printf(", %d cards in %s", sink.cards.written, sink.cards.dir)
		}
		if skippedFiles > 0 {
			fmt.Printf(" (%s skipped)", plural(skippedFiles, "file"))
		}
		if skippedDirs > 0 {
			fmt.Printf(" (%s passed over)", plural(skippedDirs, "folder"))
		}
		if failed > 0 {
			fmt.Printf(" (%d failed)", failed)
		}
		fmt.Println()
	}
	if pathErr != nil {
		return pathErr
	}
	if failed > 0 {
		return fmt.Errorf("%d item(s) failed to import", failed)
	}
	return nil
}

func validFormat(f string) error {
	switch f {
	case formatAuto, formatClaude, formatChatGPT, formatClaudeCode, formatCodex, formatFiles:
		return nil
	}
	return fmt.Errorf("unknown --format %q (want auto, chatgpt, claude, claude-code, codex, files)", f)
}

func validPer(p string) (string, error) {
	switch p {
	case perTurn, "message":
		return perTurn, nil
	case perConversation, "session":
		return perConversation, nil
	}
	return "", fmt.Errorf("unknown --per %q (want turn or conversation)", p)
}

// importPath opens one source, decides what it is, and runs the matching
// adapter. The report it returns is both the --json record and the counter the
// exit code is built from.
func importPath(p, format, per, ext string, sink *docSink, warn func(string), quiet bool) (map[string]any, error) {
	a, err := openArchive(p)
	if err != nil {
		return nil, err
	}
	defer a.Close()

	pl, err := a.detect(format)
	if err != nil {
		return nil, err
	}
	if !quiet {
		fmt.Fprintf(os.Stderr, "tennis: %s: reading %s\n", a.display, pl.describe())
	}

	before := sink.count()
	var (
		conversations int
		failed        int
		skippedFiles  int
		skippedDirs   int
	)
	switch pl.format {
	case formatChatGPT, formatClaude:
		f, err := a.open(pl.payload)
		if err != nil {
			return nil, err
		}
		if pl.format == formatChatGPT {
			conversations, failed, err = importChatGPT(f, per, sink, warn)
		} else {
			conversations, failed, err = importClaude(f, per, sink, warn)
		}
		f.Close()
		if err != nil {
			return nil, err
		}
		// A Claude export carries project knowledge files alongside the chats.
		// They are the same kind of thing — text you wrote and want back — and
		// dropping them silently would make "I imported my export" untrue.
		if pl.format == formatClaude && pl.extra != "" {
			f, err := a.open(pl.extra)
			if err != nil {
				return nil, err
			}
			n, bad, err := importClaudeProjects(f, sink, warn)
			f.Close()
			if err != nil {
				return nil, err
			}
			conversations += n
			failed += bad
		}

	case formatClaudeCode:
		conversations, failed, err = importClaudeCode(a, per, sink, warn)
		if err != nil {
			return nil, err
		}

	case formatCodex:
		conversations, failed, err = importCodex(a, per, sink, warn)
		if err != nil {
			return nil, err
		}

	case formatFiles:
		skippedFiles, skippedDirs, err = importFiles(a, ext, sink, warn)
		if err != nil {
			return nil, err
		}
	}

	docs := sink.count() - before
	if docs == 0 && failed == 0 && format == formatAuto && (pl.format == formatClaudeCode || pl.format == formatCodex) {
		// Detection settles on the first JSONL that looks like a transcript,
		// and a folder of notes can hold one that only looks the part — an
		// event log, a fixture. When nothing in the source turned out to be a
		// conversation, the guess was wrong rather than the source empty, so
		// it is read the way any other folder is. Named explicitly, the
		// format is taken at its word and the error below stands.
		if !quiet {
			fmt.Fprintf(os.Stderr, "tennis: %s: no transcripts after all; reading plain files\n", a.display)
		}
		pl = plan{format: formatFiles}
		skippedFiles, skippedDirs, err = importFiles(a, ext, sink, warn)
		if err != nil {
			return nil, err
		}
		docs = sink.count() - before
	}
	if docs == 0 && failed == 0 {
		// Silence here would look like success. It never is: either the archive
		// is not what it looked like, or the filter excluded everything in it.
		if pl.format == formatFiles {
			return nil, fmt.Errorf("no indexable files in %s (looking for %s, %s)", a.display, lookingFor(ext), passedOverNote(skippedFiles, skippedDirs))
		}
		return nil, fmt.Errorf("nothing to import from %s (read as %s)", a.display, pl.format)
	}
	rep := map[string]any{
		"path": a.display, "format": pl.format,
		"conversations": conversations, "documents": docs, "failed": failed,
	}
	if skippedFiles > 0 {
		rep["skipped_files"] = skippedFiles
	}
	if skippedDirs > 0 {
		rep["skipped_dirs"] = skippedDirs
	}
	return rep, nil
}

// --- the source ------------------------------------------------------------

// archive is an opened import source — a zip, a directory, or a single file —
// presented as one walkable filesystem, so the format adapters never have to
// care which of the three they were handed.
type archive struct {
	display    string // the path the user typed, for messages
	root       string // absolute zip path, or absolute directory, for document IDs
	isZip      bool
	fsys       fs.FS
	entries    []archiveEntry
	passedOver int // folders a plain-files import leaves out, each counted once (see walk)
	closer     io.Closer
}

type archiveEntry struct {
	path string // slash-separated, relative to fsys
	size int64
	mod  time.Time
	// ignored is what a .gitignore in the source names that covers this
	// file — the file itself, or a folder it is in — or "" for nothing.
	ignored string
}

func openArchive(p string) (*archive, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return nil, err
	}
	info, err := os.Stat(abs)
	if err != nil {
		return nil, err
	}

	switch {
	case info.IsDir() && isDocument(abs):
		// The folder a person names is read as a folder — unless a Mac
		// would call it a file, and then it is the one document it is.
		size, mod := bundleStat(os.DirFS(abs), ".")
		return &archive{
			display: p,
			root:    filepath.Dir(abs),
			fsys:    os.DirFS(filepath.Dir(abs)),
			entries: []archiveEntry{{path: filepath.Base(abs), size: size, mod: mod}},
		}, nil

	case info.IsDir():
		a := &archive{display: p, root: abs, fsys: os.DirFS(abs)}
		a.walk()
		return a, nil

	case strings.EqualFold(filepath.Ext(abs), ".zip"):
		zr, err := zip.OpenReader(abs)
		if err != nil {
			return nil, fmt.Errorf("opening %s: %w", p, err)
		}
		a := &archive{display: p, root: abs, isZip: true, fsys: zr, closer: zr}
		a.walk()
		return a, nil

	default:
		// One file: a conversations.json already unzipped, or a single session
		// transcript. Scoping the filesystem to its directory and keeping the
		// one entry gives the adapters the same shape as a real archive.
		return &archive{
			display: p,
			root:    filepath.Dir(abs),
			fsys:    os.DirFS(filepath.Dir(abs)),
			entries: []archiveEntry{{path: filepath.Base(abs), size: info.Size(), mod: info.ModTime()}},
		}, nil
	}
}

func (a *archive) Close() error {
	if a.closer != nil {
		return a.closer.Close()
	}
	return nil
}

// walk lists every readable file once. Entries that cannot be stat'd are
// dropped rather than fatal: a single unreadable file in a 20,000-file export
// is not a reason to import none of it.
//
// Some folders are not entered at all: those passOver names, since no
// export or transcript lives in one; the bundles that are no document; and
// a link to a folder, which could lead anywhere, the folder being walked
// among them. A document saved as a folder is listed as the one file it is.
// What a .gitignore says is only noted on each entry, for importFiles to act
// on, because the agent readers must not: a ~/.claude kept in git may well
// ignore its transcripts, and those are what reading it is for.
//
// passedOver counts, once each, the folders a plain-files import leaves out:
// those not entered, and the outermost of those a .gitignore names, which
// are entered only for the agent readers' sake. A folder inside one already
// counted is not counted again, and nor is one under a dot directory, which
// nobody adding a folder meant.
func (a *archive) walk() {
	ig := newIgnores(a.fsys)
	prune := func(p string) {
		if why := ig.check(p, true); (why == "" || why == p) && !hidden(p) {
			a.passedOver++
		}
	}
	fs.WalkDir(a.fsys, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d == nil {
			return nil
		}
		if d.IsDir() {
			isBundle, document := bundle(d.Name())
			switch {
			case p == ".":
				ig.dir(p)
			case isBundle && document && !a.isZip:
				size, mod := bundleStat(a.fsys, p)
				a.entries = append(a.entries, archiveEntry{path: p, size: size, mod: mod, ignored: ig.check(p, true)})
				return fs.SkipDir
			case isBundle || passOver[d.Name()]:
				prune(p)
				return fs.SkipDir
			default:
				if why := ig.dir(p); why == p && !hidden(p) {
					a.passedOver++
				}
			}
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			// A link is read as what it points to, so it is measured as
			// that too: the link's own size is the length of the path in
			// it, and would let any file past the caps. One that points
			// nowhere keeps its own, and says so when it is read.
			if target, err := fs.Stat(a.fsys, p); err == nil {
				if target.IsDir() {
					prune(p)
					return nil
				}
				info = target
			}
		}
		a.entries = append(a.entries, archiveEntry{path: p, size: info.Size(), mod: info.ModTime(), ignored: ig.file(p)})
		return nil
	})
}

// isDocument reports whether a folder is a document saved as one (see
// bundles).
func isDocument(name string) bool {
	isBundle, document := bundle(name)
	return isBundle && document
}

// bundleStat measures a document saved as a folder: the size of everything
// in it, and when any of it last changed. The folder's own time moves only
// when something is added to its top level or taken from it, and an edit
// to a Pages document rewrites a file further down.
func bundleStat(fsys fs.FS, p string) (size int64, mod time.Time) {
	fs.WalkDir(fsys, p, func(_ string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		info, err := d.Info()
		if err != nil {
			return nil
		}
		if !d.IsDir() {
			size += info.Size()
		}
		if info.ModTime().After(mod) {
			mod = info.ModTime()
		}
		return nil
	})
	return size, mod
}

func (a *archive) open(p string) (fs.File, error) {
	f, err := a.fsys.Open(p)
	if err != nil {
		return nil, fmt.Errorf("%s: reading %s: %w", a.display, p, err)
	}
	return f, nil
}

// docID is the stable identity of a plain file inside this source. A directory
// yields the same absolute path seed would produce, so importing a directory
// and later seeding it updates one document instead of storing two.
func (a *archive) docID(p string) string {
	if a.isZip {
		return a.root + "!" + p
	}
	return filepath.Join(a.root, filepath.FromSlash(p))
}

// find returns the shallowest entry with the given base name, so a payload at
// the archive root wins over a copy nested in a backup folder.
func (a *archive) find(name string) string {
	best, bestDepth := "", 1<<30
	for _, e := range a.entries {
		if !strings.EqualFold(path.Base(e.path), name) {
			continue
		}
		if d := strings.Count(e.path, "/"); d < bestDepth {
			best, bestDepth = e.path, d
		}
	}
	return best
}

// --- detection -------------------------------------------------------------

// plan is what detection decided: the adapter to run and where its payload
// lives inside the source.
type plan struct {
	format  string
	payload string // the conversations.json this format was found in
	extra   string // a Claude export's projects.json, when present
}

func (p plan) describe() string {
	switch p.format {
	case formatChatGPT:
		return "a ChatGPT export (" + p.payload + ")"
	case formatClaude:
		if p.extra != "" {
			return "a Claude export (" + p.payload + ", " + p.extra + ")"
		}
		return "a Claude export (" + p.payload + ")"
	case formatClaudeCode:
		return "Claude Code session transcripts"
	case formatCodex:
		return "Codex session transcripts"
	default:
		return "plain files"
	}
}

// detect sniffs the source unless --format already answered the question. An
// explicit format still has to locate the payload, so both paths converge.
func (a *archive) detect(want string) (plan, error) {
	switch want {
	case formatChatGPT, formatClaude:
		p := a.find("conversations.json")
		if p == "" {
			return plan{}, fmt.Errorf("%s: no conversations.json in this %s", a.display, sourceKind(a))
		}
		return plan{format: want, payload: p, extra: a.find("projects.json")}, nil
	case formatClaudeCode, formatCodex, formatFiles:
		return plan{format: want}, nil
	}

	if p := a.find("conversations.json"); p != "" {
		f, err := a.open(p)
		if err != nil {
			return plan{}, err
		}
		shape, sniffErr := sniffConversations(f)
		f.Close()
		if sniffErr != nil {
			return plan{}, fmt.Errorf("%s: %s does not look like a chat export: %w", a.display, p, sniffErr)
		}
		return plan{format: shape, payload: p, extra: a.find("projects.json")}, nil
	}

	// Both agent transcripts are directories of JSONL, so telling them apart
	// means reading one — and not simply the first one met. A real ~/.codex
	// or ~/.claude keeps a history.jsonl at its root that is in neither
	// shape, and the walk is lexical, so that file comes before any
	// transcript. Keep going past what neither sniffer claims, within a
	// bound that keeps a tree of unrelated JSONL cheap. Each sniff gets its
	// own handle because sniffing consumes the reader.
	//
	// The first file claimed decides the format for the whole source, which
	// is why the sniffers want a line that is a conversation record and not
	// merely a line with an id in it — and why importPath reads the source
	// as plain files after all when the claim yields no conversation.
	sniffed := 0
	for _, e := range a.entries {
		if !strings.EqualFold(path.Ext(e.path), ".jsonl") {
			continue
		}
		if sniffed++; sniffed > maxDetectSniffs {
			break
		}
		for _, s := range []struct {
			format string
			sniff  func(io.Reader) bool
		}{
			{formatClaudeCode, sniffClaudeCode},
			{formatCodex, sniffCodex},
		} {
			f, err := a.open(e.path)
			if err != nil {
				continue
			}
			ok := s.sniff(f)
			f.Close()
			if ok {
				return plan{format: s.format}, nil
			}
		}
	}

	return plan{format: formatFiles}, nil
}

func sourceKind(a *archive) string {
	if a.isZip {
		return "zip"
	}
	return "directory"
}

// sniffConversations reads only the first element of the array, which is
// enough to tell the two exports apart and cheap on a 300MB file: ChatGPT
// stores a message graph under "mapping", Claude a flat "chat_messages" list.
func sniffConversations(r io.Reader) (string, error) {
	dec := json.NewDecoder(r)
	tok, err := dec.Token()
	if err != nil {
		return "", err
	}
	if d, ok := tok.(json.Delim); !ok || d != '[' {
		return "", fmt.Errorf("expected a JSON array of conversations")
	}
	if !dec.More() {
		return "", fmt.Errorf("the array is empty")
	}
	var first map[string]json.RawMessage
	if err := dec.Decode(&first); err != nil {
		return "", err
	}
	switch {
	case first["mapping"] != nil:
		return formatChatGPT, nil
	case first["chat_messages"] != nil:
		return formatClaude, nil
	}
	return "", fmt.Errorf(`no "mapping" (ChatGPT) or "chat_messages" (Claude) in the first conversation`)
}

// sniffClaudeCode looks for a line that is a conversation record: one that
// carries a message with a role, or that is typed as a turn and has the uuid
// every turn is filed under. Either field alone is common in JSONL that is no
// transcript at all — an event log keyed by uuid, or the history.jsonl beside
// the transcripts, which may carry a sessionId but never a message — and a
// match here decides the format for the whole source.
//
// It reads several lines rather than one because a transcript opens with
// bookkeeping (queue operations, the permission mode, a file-history
// snapshot, a title) before the first turn.
func sniffClaudeCode(r io.Reader) bool {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxImportLineSize)
	for i := 0; i < maxSniffLines && sc.Scan(); i++ {
		var probe struct {
			Type      string `json:"type"`
			UUID      string `json:"uuid"`
			SessionID string `json:"sessionId"`
			Summary   string `json:"summary"`
			LeafUUID  string `json:"leafUuid"`
			Message   *struct {
				Role string `json:"role"`
			} `json:"message"`
		}
		if json.Unmarshal(sc.Bytes(), &probe) != nil {
			continue
		}
		if probe.SessionID != "" && probe.Message != nil && probe.Message.Role != "" {
			return true
		}
		switch probe.Type {
		case "user", "assistant", "system":
			if probe.UUID != "" {
				return true
			}
		case "summary":
			// The summary a later session writes at the top of an earlier
			// one's file points at the turn it ends on, and has no uuid of its
			// own.
			if probe.UUID != "" || (probe.Summary != "" && probe.LeafUUID != "") {
				return true
			}
		}
	}
	return false
}

// sniffCodex looks for the envelope every rollout line is wrapped in: a
// timestamp, a type from the rollout's own few, and an object payload. The
// type and payload alone are a shape plenty of event logs share; the
// timestamp beside them is what every rollout line has and they often do not.
// Codex records no uuid or message role, which is what sniffClaudeCode keys
// on, so the two never claim the same file.
func sniffCodex(r io.Reader) bool {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), maxImportLineSize)
	for i := 0; i < maxSniffLines && sc.Scan(); i++ {
		var probe struct {
			Timestamp string          `json:"timestamp"`
			Type      string          `json:"type"`
			Payload   json.RawMessage `json:"payload"`
		}
		if json.Unmarshal(sc.Bytes(), &probe) != nil {
			continue
		}
		if probe.Timestamp == "" || len(probe.Payload) == 0 || probe.Payload[0] != '{' {
			continue
		}
		switch probe.Type {
		case "session_meta", "response_item", "event_msg", "turn_context":
			return true
		}
	}
	return false
}

// maxSniffLines is how far into a JSONL file a sniffer reads for a line in
// its shape. A real Claude Code transcript puts up to a handful of
// bookkeeping records ahead of its first turn.
const maxSniffLines = 20

// maxImportLineSize bounds one transcript line. Agent transcripts embed whole
// file reads and command output, so the ceiling is well above what a message
// needs — it exists to stop a corrupt file from being read as one line.
const maxImportLineSize = 32 << 20

// maxDetectSniffs bounds how many JSONL files detection reads before giving
// up on the agent formats. The transcripts in a real ~/.codex or ~/.claude
// sit behind a handful of files in other shapes, so the bound is generous
// for them while a directory full of unrelated JSONL is skimmed, not read.
const maxDetectSniffs = 50

// --- the plain-files fallback ----------------------------------------------

// importFiles is what an archive that is not a chat export gets: every file
// in it that holds text, read by whatever reads its kind (see readers.go),
// indexed, and given a card the way every conversation gets one — except what
// a .gitignore in it names, which is what git would not keep either. It
// returns how many files and folders it passed over, which is reported but
// never fatal: declining to index a photo, or a build folder, is the tool
// working.
//
// Reading runs on a few workers because the readers that call out to the
// system take a few hundred milliseconds a file, and a folder of PDFs read
// one at a time would be minutes of silence.
func importFiles(a *archive, ext string, sink *docSink, warn func(string)) (skipped, skippedDirs int, err error) {
	wanted := extSet(ext)
	var todo []archiveEntry
	for _, e := range a.entries {
		switch {
		case hidden(e.path) || (len(wanted) > 0 && !wanted[strings.ToLower(path.Ext(e.path))]):
			// Not what anyone meant, or not what they asked for: not counted.
		case e.ignored == e.path:
			skipped++
		case e.ignored != "":
			// In a folder a .gitignore names, which the walk counted.
		default:
			todo = append(todo, e)
		}
	}
	skippedDirs = a.passedOver

	type result struct {
		e    archiveEntry
		text string
		err  error
	}
	jobs := make(chan archiveEntry)
	results := make(chan result)
	var wg sync.WaitGroup
	for i := 0; i < readConcurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for e := range jobs {
				text, err := fileText(a.file(e))
				results <- result{e, text, err}
			}
		}()
	}
	go func() {
		for _, e := range todo {
			jobs <- e
		}
		close(jobs)
		wg.Wait()
		close(results)
	}()

	for r := range results {
		if r.err != nil {
			skipped++
			if !errors.Is(r.err, errNotText) {
				warn(fmt.Sprintf("skipping %s (%v)", r.e.path, r.err))
			}
			continue
		}
		id := a.docID(r.e.path)
		attrs := map[string]any{
			"kind": "file", "path": id, "name": path.Base(r.e.path), "size": r.e.size,
		}
		if !r.e.mod.IsZero() {
			attrs["modified"] = r.e.mod.UTC().Format(time.RFC3339)
		}
		sink.cards.add(fileCard(id, path.Base(r.e.path), r.text, r.e.mod, r.e.size))
		if err := sink.add(tennis.Document{ID: id, Text: r.text, Attributes: attrs}); err != nil {
			// Let the readers finish what they have in hand rather than leave
			// them blocked on a channel nobody reads.
			go func() {
				for range results {
				}
			}()
			return skipped, skippedDirs, err
		}
	}
	return skipped, skippedDirs, nil
}

// readConcurrency is how many files are read at once. The built-in readers
// are fast and the external ones are a process each; four keeps a laptop
// responsive either way.
const readConcurrency = 4

// file presents an entry to the readers. A directory entry has a path on
// disk; a zip member has only a way to be opened.
func (a *archive) file(e archiveEntry) fileEntry {
	f := fileEntry{name: path.Base(e.path), size: e.size, open: func() (io.ReadCloser, error) { return a.open(e.path) }}
	if !a.isZip {
		f.path = filepath.Join(a.root, filepath.FromSlash(e.path))
	}
	return f
}

// passedOverNote says what a folder walk left out, for the error that it
// found nothing to index.
func passedOverNote(files, dirs int) string {
	note := fmt.Sprintf("%d skipped", files)
	if dirs > 0 {
		note += ", " + plural(dirs, "folder") + " passed over"
	}
	return note
}

// lookingFor says what the files filter was, for the error that nothing
// matched it.
func lookingFor(ext string) string {
	if strings.TrimSpace(ext) == "" {
		return "any file with text"
	}
	return ext
}

// --- the sink --------------------------------------------------------------

// docSink batches documents into ns.Write. An export is not a handful of
// files — a year of history is tens of thousands of messages — so documents
// stream through fixed-size batches instead of accumulating until the end,
// and the caller sees progress while it happens.
type docSink struct {
	ctx      context.Context
	ns       *tennis.Namespace
	batch    []tennis.Document
	res      tennis.WriteResult
	progress func(done int)
	noted    int

	// capture, when set, receives documents instead of the database. The
	// adapter tests are about what a document should look like, and have no
	// business downloading a 123MB model to find out.
	capture  func(id, text string, attrs map[string]any)
	captured int

	// cards, when set, receives each conversation and each indexed file once.
	// A conversation's card is independent of granularity, since it describes
	// the conversation rather than the documents it was split into.
	cards *cardWriter
}

const (
	importBatchSize     = 500
	importProgressEvery = 5000
)

func (s *docSink) add(d tennis.Document) error {
	if s.capture != nil {
		s.capture(d.ID, d.Text, d.Attributes)
		s.captured++
		return nil
	}
	s.batch = append(s.batch, d)
	if len(s.batch) >= importBatchSize {
		return s.flush()
	}
	return nil
}

func (s *docSink) flush() error {
	if len(s.batch) == 0 {
		return nil
	}
	r, err := s.ns.Write(s.ctx, s.batch)
	if err != nil {
		return err
	}
	s.res.Written += r.Written
	s.res.Skipped += r.Skipped
	s.res.Chunks += r.Chunks
	s.batch = s.batch[:0]

	if done := s.count(); s.progress != nil && done-s.noted >= importProgressEvery {
		s.noted = done
		s.progress(done)
	}
	return nil
}

// count is documents accepted so far, written and skipped alike, including the
// batch still in hand.
func (s *docSink) count() int {
	return s.res.Written + s.res.Skipped + len(s.batch) + s.captured
}

// --- the normalized shape every chat format lands in ------------------------

// turn is one message, after its format's quirks have been dealt with.
type turn struct {
	id      string
	role    string
	text    string
	created string // RFC3339, empty when the export omits it
}

// conversation is one thread. Keeping the parsers' output in this shape is
// what lets --per, the ID scheme, and the attribute contract be written once
// instead of three times.
type conversation struct {
	source string // chatgpt | claude | claude-code
	id     string
	title  string
	create string
	extra  map[string]any // per-source attributes: project, cwd, branch
	turns  []turn
	// subagent names the subagent whose run this is, for a Claude Code
	// subagent's transcript, and is empty for a session's own. The run is
	// filed under the session that started it, so id is that session's.
	subagent string
}

// emit writes a conversation out at the requested granularity.
//
// The attribute contract is the same one put documents already follow, because
// the consumer is the same: kind and session on every hit are what a caller
// needs to render a result and jump back to where it came from.
func (c conversation) emit(per string, sink *docSink) error {
	if len(c.turns) == 0 {
		return nil
	}
	// A subagent's run is part of its session, which the session's own card
	// describes. A card of its own would be a second card for one session.
	if c.subagent == "" {
		sink.cards.add(c.card())
	}
	if per == perConversation {
		// A session and each of its subagents are a document apiece, which
		// one ID for all of them would have written over one another.
		id := c.source + ":" + c.id
		if c.subagent != "" {
			id += ":" + c.subagent
		}
		var b strings.Builder
		if c.title != "" {
			b.WriteString("# " + c.title + "\n\n")
		}
		for _, t := range c.turns {
			b.WriteString("## " + t.role + "\n\n" + t.text + "\n\n")
		}
		attrs := c.baseAttrs()
		attrs["kind"] = "conversation"
		attrs["messages"] = len(c.turns)
		return sink.add(tennis.Document{
			ID:         id,
			Text:       strings.TrimSpace(b.String()),
			Attributes: attrs,
		})
	}

	// Document IDs must be unique or a later turn silently overwrites an
	// earlier one. Message IDs are unique in practice, so the suffix is a
	// backstop for hand-edited and partial exports rather than the normal path.
	used := make(map[string]bool, len(c.turns))
	for i, t := range c.turns {
		id := c.source + ":" + c.id + ":" + t.id
		if used[id] {
			id += "#" + strconv.Itoa(i)
		}
		used[id] = true

		attrs := c.baseAttrs()
		attrs["kind"] = "message"
		attrs["role"] = t.role
		attrs["index"] = i
		if t.created != "" {
			attrs["created"] = t.created
		}
		if err := sink.add(tennis.Document{ID: id, Text: t.text, Attributes: attrs}); err != nil {
			return err
		}
	}
	return nil
}

func (c conversation) baseAttrs() map[string]any {
	attrs := map[string]any{"source": c.source, "session": c.id}
	if c.title != "" {
		attrs["title"] = c.title
	}
	if c.create != "" {
		attrs["created"] = c.create
	}
	for k, v := range c.extra {
		attrs[k] = v
	}
	if c.subagent != "" {
		attrs["subagent"] = c.subagent
	}
	return attrs
}

// normalizeTime puts every export's idea of a timestamp into one comparable
// form, so `--where created>2025` means the same thing whichever archive the
// document came from. An unparseable value is kept verbatim rather than
// dropped — wrong-looking is recoverable, missing is not.
func normalizeTime(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02 15:04:05"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC().Format(time.RFC3339)
		}
	}
	return s
}

// epochTime converts the fractional Unix seconds ChatGPT exports use.
func epochTime(sec *float64) string {
	if sec == nil || *sec <= 0 {
		return ""
	}
	whole, frac := int64(*sec), *sec-float64(int64(*sec))
	return time.Unix(whole, int64(frac*1e9)).UTC().Format(time.RFC3339)
}

// sortedKeys keeps every traversal deterministic, so two imports of the same
// archive produce identical documents and the unchanged-skip actually fires.
func sortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
