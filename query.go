package tennis

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"
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

	// Collapse chunks to documents in SQL so that LIMIT counts DOCUMENTS.
	// Limiting raw chunk rows would let one chunky document eat the whole
	// candidate budget while the semantic side counts documents — a quiet
	// systematic bias in every hybrid ranking.
	//
	// rank is an FTS5 auxiliary column, only valid in a query whose FROM is
	// the FTS table itself. A plain subquery is NOT enough: the planner
	// flattens it into the outer join and the auxiliary context is gone
	// ("unable to use function bm25 in the requested context"). MATERIALIZED
	// forbids the flattening by contract rather than by planner mood.
	//
	// The outer bare c.text column rides SQLite's documented MIN() behaviour:
	// with exactly one aggregate MIN/MAX in the SELECT, bare columns come from
	// the row that produced the extreme — each document's best-scoring chunk.
	// rank is more negative for better matches, so MIN picks the best.
	rows, err := n.db.sql.QueryContext(ctx, `
		WITH f AS MATERIALIZED (
			SELECT rowid, rank AS score FROM chunks_fts WHERE chunks_fts MATCH ?
		)
		SELECT c.doc_id, c.text, MIN(f.score) AS best
		FROM f
		JOIN chunks c ON c.id = f.rowid
		JOIN docs   d ON d.ns = c.ns AND d.id = c.doc_id
		WHERE c.ns = ?`+filterSQL+`
		GROUP BY c.doc_id
		ORDER BY best
		LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []candidate
	for rows.Next() {
		var docID, text string
		var best float64
		if err := rows.Scan(&docID, &text, &best); err != nil {
			return nil, err
		}
		out = append(out, candidate{docID: docID, rank: len(out) + 1, text: text})
	}
	return out, rows.Err()
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

	// Pass 1 scans vectors only. Chunk TEXT stays on disk: the scan touches
	// every chunk in the namespace, but only the winners' text is ever shown,
	// and dragging the entire corpus's prose through RAM per query is the
	// difference between scaling to 500k chunks and not.
	args := append([]any{n.name}, filterArgs...)
	rows, err := n.db.sql.QueryContext(ctx, `
		SELECT c.id, c.doc_id, c.vec
		FROM chunks c
		JOIN docs  d ON d.ns = c.ns AND d.id = c.doc_id
		WHERE c.ns = ?`+filterSQL, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	type scored struct {
		chunkID int64
		docID   string
		score   float64
	}
	var all []scored
	for rows.Next() {
		var chunkID int64
		var docID string
		var blob []byte
		if err := rows.Scan(&chunkID, &docID, &blob); err != nil {
			return nil, err
		}
		v, err := decodeVector(blob, n.dims)
		if err != nil {
			return nil, err
		}
		all = append(all, scored{chunkID, docID, dot(qv, v)})
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	sort.Slice(all, func(i, j int) bool { return all[i].score > all[j].score })

	seen := make(map[string]bool)
	var winners []scored
	for _, s := range all {
		if seen[s.docID] {
			continue // keep only each document's best chunk
		}
		seen[s.docID] = true
		winners = append(winners, s)
		if len(winners) >= q.CandidateDepth {
			break
		}
	}
	if len(winners) == 0 {
		return nil, nil
	}

	// Pass 2 fetches text for just the winning chunks.
	ph := strings.TrimSuffix(strings.Repeat("?,", len(winners)), ",")
	textArgs := make([]any, len(winners))
	for i, w := range winners {
		textArgs[i] = w.chunkID
	}
	textRows, err := n.db.sql.QueryContext(ctx, `SELECT id, text FROM chunks WHERE id IN (`+ph+`)`, textArgs...)
	if err != nil {
		return nil, err
	}
	defer textRows.Close()
	texts := make(map[int64]string, len(winners))
	for textRows.Next() {
		var id int64
		var text string
		if err := textRows.Scan(&id, &text); err != nil {
			return nil, err
		}
		texts[id] = text
	}
	if err := textRows.Err(); err != nil {
		return nil, err
	}

	out := make([]candidate, len(winners))
	for i, w := range winners {
		out[i] = candidate{docID: w.docID, rank: i + 1, text: texts[w.chunkID]}
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
//
// Term characters are any Unicode letter or digit, matching the unicode61
// tokenizer that built the index. An ASCII-only rule here would shred "café"
// into garbage and reduce non-Latin queries to nothing — silently disabling
// the keyword half of the hybrid for every language but English.
func buildFTSQuery(text string) string {
	var terms []string
	for _, w := range strings.FieldsFunc(strings.ToLower(text), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r) && r != '_'
	}) {
		if utf8.RuneCountInString(w) < 2 || stopWords[w] {
			continue
		}
		terms = append(terms, `"`+w+`"`)
	}
	return strings.Join(terms, " OR ")
}
