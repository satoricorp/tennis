// Package embed turns text into vectors.
//
// Two implementations ship: a built-in static model that runs offline with no
// key and no inference engine, and an OpenAI client that you must switch on
// deliberately. There is no autodetection anywhere in this package. A namespace
// records the ID of the embedder that filled it, and tennis refuses to mix
// them, because vectors from different models occupy different spaces — a
// cosine between them is not "worse", it is meaningless, and the failure is
// silent unless something checks.
package embed

import (
	"context"
	"math"
)

// Embedder converts text into unit-length vectors.
//
// Implementations return normalized vectors so that a dot product is a cosine
// similarity, which lets the query layer skip a square root per comparison.
type Embedder interface {
	// ID is the stable identity recorded on a namespace at creation and
	// compared on every later write and query. Changing what a given ID means
	// silently invalidates every index that recorded it, so treat these as
	// permanent — introduce a new ID instead.
	ID() string

	// Dims is the vector width. Recorded alongside ID so a mismatch is caught
	// even if two models somehow share a name.
	Dims() int

	// Embed converts texts to vectors, one per input, in order. Text that
	// tokenizes to nothing yields a zero vector, which scores zero against
	// everything rather than erroring — an empty document is a legitimate
	// thing to store, it just should not match.
	Embed(ctx context.Context, texts []string) ([][]float32, error)
}

// normalize scales v to unit length in place and returns it. A zero vector is
// left alone: there is no meaningful direction to preserve, and dividing by
// zero would poison every later comparison with NaN.
func normalize(v []float32) []float32 {
	var sum float64
	for _, x := range v {
		sum += float64(x) * float64(x)
	}
	if sum == 0 {
		return v
	}
	inv := float32(1 / math.Sqrt(sum))
	for i := range v {
		v[i] *= inv
	}
	return v
}
