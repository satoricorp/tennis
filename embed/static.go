package embed

import (
	"context"
	"fmt"
	"sync"

	"github.com/sugarme/tokenizer"
	"github.com/sugarme/tokenizer/pretrained"
)

// Static is a model2vec/potion embedder: a vocabulary-sized matrix of vectors
// and nothing else. "Inference" is a tokenize, a gather, and a mean — there is
// no network to run, so there is no ONNX runtime, no cgo, and no shared library
// to find at startup. That is the whole reason tennis is one static binary.
//
// The tradeoff is quality. potion-retrieval-32M scores 35.06 on MTEB retrieval
// against all-MiniLM-L6-v2's 42.92, so roughly 82% of a model that is itself
// well behind text-embedding-3-small. In a hybrid ranking that gap narrows a
// lot, because the queries a static model handles worst — exact identifiers,
// rare tokens — are precisely the ones BM25 handles best. If you need the
// quality back, turn on OpenAI explicitly.
//
// A real upside beyond size: with no transformer there is no context window,
// so unlike a BERT-based embedder this will not silently ignore everything past
// token 512. Long text still wants chunking for ranking reasons, but nothing
// is discarded without telling you.
type Static struct {
	model ModelSpec
	tk    *tokenizer.Tokenizer
	mat   *Matrix

	// The tokenizer is not documented as goroutine-safe, and Embed is a
	// natural thing to call concurrently, so serialize it. The matrix is
	// read-only after load and needs no guard.
	mu sync.Mutex
}

// NewStatic loads a static model from an already-materialized cache directory.
// Use Load if you want the download handled for you.
func NewStatic(spec ModelSpec, weightsPath, tokenizerPath string) (*Static, error) {
	mat, err := readSafetensors(weightsPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s weights: %w", spec.Name, err)
	}
	tk, err := pretrained.FromFile(tokenizerPath)
	if err != nil {
		return nil, fmt.Errorf("loading %s tokenizer: %w", spec.Name, err)
	}
	// Padding would pad every input out to a fixed width with [PAD], which for
	// mean pooling means dragging every vector toward the pad token's. These
	// models ship with padding off, but a future one might not.
	tk.WithPadding(nil)
	tk.WithTruncation(nil)

	if mat.Cols != spec.Dims {
		return nil, fmt.Errorf("%s: weights are %d-dimensional but the model spec says %d", spec.Name, mat.Cols, spec.Dims)
	}
	return &Static{model: spec, tk: tk, mat: mat}, nil
}

func (s *Static) ID() string { return "builtin:" + s.model.Name }
func (s *Static) Dims() int  { return s.mat.Cols }

// Embed mean-pools the vectors of a text's tokens.
//
// Special tokens are excluded: model2vec distills a matrix over ordinary
// vocabulary, and folding [CLS]/[SEP] into the mean would pull every vector
// toward a common point and flatten the differences we are trying to measure.
func (s *Static) Embed(_ context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))

	s.mu.Lock()
	defer s.mu.Unlock()

	for i, text := range texts {
		enc, err := s.tk.EncodeSingle(text, false) // false: no [CLS]/[SEP]
		if err != nil {
			return nil, fmt.Errorf("tokenizing input %d: %w", i, err)
		}

		acc := make([]float32, s.mat.Cols)
		n := 0
		for j, id := range enc.Ids {
			// Guard the row index rather than trusting the tokenizer to agree
			// with the matrix: a vocab/weights mismatch would otherwise be an
			// out-of-range panic deep in a library.
			if id < 0 || id >= s.mat.Rows {
				continue
			}
			if j < len(enc.SpecialTokenMask) && enc.SpecialTokenMask[j] == 1 {
				continue
			}
			row := s.mat.Row(id)
			for k, v := range row {
				acc[k] += v
			}
			n++
		}
		if n > 0 {
			inv := 1 / float32(n)
			for k := range acc {
				acc[k] *= inv
			}
		}
		out[i] = normalize(acc)
	}
	return out, nil
}
