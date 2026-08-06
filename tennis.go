// Package tennis is a local hybrid search engine: keyword ranking and semantic
// ranking over one SQLite file, fused into a single result list.
//
// It has no server, no extension to load, and no runtime dependencies. The
// embedding model is a static lookup table rather than a neural network, so
// there is nothing to install and nothing to call out to.
//
//	db, _ := tennis.Open("~/.tennis/db.sqlite")
//	ns, _ := db.CreateNamespace(ctx, "agents", tennis.NamespaceOptions{})
//	ns.Write(ctx, []tennis.Document{{ID: "a1", Text: "..."}})
//	res, _ := ns.Query(ctx, tennis.Query{Text: "keep me signed in", TopK: 10})
package tennis

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/joelachance/tennis/embed"
	_ "modernc.org/sqlite" // pure-Go driver: no cgo, so the binary stays static
)

// ErrNamespaceNotFound reports that a namespace does not exist. Callers that
// auto-create on first use must check for this with errors.Is rather than
// treating any Namespace() error as "missing" — a namespace can also fail to
// open because its bound embedder cannot be reconstructed (an OpenAI namespace
// without its key, say), and answering that with "already exists" from a
// doomed create hides the actual problem.
var ErrNamespaceNotFound = errors.New("namespace does not exist")

// DB is a tennis database. It is safe for concurrent use.
type DB struct {
	sql  *sql.DB
	path string

	// modelLoader is swapped in tests to avoid a 123MB download.
	modelLoader func(ctx context.Context, name string, progress func(string)) (embed.Embedder, error)

	// Progress, if set, receives human-readable status for slow operations
	// such as a first-run model download.
	Progress func(string)
}

