package embed

import (
	"context"
	"encoding/json"
	"os"
	"testing"
)

// TestAgainstReference dumps vectors for a fixed set of inputs so they can be
// diffed against the Python model2vec implementation. Correctness here is not
// eyeballable: a wrong pooling or an off-by-one in the vocab produces vectors
// that are merely mediocre, never obviously broken.
func TestAgainstReference(t *testing.T) {
	spec := Builtins["potion-retrieval-32M"]
	s, err := NewStatic(spec, "../testdata/model.safetensors", "../testdata/tokenizer.json")
	if err != nil {
		t.Fatal(err)
	}
	inputs := []string{
		"keep me signed in",
		"make the login flow remember the user between sessions",
		"fix the flaky test in the auth module",
		"",
	}
	vecs, err := s.Embed(context.Background(), inputs)
	if err != nil {
		t.Fatal(err)
	}
	out := map[string]any{"id": s.ID(), "dims": s.Dims(), "inputs": inputs, "vectors": vecs}
	f, _ := os.Create("../testdata/go_vectors.json")
	defer f.Close()
	json.NewEncoder(f).Encode(out)
	t.Logf("id=%s dims=%d first8=%v", s.ID(), s.Dims(), vecs[0][:8])
}
