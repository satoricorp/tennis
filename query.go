package tennis

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
)

// Mode selects which rankers run.
type Mode string

const (
	// Hybrid runs both rankers and fuses them. This is the default and the
	// reason tennis exists: keyword search finds exact terms, identifiers and
	// rare words; semantic search finds paraphrases. Each fails where the other
	// is strong.
	Hybrid Mode = "hybrid"

	// Keyword is BM25 only. Exact, explainable, and free of any model.
	Keyword Mode = "keyword"

	// Semantic is vector similarity only. Useful for diagnosing ranking, rarely
	// the right choice on its own.
	Semantic Mode = "semantic"
)

// defaultRRFK is the standard reciprocal-rank-fusion constant. It damps the
// difference between adjacent top ranks so that one ranker cannot dominate on
// the strength of a single confident hit; 60 is the value from the original
// paper and behaves well without tuning.
const defaultRRFK = 60

// Query describes a search.
type Query struct {
	Text   string // what to search for
	TopK   int    // how many documents to return (default 10)
	Mode   Mode   // default Hybrid
	Filter Filter // optional attribute filter

	// RRFK overrides the fusion constant. Lower values weight the very top of
	// each ranker more heavily. Leave at zero unless you are tuning.
	RRFK int

	// CandidateDepth is how many chunks each ranker contributes before fusion
	// (default 100). Raising it improves recall on large namespaces at the cost
	// of a longer merge.
	CandidateDepth int
}

// Result is one matched document.
type Result struct {
	ID         string         `json:"id"`
	Score      float64        `json:"score"`
	Text       string         `json:"text"` // the best-matching chunk, not the whole document
	Attributes map[string]any `json:"attributes,omitempty"`

	// KeywordRank and SemanticRank are 1-based positions in each ranker, or 0
	// if that ranker did not surface this document. They make a result
	// explainable: a document that only one ranker found is worth a second look.
	KeywordRank  int `json:"keyword_rank"`
	SemanticRank int `json:"semantic_rank"`
}

// candidate is one ranker's view of a document.
type candidate struct {
	docID string
	rank  int
	text  string
}

// Query searches the namespace.
func (n *Namespace) Query(ctx context.Context, q Query) ([]Result, error) {
	if strings.TrimSpace(q.Text) == "" {
		return nil, fmt.Errorf("query text is empty")
	}
	if q.TopK <= 0 {
		q.TopK = 10
	}
	if q.Mode == "" {
		q.Mode = Hybrid
	}
	if q.RRFK <= 0 {
		q.RRFK = defaultRRFK
	}
	if q.CandidateDepth <= 0 {
		q.CandidateDepth = 100
	}

	filterSQL, filterArgs := "", []any(nil)
	if q.Filter != nil {
		c, a, err := q.Filter.clause()
		if err != nil {
			return nil, err
		}
		filterSQL, filterArgs = " AND "+c, a
	}

	var keyword, semantic []candidate
	var err error

	if q.Mode == Hybrid || q.Mode == Keyword {
		keyword, err = n.keywordSearch(ctx, q, filterSQL, filterArgs)
		if err != nil {
			return nil, fmt.Errorf("keyword search: %w", err)
		}
	}
	if q.Mode == Hybrid || q.Mode == Semantic {
		semantic, err = n.semanticSearch(ctx, q, filterSQL, filterArgs)
		if err != nil {
			return nil, fmt.Errorf("semantic search: %w", err)
		}
	}
	return n.fuse(ctx, q, keyword, semantic)
}

