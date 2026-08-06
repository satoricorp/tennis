package tennis

import (
	"strings"
	"unicode/utf8"
)

const (
	defaultChunkSize    = 1000
	defaultChunkOverlap = 100
)

// chunkText splits text into overlapping windows of roughly size characters.
//
// Why chunk at all, when a static embedder has no context window to overflow:
// a single vector is the mean of every token in the text, so the longer the
// input the more it converges on the average of the language rather than the
// meaning of the passage. A ten-page document embeds to something close to
// nothing in particular. Chunking keeps each vector about one idea.
//
// Splitting prefers paragraph breaks, then sentence ends, then whitespace, so
// chunks land on boundaries a human would pick. overlap carries the tail of
// each chunk into the next, so a passage straddling a boundary is still found
// intact by at least one of them.
func chunkText(text string, size, overlap int) []string {
	text = strings.TrimSpace(text)
	if text == "" {
		return nil
	}
	if size <= 0 {
		size = defaultChunkSize
	}
	if overlap < 0 || overlap >= size {
		overlap = defaultChunkOverlap
	}
	if len(text) <= size {
		return []string{text}
	}

	var out []string
	for start := 0; start < len(text); {
		end := start + size
		if end >= len(text) {
			if s := strings.TrimSpace(text[start:]); s != "" {
				out = append(out, s)
			}
			break
		}
		end = breakPoint(text, start, end)
		if s := strings.TrimSpace(text[start:end]); s != "" {
			out = append(out, s)
		}
		// The overlap step is byte arithmetic, so it can land inside a
		// multi-byte rune; align it back to a rune start before slicing there.
		next := alignRune(text, end-overlap)
		if next <= start {
			// Pathological input (no break points, tiny window): step forward
			// rather than loop forever producing the same chunk.
			next = end
		}
		start = next
	}
	return out
}

// breakPoint finds a natural split at or before end, searching back through the
// last quarter of the window. Beyond that the chunks get too ragged, so it
// gives up and cuts mid-word rather than emitting a stub.
//
// The separators are all ASCII, so a match is always a rune boundary; only the
// give-up path needs explicit alignment. Without it, size is byte arithmetic
// and the cut can land inside a multi-byte rune, storing invalid UTF-8 —
// invisible on English corpora, near-universal on anything else.
func breakPoint(text string, start, end int) int {
	limit := end - (end-start)/4
	for _, sep := range []string{"\n\n", ". ", "\n", " "} {
		if i := strings.LastIndex(text[start:end], sep); i >= 0 && start+i > limit {
			return start + i + len(sep)
		}
	}
	if aligned := alignRune(text, end); aligned > start {
		return aligned
	}
	// The window is narrower than one rune (size < 4 with a 4-byte rune at
	// start): emit that whole rune rather than looping or splitting it.
	_, w := utf8.DecodeRuneInString(text[start:])
	return start + w
}

// alignRune moves i back to the start of the rune it points into. UTF-8
// continuation bytes are self-identifying, so this is at most a 3-byte walk.
func alignRune(s string, i int) int {
	for i > 0 && i < len(s) && !utf8.RuneStart(s[i]) {
		i--
	}
	return i
}
