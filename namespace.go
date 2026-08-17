package tennis

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"

	"github.com/satoricorp/tennis/embed"
)

// Namespace is an isolated collection of documents with one bound embedder.
type Namespace struct {
	db           *DB
	name         string
	emb          embed.Embedder
	dims         int
	chunkSize    int
	chunkOverlap int
}

// Name is the namespace's name.
func (n *Namespace) Name() string { return n.name }

// EmbedderID is the embedder permanently bound to this namespace.
func (n *Namespace) EmbedderID() string { return n.emb.ID() }

// Document is a unit of storage. Text is what gets chunked, embedded, and
// searched; Attributes are arbitrary JSON values you can filter on.
type Document struct {
	ID         string         `json:"id"`
	Text       string         `json:"text"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// WriteResult reports what a Write actually did.
type WriteResult struct {
	Written int `json:"written"` // documents embedded and stored
	Skipped int `json:"skipped"` // unchanged since last write, left alone
	Chunks  int `json:"chunks"`  // chunks produced for the written documents
}

// Write upserts documents.
//
// Documents whose text and attributes are byte-identical to what is already
// stored are skipped entirely — not re-embedded, not re-indexed. Embedding is
// the slowest part of ingest by a wide margin, so re-seeding a corpus where
// little changed costs almost nothing. This is why the content hash is a column
// rather than something computed on demand.
func (n *Namespace) Write(ctx context.Context, docs []Document) (*WriteResult, error) {
	res := &WriteResult{}
	if len(docs) == 0 {
		return res, nil
	}

	type pending struct {
		doc    Document
		attrs  string
		hash   string
		chunks []string
	}
	var (
		todo      []pending
		allChunks []string
	)

	// One query for every stored hash in the namespace, instead of one query
	// per incoming document. A namespace's (id, hash) pairs are tiny — even a
	// hundred thousand documents fit in a few MB — and this is the difference
	// between a 10k-file re-seed doing 1 round trip or 10,000.
	existing := make(map[string]string)
	hashRows, err := n.db.sql.QueryContext(ctx, `SELECT id, hash FROM docs WHERE ns = ?`, n.name)
	if err != nil {
		return nil, err
	}
	for hashRows.Next() {
		var id, h string
		if err := hashRows.Scan(&id, &h); err != nil {
			hashRows.Close()
			return nil, err
		}
		existing[id] = h
	}
	if err := hashRows.Close(); err != nil {
		return nil, err
	}

	for _, d := range docs {
		if d.ID == "" {
			return nil, fmt.Errorf("document has an empty ID")
		}
		attrs := "{}"
		if len(d.Attributes) > 0 {
			b, err := json.Marshal(d.Attributes)
			if err != nil {
				return nil, fmt.Errorf("document %q: encoding attributes: %w", d.ID, err)
			}
			attrs = string(b)
		}
		sum := sha256.Sum256([]byte(d.Text + "\x00" + attrs))
		hash := hex.EncodeToString(sum[:])

		if existing[d.ID] == hash {
			res.Skipped++
			continue
		}

		chunks := chunkText(d.Text, n.chunkSize, n.chunkOverlap)
		todo = append(todo, pending{doc: d, attrs: attrs, hash: hash, chunks: chunks})
		allChunks = append(allChunks, chunks...)
	}

	if len(todo) == 0 {
		return res, nil
	}

	// One embed call for every chunk in the batch. Static embedding is cheap
	// per item but the call still has fixed overhead, and the OpenAI path bills
	// per request, so batching matters more there.
	vectors, err := n.emb.Embed(ctx, allChunks)
	if err != nil {
		return nil, fmt.Errorf("embedding: %w", err)
	}
	if len(vectors) != len(allChunks) {
		return nil, fmt.Errorf("embedder returned %d vectors for %d chunks", len(vectors), len(allChunks))
	}

	tx, err := n.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	cursor := 0
	for _, p := range todo {
		// Replacing a document means deleting its old chunks first; the FTS
		// triggers turn that into a proper index delete.
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE ns = ? AND doc_id = ?`, n.name, p.doc.ID); err != nil {
			return nil, err
		}
		if _, err := tx.ExecContext(ctx,
			`INSERT INTO docs (ns, id, text, attrs, hash) VALUES (?,?,?,?,?)
			 ON CONFLICT(ns, id) DO UPDATE SET text=excluded.text, attrs=excluded.attrs, hash=excluded.hash`,
			n.name, p.doc.ID, p.doc.Text, p.attrs, p.hash); err != nil {
			return nil, err
		}
		for i, chunkStr := range p.chunks {
			if _, err := tx.ExecContext(ctx,
				`INSERT INTO chunks (ns, doc_id, ord, text, vec) VALUES (?,?,?,?,?)`,
				n.name, p.doc.ID, i, chunkStr, encodeVector(vectors[cursor+i])); err != nil {
				return nil, err
			}
		}
		cursor += len(p.chunks)
		res.Written++
		res.Chunks += len(p.chunks)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