// nsNamePattern keeps namespace names to things that are unambiguous in a CLI,
// a URL path, and a filename, since a namespace shows up in all three.
var nsNamePattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,63}$`)

// Open opens or creates a tennis database. A leading ~ is expanded, and parent
// directories are created.
func Open(path string) (*DB, error) {
	expanded, err := expandPath(path)
	if err != nil {
		return nil, err
	}
	if dir := filepath.Dir(expanded); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return nil, err
		}
	}

	// Pragmas ride in the DSN, NOT in Exec calls after opening. database/sql is
	// a connection pool, and an Exec'd PRAGMA configures only whichever
	// connection ran it — every other connection the pool opens would have
	// busy_timeout=0 and fail instantly under write contention instead of
	// waiting. The DSN form is applied by the driver to each new connection.
	//
	// WAL lets a query run while an index is being written, which matters
	// because seeding a large corpus is slow and search should stay available
	// during it.
	dsn := "file:" + expanded +
		"?_pragma=busy_timeout(5000)" +
		"&_pragma=journal_mode(WAL)" +
		"&_pragma=synchronous(NORMAL)" +
		"&_pragma=foreign_keys(1)"
	sqlDB, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, err
	}
	for _, stmt := range append(append([]string{}, schemaStatements...), triggerStatements...) {
		if _, err := sqlDB.Exec(stmt); err != nil {
			sqlDB.Close()
			return nil, fmt.Errorf("creating schema: %w", err)
		}
	}

	db := &DB{sql: sqlDB, path: expanded}
	db.modelLoader = func(ctx context.Context, name string, progress func(string)) (embed.Embedder, error) {
		return embed.Load(ctx, name, progress)
	}
	return db, nil
}

// Close releases the database.
func (d *DB) Close() error { return d.sql.Close() }

// Path is the file backing this database.
func (d *DB) Path() string { return d.path }

// NamespaceOptions configures a namespace at creation. Every field here is
// permanent for the life of the namespace: they determine what the stored
// vectors mean, so changing one would invalidate everything already indexed.
type NamespaceOptions struct {
	// Model is the built-in static model to use. Defaults to
	// embed.DefaultModel. Ignored when OpenAIModel is set.
	Model string

	// OpenAIModel switches this namespace to the OpenAI API. It must be set
	// explicitly — tennis never reads OPENAI_API_KEY to decide this for you,
	// because a namespace whose embedder depends on ambient environment
	// returns silently wrong results the first time that environment differs.
	OpenAIModel string

	// ChunkSize is the target chunk width in characters (default 1000), and
	// ChunkOverlap how much consecutive chunks share (default 100), so a match
	// spanning a boundary is still found whole.
	ChunkSize    int
	ChunkOverlap int
}

// CreateNamespace creates a namespace and binds an embedder to it permanently.
//
// The binding is the important part. The embedder's ID and dimension are
// written to the namespace row, and every later Write and Query verifies the
// live embedder against them. Vectors from two different models are not
// comparable — a cosine between them is noise that looks like a score — so the
// mismatch has to be an error rather than a degradation.
func (d *DB) CreateNamespace(ctx context.Context, name string, opts NamespaceOptions) (*Namespace, error) {
	if !nsNamePattern.MatchString(name) {
		return nil, fmt.Errorf("invalid namespace name %q: use letters, digits, dash and underscore (max 64)", name)
	}
	var exists int
	if err := d.sql.QueryRowContext(ctx, `SELECT COUNT(*) FROM namespaces WHERE name = ?`, name).Scan(&exists); err != nil {
		return nil, err
	}
	if exists > 0 {
		return nil, fmt.Errorf("namespace %q already exists", name)
	}

	emb, err := d.buildEmbedder(ctx, opts)
	if err != nil {
		return nil, err
	}
	chunkSize, chunkOverlap := opts.ChunkSize, opts.ChunkOverlap
	if chunkSize <= 0 {
		chunkSize = defaultChunkSize
	}
	if chunkOverlap < 0 || chunkOverlap >= chunkSize {
		chunkOverlap = defaultChunkOverlap
	}

	_, err = d.sql.ExecContext(ctx,
		`INSERT INTO namespaces (name, embedder_id, dims, chunk_size, chunk_over, created_at) VALUES (?,?,?,?,?,?)`,
		name, emb.ID(), emb.Dims(), chunkSize, chunkOverlap, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return &Namespace{db: d, name: name, emb: emb, dims: emb.Dims(), chunkSize: chunkSize, chunkOverlap: chunkOverlap}, nil
}

// Namespace opens an existing namespace and rebuilds its bound embedder.
//
// If the recorded embedder cannot be reconstructed — an OpenAI namespace opened
// without a key, say — this fails loudly instead of falling back to the
// built-in model. Falling back would produce a working-looking search over
// vectors that mean nothing to each other.
func (d *DB) Namespace(ctx context.Context, name string) (*Namespace, error) {
	var (
		embedderID                    string
		dims, chunkSize, chunkOverlap int
	)
	err := d.sql.QueryRowContext(ctx,
		`SELECT embedder_id, dims, chunk_size, chunk_over FROM namespaces WHERE name = ?`, name).
		Scan(&embedderID, &dims, &chunkSize, &chunkOverlap)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("namespace %q: %w", name, ErrNamespaceNotFound)
	}
	if err != nil {
		return nil, err
	}

	emb, err := d.embedderFromID(ctx, embedderID)
	if err != nil {
		return nil, fmt.Errorf("namespace %q is bound to embedder %q: %w", name, embedderID, err)
	}
	if emb.ID() != embedderID || emb.Dims() != dims {
		return nil, fmt.Errorf(
			"namespace %q was indexed with %s (%d dims) but the loaded embedder is %s (%d dims); "+
				"reindex the namespace or restore the original model",
			name, embedderID, dims, emb.ID(), emb.Dims())
	}
	return &Namespace{db: d, name: name, emb: emb, dims: dims, chunkSize: chunkSize, chunkOverlap: chunkOverlap}, nil
}

// ListNamespaces returns every namespace with its bound embedder and counts.
func (d *DB) ListNamespaces(ctx context.Context) ([]NamespaceInfo, error) {
	rows, err := d.sql.QueryContext(ctx, `
		SELECT n.name, n.embedder_id, n.dims, n.created_at,
		       (SELECT COUNT(*) FROM docs   d WHERE d.ns = n.name),
		       (SELECT COUNT(*) FROM chunks c WHERE c.ns = n.name)
		FROM namespaces n ORDER BY n.name`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []NamespaceInfo
	for rows.Next() {
		var i NamespaceInfo
		if err := rows.Scan(&i.Name, &i.EmbedderID, &i.Dims, &i.CreatedAt, &i.Documents, &i.Chunks); err != nil {
			return nil, err
		}
		out = append(out, i)
	}
	return out, rows.Err()
}

// NamespaceInfo is a namespace as reported by ListNamespaces.
type NamespaceInfo struct {
	Name       string `json:"name"`
	EmbedderID string `json:"embedder_id"`
	Dims       int    `json:"dims"`
	CreatedAt  string `json:"created_at"`
	Documents  int    `json:"documents"`
	Chunks     int    `json:"chunks"`
}

// DropNamespace deletes a namespace and everything in it.
func (d *DB) DropNamespace(ctx context.Context, name string) error {
	tx, err := d.sql.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Chunks go through DELETE rather than a bulk drop so the FTS triggers fire
	// and the index does not keep serving rows that no longer exist.
	for _, stmt := range []string{
		`DELETE FROM chunks WHERE ns = ?`,
		`DELETE FROM docs WHERE ns = ?`,
		`DELETE FROM namespaces WHERE name = ?`,
	} {
		if _, err := tx.ExecContext(ctx, stmt, name); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// buildEmbedder constructs the embedder described by opts.
func (d *DB) buildEmbedder(ctx context.Context, opts NamespaceOptions) (embed.Embedder, error) {
	if opts.OpenAIModel != "" {
		return embed.NewOpenAI(opts.OpenAIModel)
	}
	model := opts.Model
	if model == "" {
		model = embed.DefaultModel
	}
	return d.modelLoader(ctx, model, d.Progress)
}

// embedderFromID reverses ID() so a stored binding can be reconstructed.
func (d *DB) embedderFromID(ctx context.Context, id string) (embed.Embedder, error) {
	switch {
	case strings.HasPrefix(id, "builtin:"):
		return d.modelLoader(ctx, strings.TrimPrefix(id, "builtin:"), d.Progress)
	case strings.HasPrefix(id, "openai:"):
		return embed.NewOpenAI(strings.TrimPrefix(id, "openai:"))
	default:
		return nil, fmt.Errorf("unrecognized embedder id %q", id)
	}
}

func expandPath(p string) (string, error) {
	if !strings.HasPrefix(p, "~") {
		return p, nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, strings.TrimPrefix(p, "~")), nil
}
