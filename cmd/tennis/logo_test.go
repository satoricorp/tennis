package main

import (
	"os"
	"strings"
	"testing"
)

// The art is generated, so what is worth testing is that it is still something
// a terminal can print: the three characters it is drawn with and spaces, no
// wider than the width a terminal is always at least, and nothing trailing.
func TestLogoArtIsPrintable(t *testing.T) {
	lines := logoLines()
	if len(lines) == 0 {
		t.Fatal("no art")
	}

	for i, line := range lines {
		for _, c := range line {
			switch c {
			case ' ', '#', '"', '_':
			default:
				t.Fatalf("line %d: unexpected %q in the art", i, c)
			}
		}
		if strings.HasSuffix(line, " ") {
			t.Errorf("line %d has trailing space: %q", i, line)
		}
		if strings.Contains(line, "\x1b") {
			t.Errorf("line %d has an escape: %q", i, line)
		}
	}

	// A logo wider than the terminal is a logo nobody sees: writeLogo drops it
	// rather than let it wrap, so it has to fit the width that is always there.
	if w := logoWidth(); w > 80 {
		t.Errorf("art is %d columns, wider than an 80-column terminal", w)
	}
}

// The mark is decoration, and decoration does not go into a pipe.
func TestWriteLogoSkipsWhatIsNotATerminal(t *testing.T) {
	f, err := os.CreateTemp(t.TempDir(), "logo")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	writeLogo(f)

	if info, err := f.Stat(); err != nil {
		t.Fatal(err)
	} else if info.Size() != 0 {
		t.Errorf("wrote %d bytes to a file", info.Size())
	}
	if logoFits(f) {
		t.Error("logoFits said yes to a file")
	}
}