// Delete removes documents by ID and reports how many existed.
func (n *Namespace) Delete(ctx context.Context, ids []string) (int, error) {
	tx, err := n.db.sql.BeginTx(ctx, nil)
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	deleted := 0
	for _, id := range ids {
		if _, err := tx.ExecContext(ctx, `DELETE FROM chunks WHERE ns = ? AND doc_id = ?`, n.name, id); err != nil {
			return 0, err
		}
		r, err := tx.ExecContext(ctx, `DELETE FROM docs WHERE ns = ? AND id = ?`, n.name, id)
		if err != nil {
			return 0, err
		}
		k, err := r.RowsAffected()
		if err != nil {
			return 0, err
		}
		if k > 0 {
			deleted++
		}
	}
	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return deleted, nil
}

// DocumentInfo is a document's metadata without its text — what you need to
// see what a namespace holds, without dragging every transcript through memory
// to find out.
type DocumentInfo struct {
	ID         string         `json:"id"`
	Attributes map[string]any `json:"attributes,omitempty"`
	Chars      int            `json:"chars"`
	Chunks     int            `json:"chunks"`
}

// GroupInfo is one value of a grouping attribute and what sits under it.
// Attributes carries the fields that are constant within the group — a title
// and a source do not vary between the turns of one conversation.
type GroupInfo struct {
	Key        string         `json:"key"`
	Documents  int            `json:"documents"`
	Chunks     int            `json:"chunks"`
	Attributes map[string]any `json:"attributes,omitempty"`
}

// ListOptions narrows and orders a listing.
type ListOptions struct {
	Filter Filter // optional attribute filter, same as Query
	Limit  int    // default 50; a negative value means no limit
	Offset int

	// SortBy names an attribute to order by. Values are compared as SQLite
	// compares them, so an attribute holding RFC3339 timestamps orders
	// chronologically and one holding free text orders lexically.
	SortBy string
	Asc    bool
}

