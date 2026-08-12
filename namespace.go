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
