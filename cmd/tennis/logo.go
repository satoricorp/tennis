package main

// The mark, printed above the usage when tennis is asked what it is.
//
// logoart.go holds it — the website's wordmark and the ball off its shoulder,
// sampled by www/scripts/gen-logo.mts from the same font the site loads and the
// same module that raycasts the ball on the page.
//
// It is drawn in half blocks because a character cell is twice as tall as it is
// wide: one pixel to a cell and the ball comes out an ellipse, two pixels to a
// cell and it is round. Everything is in the terminal's own ink, so the mark
// reads whichever way round the theme runs, and the ball's seam is a gap cut
// out of the ball the same way it is a gap in the green on the page.

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

// logoLines is the mark, one string per terminal line.
func logoLines() []string {
	return strings.Split(strings.Trim(logoArt, "\n"), "\n")
}

// logoWidth is the mark's width in columns.
func logoWidth() int {
	width := 0
	for _, line := range logoLines() {
		if n := len([]rune(line)); n > width {
			width = n
		}
	}
	return width
}

// writeLogo prints the mark to f, when f is a terminal with room for it.
//
// A pipe gets nothing: `tennis --help | grep add` should find the line it is
// looking for and not a wall of block elements, and the mark is decoration in
// the exact sense that removing it costs no information.
func writeLogo(f *os.File) {
	if !logoFits(f) {
		return
	}
	for _, line := range logoLines() {
		fmt.Fprintln(f, line)
	}
	fmt.Fprintln(f)
}

// logoFits reports whether f is a terminal that can show the mark.
func logoFits(f *os.File) bool {
	if !term.IsTerminal(int(f.Fd())) {
		return false
	}
	// TERM=dumb is a terminal saying it cannot be relied on for anything past
	// the letters, and block elements are past the letters.
	if t := os.Getenv("TERM"); t == "" || t == "dumb" {
		return false
	}
	// Narrower than the mark and every line of it wraps, which is worse than
	// not printing it.
	w, _, err := term.GetSize(int(f.Fd()))
	return err == nil && w >= logoWidth()
}