// List returns document metadata, newest first.
//
// Text is deliberately not returned. A large namespace is tens of megabytes of
// transcript, and none of it is needed to answer "what is in here" — the point
// of this call is that it stays cheap as the corpus grows, which the scan
// behind Query does not.
func (n *Namespace) List(ctx context.Context, opts ListOptions) ([]DocumentInfo, error) {
	sortPath, limit, err := listPlan(&opts, "created")
	if err != nil {
		return nil, err
	}
	where, args, err := listFilter(n.name, opts.Filter)
	if err != nil {
		return nil, err
	}
	args = append(args, limit, opts.Offset)

	rows, err := n.db.sql.QueryContext(ctx, `
		SELECT d.id, d.attrs, LENGTH(d.text),
		       (SELECT COUNT(*) FROM chunks c WHERE c.ns = d.ns AND c.doc_id = d.id)
		FROM docs d
		WHERE d.ns = ?`+where+`
		ORDER BY (`+sortPath+` IS NULL), `+sortPath+` `+listDirection(opts)+`, d.id
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []DocumentInfo
	for rows.Next() {
		var info DocumentInfo
		var attrs string
		if err := rows.Scan(&info.ID, &attrs, &info.Chars, &info.Chunks); err != nil {
			return nil, err
		}
		if err := decodeAttrs(attrs, &info.Attributes); err != nil {
			return nil, err
		}
		out = append(out, info)
	}
	return out, rows.Err()
}

// Groups collapses documents by an attribute — most usefully the one that ties
// a conversation's turns together.
//
// A namespace imported at turn granularity holds one document per message, and
// listing those answers a question nobody asked: thirty thousand rows where the
// user wanted to know which conversations they have. Grouping is what makes a
// listing readable at that granularity.
//
// The extra fields are aggregated with MIN, which is exact rather than
// arbitrary for values that do not vary within a group — which is the case for
// every attribute carried here.
func (n *Namespace) Groups(ctx context.Context, attr string, opts ListOptions, extra ...string) ([]GroupInfo, error) {
	groupPath, err := attrPath(attr)
	if err != nil {
		return nil, err
	}
	sortPath, limit, err := listPlan(&opts, "created")
	if err != nil {
		return nil, err
	}
	where, args, err := listFilter(n.name, opts.Filter)
	if err != nil {
		return nil, err
	}
	args = append(args, limit, opts.Offset)

	selects := ""
	for _, key := range extra {
		path, err := attrPath(key)
		if err != nil {
			return nil, err
		}
		selects += ", MIN(" + path + ")"
	}

	rows, err := n.db.sql.QueryContext(ctx, `
		SELECT `+groupPath+` AS grp, COUNT(*),
		       SUM((SELECT COUNT(*) FROM chunks c WHERE c.ns = d.ns AND c.doc_id = d.id)),
		       MAX(`+sortPath+`)`+selects+`
		FROM docs d
		WHERE d.ns = ? AND `+groupPath+` IS NOT NULL`+where+`
		GROUP BY grp
		ORDER BY MAX(`+sortPath+`) `+listDirection(opts)+`, grp
		LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []GroupInfo
	for rows.Next() {
		var (
			g      GroupInfo
			sortAt any
			vals   = make([]any, len(extra))
		)
		dest := []any{&g.Key, &g.Documents, &g.Chunks, &sortAt}
		for i := range vals {
			dest = append(dest, &vals[i])
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		g.Attributes = map[string]any{}
		if sortAt != nil {
			g.Attributes[opts.SortBy] = sortAt
		}
		for i, key := range extra {
			if vals[i] != nil {
				g.Attributes[key] = vals[i]
			}
		}
		out = append(out, g)
	}
	return out, rows.Err()
}

// Count reports how many documents match a filter, without reading any of them.
func (n *Namespace) Count(ctx context.Context, filter Filter) (int, error) {
	where, args, err := listFilter(n.name, filter)
	if err != nil {
		return 0, err
	}
	var count int
	err = n.db.sql.QueryRowContext(ctx,
		`SELECT COUNT(*) FROM docs d WHERE d.ns = ?`+where, args...).Scan(&count)
	return count, err
}

// CountGroups reports how many distinct values of an attribute are present.
func (n *Namespace) CountGroups(ctx context.Context, attr string, filter Filter) (int, error) {
	path, err := attrPath(attr)
	if err != nil {
		return 0, err
	}
	where, args, err := listFilter(n.name, filter)
	if err != nil {
		return 0, err
	}
	var count int
	err = n.db.sql.QueryRowContext(ctx,
		`SELECT COUNT(DISTINCT `+path+`) FROM docs d WHERE d.ns = ? AND `+path+` IS NOT NULL`+where,
		args...).Scan(&count)
	return count, err
}

func listPlan(opts *ListOptions, defaultSort string) (sortPath string, limit int, err error) {
	if opts.SortBy == "" {
		opts.SortBy = defaultSort
	}
	limit = opts.Limit
	switch {
	case limit == 0:
		limit = 50
	case limit < 0:
		limit = -1 // SQLite reads a negative LIMIT as unbounded
	}
	sortPath, err = attrPath(opts.SortBy)
	return sortPath, limit, err
}

func listFilter(ns string, f Filter) (string, []any, error) {
	args := []any{ns}
	if f == nil {
		return "", args, nil
	}
	clause, filterArgs, err := f.clause()
	if err != nil {
		return "", nil, err
	}
	return " AND " + clause, append(args, filterArgs...), nil
}

// listDirection puts documents missing the sort attribute last in either
// direction rather than clustering them at the top: a plain file has no
// created date, and a listing that opened with every file would bury what the
// user was looking for.
func listDirection(opts ListOptions) string {
	if opts.Asc {
		return "ASC"
	}
	return "DESC"
}

func decodeAttrs(raw string, into *map[string]any) error {
	if raw == "" || raw == "{}" {
		return nil
	}
	return json.Unmarshal([]byte(raw), into)
}

// Get returns a single document.
func (n *Namespace) Get(ctx context.Context, id string) (*Document, error) {
	var text, attrs string
	err := n.db.sql.QueryRowContext(ctx, `SELECT text, attrs FROM docs WHERE ns = ? AND id = ?`, n.name, id).Scan(&text, &attrs)
	if err == sql.ErrNoRows {
		return nil, fmt.Errorf("document %q not found in namespace %q", id, n.name)
	}
	if err != nil {
		return nil, err
	}
	d := &Document{ID: id, Text: text}
	if attrs != "" && attrs != "{}" {
		if err := json.Unmarshal([]byte(attrs), &d.Attributes); err != nil {
			return nil, err
		}
	}
	return d, nil
}

// encodeVector packs a float32 slice into a little-endian BLOB.
//
// Storing raw floats rather than JSON keeps a 512-dim vector at 2KB instead of
// roughly 6KB of text, and decoding is a cast rather than a parse — which
// matters because every query decodes every vector in the namespace.
func encodeVector(v []float32) []byte {
	b := make([]byte, len(v)*4)
	for i, f := range v {
		bits := math.Float32bits(f)
		b[i*4+0] = byte(bits)
		b[i*4+1] = byte(bits >> 8)
		b[i*4+2] = byte(bits >> 16)
		b[i*4+3] = byte(bits >> 24)
	}
	return b
}

// decodeVector reverses encodeVector.
func decodeVector(b []byte, dims int) ([]float32, error) {
	if len(b) != dims*4 {
		return nil, fmt.Errorf("vector blob is %d bytes, expected %d for %d dims", len(b), dims*4, dims)
	}
	v := make([]float32, dims)
	for i := range v {
		v[i] = math.Float32frombits(
			uint32(b[i*4+0]) | uint32(b[i*4+1])<<8 | uint32(b[i*4+2])<<16 | uint32(b[i*4+3])<<24)
	}
	return v, nil
}
