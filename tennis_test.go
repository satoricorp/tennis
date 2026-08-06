package tennis

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satoricorp/tennis/embed"
)

// The tests point TENNIS_CACHE at a checked-out copy of the model so they
// exercise the real load path without a 123MB download on every run.
func TestMain(m *testing.M) {
	abs, err := filepath.Abs("testdata/cache")
	if err != nil {
		panic(err)
	}
	if _, err := os.Stat(filepath.Join(abs, "models", "potion-retrieval-32M", "model.safetensors")); err != nil {
		// Without weights there is nothing meaningful to test; say so rather
		// than failing with a confusing download error.
		println("skipping: test model not present at", abs)
		os.Exit(0)
	}
	os.Setenv("TENNIS_CACHE", abs)
	os.Exit(m.Run())
}

func openTest(t *testing.T) *DB {
	t.Helper()
	db, err := Open(filepath.Join(t.TempDir(), "test.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

var corpus = []Document{
	{ID: "a1", Text: "reverse a CLI that reads stdin and writes it back backwards", Attributes: map[string]any{"status": "merged", "cost": 3}},
	{ID: "a2", Text: "build a web scraper that paginates through search results", Attributes: map[string]any{"status": "failed", "cost": 12}},
	{ID: "a3", Text: "add retry with exponential backoff to the HTTP client", Attributes: map[string]any{"status": "merged", "cost": 5}},
	{ID: "a4", Text: "fix the flaky test in the auth module", Attributes: map[string]any{"status": "merged", "cost": 2}},
	{ID: "a5", Text: "write a parser for TOML configuration files", Attributes: map[string]any{"status": "open", "cost": 8}},
	{ID: "a6", Text: "make the login flow remember the user between sessions", Attributes: map[string]any{"status": "merged", "cost": 4}},
}

func seeded(t *testing.T) (*DB, *Namespace) {
	t.Helper()
	db := openTest(t)
	ns, err := db.CreateNamespace(context.Background(), "agents", NamespaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ns.Write(context.Background(), corpus); err != nil {
		t.Fatal(err)
	}
	return db, ns
}

func TestHybridBeatsEitherAlone(t *testing.T) {
	_, ns := seeded(t)
	ctx := context.Background()

	// "keep me signed in" shares no content word with the login document, so
	// only the semantic ranker can find it. This is the case that justifies
	// running two rankers at all.
	res, err := ns.Query(ctx, Query{Text: "keep me signed in", TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].ID != "a6" {
		t.Errorf("hybrid: want a6 first, got %v", ids(res))
	}

	kw, err := ns.Query(ctx, Query{Text: "keep me signed in", TopK: 3, Mode: Keyword})
	if err != nil {
		t.Fatal(err)
	}
	if len(kw) > 0 && kw[0].ID == "a6" {
		t.Log("keyword alone happened to find a6; the hybrid case is weaker than assumed")
	}

	// The mirror case: an exact rare token the semantic side blurs but BM25
	// nails.
	res, err = ns.Query(ctx, Query{Text: "TOML", TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].ID != "a5" {
		t.Errorf("exact term: want a5 first, got %v", ids(res))
	}
}

func TestFilters(t *testing.T) {
	_, ns := seeded(t)
	ctx := context.Background()

	res, err := ns.Query(ctx, Query{Text: "test auth login", TopK: 10, Filter: Eq("status", "merged")})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 {
		t.Fatal("filter returned nothing")
	}
	for _, r := range res {
		if r.Attributes["status"] != "merged" {
			t.Errorf("filter leaked %s with status %v", r.ID, r.Attributes["status"])
		}
	}

	res, err = ns.Query(ctx, Query{Text: "build scraper parser", TopK: 10, Filter: And(Gt("cost", 6), NotEq("status", "failed"))})
	if err != nil {
		t.Fatal(err)
	}
	for _, r := range res {
		if r.ID != "a5" {
			t.Errorf("compound filter returned unexpected %s", r.ID)
		}
	}

	// An attribute name that would break out of the JSON path must be refused,
	// not interpolated.
	if _, err := ns.Query(ctx, Query{Text: "x", Filter: Eq("bad') OR 1=1 --", 1)}); err == nil {
		t.Error("expected hostile attribute name to be rejected")
	}
}

// TestFTSStaysInSyncOnUpdateAndDelete is the regression test for the failure
// mode external-content FTS5 tables have by default: without triggers the index
// keeps serving text that no longer exists in the table.
func TestFTSStaysInSyncOnUpdateAndDelete(t *testing.T) {
	_, ns := seeded(t)
	ctx := context.Background()

	find := func(term string) []Result {
		r, err := ns.Query(ctx, Query{Text: term, TopK: 10, Mode: Keyword})
		if err != nil {
			t.Fatal(err)
		}
		return r
	}

	if got := find("TOML"); len(got) != 1 {
		t.Fatalf("setup: want 1 hit for TOML, got %v", ids(got))
	}

	// Overwrite the document. The old term must stop matching.
	if _, err := ns.Write(ctx, []Document{{ID: "a5", Text: "write a parser for YAML configuration files"}}); err != nil {
		t.Fatal(err)
	}
	if got := find("TOML"); len(got) != 0 {
		t.Errorf("after update: TOML should be gone from the index, got %v", ids(got))
	}
	if got := find("YAML"); len(got) != 1 {
		t.Errorf("after update: want 1 hit for YAML, got %v", ids(got))
	}

	// Delete it. Nothing should match.
	if n, err := ns.Delete(ctx, []string{"a5"}); err != nil || n != 1 {
		t.Fatalf("delete: n=%d err=%v", n, err)
	}
	if got := find("YAML"); len(got) != 0 {
		t.Errorf("after delete: index still returns %v", ids(got))
	}
}

func TestIncrementalWriteSkipsUnchanged(t *testing.T) {
	_, ns := seeded(t)
	ctx := context.Background()

	res, err := ns.Write(ctx, corpus)
	if err != nil {
		t.Fatal(err)
	}
	if res.Skipped != len(corpus) || res.Written != 0 {
		t.Errorf("rewriting identical docs: want all skipped, got written=%d skipped=%d", res.Written, res.Skipped)
	}

	changed := append([]Document{}, corpus...)
	changed[0].Text += " with tests"
	res, err = ns.Write(ctx, changed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Written != 1 || res.Skipped != len(corpus)-1 {
		t.Errorf("one changed doc: want written=1 skipped=%d, got written=%d skipped=%d",
			len(corpus)-1, res.Written, res.Skipped)
	}
}

// TestEmbedderBindingIsEnforced covers the design's central safety property.
// A namespace filled by one model must never be queried through another: the
// vectors are in different spaces, so the results would be plausible nonsense
// rather than an error.
func TestEmbedderBindingIsEnforced(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bind.sqlite")
	ctx := context.Background()

	db, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	ns, err := db.CreateNamespace(ctx, "agents", NamespaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	wantID := ns.EmbedderID()
	if _, err := ns.Write(ctx, corpus[:2]); err != nil {
		t.Fatal(err)
	}
	db.Close()

	// Reopen with a loader standing in for "a different model with the same
	// name" — the shape of an upgrade that silently changes vector meaning.
	db2, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer db2.Close()
	db2.modelLoader = func(ctx context.Context, name string, _ func(string)) (embed.Embedder, error) {
		return embedderStub{id: "builtin:some-other-model", dims: 256}, nil
	}
	if _, err := db2.Namespace(ctx, "agents"); err == nil {
		t.Fatal("expected a mismatched embedder to be refused")
	} else if !strings.Contains(err.Error(), wantID) {
		t.Errorf("error should name the bound embedder %q, got: %v", wantID, err)
	}
}

func TestNamespaceLifecycle(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	if _, err := db.CreateNamespace(ctx, "bad name!", NamespaceOptions{}); err == nil {
		t.Error("expected invalid namespace name to be refused")
	}
	if _, err := db.CreateNamespace(ctx, "agents", NamespaceOptions{}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.CreateNamespace(ctx, "agents", NamespaceOptions{}); err == nil {
		t.Error("expected duplicate namespace to be refused")
	}
	if _, err := db.Namespace(ctx, "nope"); err == nil {
		t.Error("expected unknown namespace to error")
	}

	infos, err := db.ListNamespaces(ctx)
	if err != nil || len(infos) != 1 || infos[0].Name != "agents" {
		t.Fatalf("list: %v %v", infos, err)
	}
	if err := db.DropNamespace(ctx, "agents"); err != nil {
		t.Fatal(err)
	}
	if infos, _ := db.ListNamespaces(ctx); len(infos) != 0 {
		t.Errorf("after drop: %v", infos)
	}
}

func TestChunking(t *testing.T) {
	long := strings.Repeat("The system processes records and writes output to disk. ", 80)
	chunks := chunkText(long, 500, 50)
	if len(chunks) < 2 {
		t.Fatalf("expected several chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 600 {
			t.Errorf("chunk %d is %d chars, well past the 500 target", i, len(c))
		}
	}
	if got := chunkText("short", 500, 50); len(got) != 1 || got[0] != "short" {
		t.Errorf("short text should be one chunk, got %v", got)
	}
	if got := chunkText("   ", 500, 50); got != nil {
		t.Errorf("blank text should produce no chunks, got %v", got)
	}
}

func TestLongDocumentIsFullySearchable(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	ns, err := db.CreateNamespace(ctx, "docs", NamespaceOptions{ChunkSize: 400, ChunkOverlap: 40})
	if err != nil {
		t.Fatal(err)
	}
	// A distinctive term buried far past where a 512-token transformer would
	// have truncated. Chunking is what keeps it findable.
	long := strings.Repeat("Routine boilerplate about configuration and defaults. ", 120) +
		" The pineapple sentinel marks the end of the document."
	if _, err := ns.Write(ctx, []Document{{ID: "big", Text: long}}); err != nil {
		t.Fatal(err)
	}
	res, err := ns.Query(ctx, Query{Text: "pineapple sentinel", TopK: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) == 0 || res[0].ID != "big" {
		t.Fatalf("buried term not found: %v", ids(res))
	}
	if !strings.Contains(res[0].Text, "pineapple") {
		t.Errorf("returned chunk should be the one containing the term, got: %q", res[0].Text)
	}
}

// embedderStub lets tests swap the model without loading weights.
type embedderStub struct {
	id   string
	dims int
}

func (e embedderStub) ID() string { return e.id }
func (e embedderStub) Dims() int  { return e.dims }
func (e embedderStub) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range texts {
		out[i] = make([]float32, e.dims)
	}
	return out, nil
}

func ids(rs []Result) []string {
	out := make([]string, len(rs))
	for i, r := range rs {
		out[i] = r.ID
	}
	return out
}

// Regression: pragmas used to be Exec'd after open, which configures only the
// one pool connection that ran them. Any other connection the pool opened had
// busy_timeout=0 and failed instantly under write contention. The DSN form is
// applied per connection by the driver; this test forces a second connection
// and checks it.
func TestPragmasApplyToEveryPoolConnection(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	c1, err := db.sql.Conn(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer c1.Close()
	c2, err := db.sql.Conn(ctx) // second live conn => pool must open a new one
	if err != nil {
		t.Fatal(err)
	}
	defer c2.Close()

	for name, c := range map[string]*sql.Conn{"conn1": c1, "conn2": c2} {
		var busy, fk int
		if err := c.QueryRowContext(ctx, "PRAGMA busy_timeout").Scan(&busy); err != nil {
			t.Fatal(err)
		}
		if err := c.QueryRowContext(ctx, "PRAGMA foreign_keys").Scan(&fk); err != nil {
			t.Fatal(err)
		}
		if busy != 5000 || fk != 1 {
			t.Errorf("%s: busy_timeout=%d foreign_keys=%d; pragmas did not reach this connection", name, busy, fk)
		}
	}
}

// Regression: auto-create-on-first-use must key on ErrNamespaceNotFound
// specifically. Before the sentinel existed, ANY open failure fell through to
// CreateNamespace, so "OPENAI_API_KEY is not set" surfaced as the baffling
// "namespace already exists".
func TestNamespaceNotFoundIsDistinguishable(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	_, err := db.Namespace(ctx, "missing")
	if !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("missing namespace should be ErrNamespaceNotFound, got: %v", err)
	}

	// A namespace that exists but cannot open must NOT look like "not found".
	if _, err := db.CreateNamespace(ctx, "agents", NamespaceOptions{}); err != nil {
		t.Fatal(err)
	}
	db.modelLoader = func(ctx context.Context, name string, _ func(string)) (embed.Embedder, error) {
		return nil, fmt.Errorf("model backend unavailable")
	}
	_, err = db.Namespace(ctx, "agents")
	if err == nil {
		t.Fatal("expected an error from the failing loader")
	}
	if errors.Is(err, ErrNamespaceNotFound) {
		t.Error("an open failure must not masquerade as not-found — that is the auto-create trap")
	}
}

// Regression: the FTS query builder used to keep only ASCII [a-z0-9_], which
// shredded accented words into garbage terms and reduced non-Latin queries to
// nothing — silently turning off the keyword half of the hybrid for every
// language but English. The index side (unicode61) always handled these fine.
func TestKeywordSearchSpeaksUnicode(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	ns, err := db.CreateNamespace(ctx, "docs", NamespaceOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ns.Write(ctx, []Document{
		{ID: "fr", Text: "un café et un résumé sur la terrasse"},
		{ID: "en", Text: "a coffee and a summary on the porch"},
	}); err != nil {
		t.Fatal(err)
	}
	res, err := ns.Query(ctx, Query{Text: "café", TopK: 2, Mode: Keyword})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 1 || res[0].ID != "fr" {
		t.Errorf(`keyword query "café": want [fr], got %v`, ids(res))
	}
}

// Regression: dropping a namespace that does not exist used to succeed
// silently — "dropped" printed for a typo, real namespace untouched.
func TestDropMissingNamespaceErrors(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()

	err := db.DropNamespace(ctx, "never-existed")
	if !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("want ErrNamespaceNotFound, got: %v", err)
	}

	// And a real drop still works exactly once.
	if _, err := db.CreateNamespace(ctx, "real", NamespaceOptions{}); err != nil {
		t.Fatal(err)
	}
	if err := db.DropNamespace(ctx, "real"); err != nil {
		t.Fatal(err)
	}
	if err := db.DropNamespace(ctx, "real"); !errors.Is(err, ErrNamespaceNotFound) {
		t.Errorf("second drop should be not-found, got: %v", err)
	}
}

// Regression: keyword candidates used to be limited by CHUNK count before
// collapsing to documents, while the semantic side counted documents. One
// chunky document could eat the whole keyword budget, quietly tilting every
// hybrid ranking toward whatever the semantic ranker liked.
func TestKeywordCandidatesCountDocumentsNotChunks(t *testing.T) {
	db := openTest(t)
	ctx := context.Background()
	ns, err := db.CreateNamespace(ctx, "docs", NamespaceOptions{ChunkSize: 60, ChunkOverlap: 0})
	if err != nil {
		t.Fatal(err)
	}
	// "big" produces many chunks that all contain the term; b and c one each.
	big := strings.Repeat("the zebra runs across the plain today. ", 20)
	if _, err := ns.Write(ctx, []Document{
		{ID: "big", Text: big},
		{ID: "b", Text: "a zebra sleeps in the shade"},
		{ID: "c", Text: "zebra stripes form a pattern"},
	}); err != nil {
		t.Fatal(err)
	}

	// Sanity: the setup really does give "big" more chunks than the depth.
	var chunkCount int
	if err := db.sql.QueryRow(`SELECT COUNT(*) FROM chunks WHERE ns='docs' AND doc_id='big'`).Scan(&chunkCount); err != nil {
		t.Fatal(err)
	}
	if chunkCount <= 3 {
		t.Fatalf("setup: big has only %d chunks; the regression needs more than the candidate depth", chunkCount)
	}

	res, err := ns.Query(ctx, Query{Text: "zebra", Mode: Keyword, TopK: 10, CandidateDepth: 3})
	if err != nil {
		t.Fatal(err)
	}
	if len(res) != 3 {
		t.Errorf("depth 3 must yield 3 DOCUMENTS, got %d: %v (old code: all 3 slots eaten by 'big')",
			len(res), ids(res))
	}
}