// keywordSearch ranks chunks with FTS5's BM25 and rolls them up per document.
func (n *Namespace) keywordSearch(ctx context.Context, q Query, filterSQL string, filterArgs []any) ([]candidate, error) {
	match := buildFTSQuery(q.Text)
	if match == "" {
		// Every term was a stop word. No keyword signal, but the semantic side
		// can still answer, so this is empty rather than an error.
		return nil, nil
	}
	args := append([]any{match, n.name}, filterArgs...)
	args = append(args, q.CandidateDepth)

	// The FTS table is not aliased: an alias is not a valid MATCH target, and
	// "rank" only resolves against the real table name.
	rows, err := n.db.sql.QueryContext(ctx, `
		SELECT c.doc_id, c.text
		FROM chunks_fts
		JOIN chunks c ON c.id = chunks_fts.rowid
		JOIN docs   d ON d.ns = c.ns AND d.id = c.doc_id
		WHERE chunks_fts MATCH ? AND c.ns = ?`+filterSQL+`
		ORDER BY chunks_fts.rank
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	return collapseToDocuments(rows)
}

// semanticSearch scores every chunk in the namespace by cosine similarity.
//
// This is an exact brute-force scan, not an approximate index. At 512 dims a
// modern core compares a few million vectors a second, so the honest crossover
// where ANN starts to pay is well past the point where you would move to a
// server anyway. Exactness means no recall cliff and no index to tune.
func (n *Namespace) semanticSearch(ctx context.Context, q Query, filterSQL string, filterArgs []any) ([]candidate, error) {
	vecs, err := n.emb.Embed(ctx, []string{q.Text})
	if err != nil {
		return nil, fmt.Errorf("embedding query: %w", err)
	}
	qv := vecs[0]

	args := append([]any{n.name}, filterArgs...)
	rows, err := n.db.sql.QueryContext(ctx, `
		SELECT c.doc_id, c.text, c.vec
		FROM chunks c
		JOIN docs  d ON d.ns = c.ns AND d.id = c.doc_id
		WHERE c.ns = ?`+filterSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		docID string
		text  string
		score float64
	}
	var all []scored
	for rows.Next() {
		var docID, text string
		var blob []byte
		if err := rows.Scan(&docID, &text, &blob); err != nil {
			return nil, err
		}
		v, err := decodeVector(blob, n.dims)
		if err != nil {
			return nil, err
		}
		all = append(all, scored{docID, text, dot(qv, v)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })

	seen := make(map[string]bool)
	var out []candidate
	for _, s := range all {
		if seen[s.docID] {
			continue // keep only each document's best chunk
		}
		seen[s.docID] = true
		out = append(out, candidate{docID: s.docID, rank: len(out) + 1, text: s.text})
		if len(out) >= q.CandidateDepth {
			break
		}
	}
	return out, nil
}

// fuse merges the two rankings with reciprocal rank fusion.
//
// RRF combines by position rather than by score, which is what makes it work
// here: BM25 scores and cosine similarities are on incomparable scales, and any
// attempt to normalize them into a weighted sum ends up encoding an arbitrary
// opinion about how a 0.7 cosine compares to a BM25 of -1.3. Positions are
// directly comparable and need no calibration.
func (n *Namespace) fuse(ctx context.Context, q Query, keyword, semantic []candidate) ([]Result, error) {
	type acc struct {
		score                     float64
		text                      string
		keywordRank, semanticRank int
	}
	merged := map[string]*acc{}

	add := func(list []candidate, isKeyword bool) {
		for _, c := range list {
			a := merged[c.docID]
			if a == nil {
				a = &acc{text: c.text}
				merged[c.docID] = a
			}
			a.score += 1 / float64(q.RRFK+c.rank)
			if isKeyword {
				a.keywordRank = c.rank
			} else {
				a.semanticRank = c.rank
				// Prefer the semantically best chunk as the shown snippet when
				// both rankers matched: it is more often the passage a reader
				// would consider the answer.
				a.text = c.text
			}
		}
	}
	add(keyword, true)
	add(semantic, false)

	results := make([]Result, 0, len(merged))
	for id, a := range merged {
		results = append(results, Result{
			ID: id, Score: a.score, Text: a.text,
			KeywordRank: a.keywordRank, SemanticRank: a.semanticRank,
		})
	}
	sort.Slice(results, func(i, j int) bool {
		if results[i].Score != results[j].Score {
			return results[i].Score > results[j].Score
		}
		return results[i].ID < results[j].ID // stable output for equal scores
	})
	if len(results) > q.TopK {
		results = results[:q.TopK]
	}

	// Attach attributes only for the page actually returned.
	for i := range results {
		var attrs string
		if err := n.db.sql.QueryRowContext(ctx,
			`SELECT attrs FROM docs WHERE ns = ? AND id = ?`, n.name, results[i].ID).Scan(&attrs); err != nil {
			return nil, err
		}
		if attrs != "" && attrs != "{}" {
			if err := json.Unmarshal([]byte(attrs), &results[i].Attributes); err != nil {
				return nil, err
			}
		}
	}
	return results, nil
}

// collapseToDocuments keeps the first (best-ranked) chunk per document.
func collapseToDocuments(rows interface {
	Next() bool
	Scan(...any) error
	Err() error
}) ([]candidate, error) {
	seen := make(map[string]bool)
	var out []candidate
	for rows.Next() {
		var docID, text string
		if err := rows.Scan(&docID, &text); err != nil {
			return nil, err
		}
		if seen[docID] {
			continue
		}
		seen[docID] = true
		out = append(out, candidate{docID: docID, rank: len(out) + 1, text: text})
	}
	return out, rows.Err()
}

func dot(a, b []float32) float64 {
	var s float64
	for i := range a {
		s += float64(a[i]) * float64(b[i])
	}
	return s
}

// stopWords are closed-class words that carry no discriminating signal. Left in
// an OR query they match nearly every document, so BM25 spends its ranking
// budget on "the" instead of on the word you cared about.
var stopWords = map[string]bool{
	"a": true, "an": true, "and": true, "are": true, "as": true, "at": true,
	"be": true, "but": true, "by": true, "for": true, "from": true, "has": true,
	"have": true, "he": true, "her": true, "his": true, "i": true, "in": true,
	"is": true, "it": true, "its": true, "me": true, "my": true, "not": true,
	"of": true, "on": true, "or": true, "she": true, "that": true, "the": true,
	"they": true, "this": true, "to": true, "was": true, "we": true, "were": true,
	"with": true, "you": true, "your": true,
}

// buildFTSQuery turns human input into an FTS5 expression.
//
// Terms are OR'd rather than AND'd because BM25 is one half of a hybrid: its
// job is to nominate candidates broadly and let IDF sort out which matches
// mattered, while requiring every term would drop documents the semantic side
// could have rescued. Every term is quoted so that FTS5 operators typed by a
// user ("NEAR", "*", "-") are treated as text rather than syntax.
func buildFTSQuery(text string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !('a' <= r && r <= 'z') && !('0' <= r && r <= '9') && r != '_'
	}) {
		if len(w) < 2 || stopWords[w] {
			continue
		}
		terms = append(terms, `"`+w+`"`)
	}
	return strings.Join(terms, " OR ")
}
