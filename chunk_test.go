package tennis

import (
	"strings"
	"testing"
	"unicode/utf8"
)

// Regression: chunk boundaries are byte arithmetic, and a byte offset can land
// inside a multi-byte rune. The original implementation produced invalid UTF-8
// in 43 of 45 chunks on this corpus — invisible on English text, near-certain
// on anything else.
func TestChunkingPreservesUTF8(t *testing.T) {
	text := strings.Repeat("héllo wörld émoji 🎾 日本語テキスト ", 400)
	chunks := chunkText(text, 500, 50)
	if len(chunks) < 10 {
		t.Fatalf("expected many chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if !utf8.ValidString(c) {
			t.Fatalf("chunk %d is invalid UTF-8 (len %d)", i, len(c))
		}
	}
}

// FuzzChunkText holds the two invariants that matter for arbitrary input:
// valid chunks in, valid chunks out, and termination with every chunk
// non-empty. The UTF-8 bug this guards against was found by review, not by the
// unit tests — every hand-written corpus was clean ASCII, which is exactly the
// blind spot fuzzing exists to remove.
func FuzzChunkText(f *testing.F) {
	f.Add("plain ascii text with several words", 50, 10)
	f.Add(strings.Repeat("héllo wörld 🎾 日本語 ", 40), 64, 16)
	f.Add("no-spaces-"+strings.Repeat("🎾", 100), 16, 4)
	f.Add("", 100, 10)
	f.Add("x", 1, 0)
	f.Fuzz(func(t *testing.T, text string, size, overlap int) {
		if !utf8.ValidString(text) {
			t.Skip() // garbage in, no promise out
		}
		// Bound the parameters the way the API does not: chunkText itself
		// resets nonsense values to defaults, so huge sizes are legal.
		chunks := chunkText(text, size%5000, overlap%5000)
		for i, c := range chunks {
			if !utf8.ValidString(c) {
				t.Fatalf("chunk %d invalid UTF-8 for size=%d overlap=%d", i, size, overlap)
			}
			if strings.TrimSpace(c) == "" {
				t.Fatalf("chunk %d is blank", i)
			}
		}
	})
}
