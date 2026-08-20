package main

// The mark, printed above the usage when tennis is asked what it is.
//
// Shadowed block lettering, the shape figlet has been printing since a terminal
// was the only place a logo could go: the letters are walls of █, and the
// box-drawing pieces are the shadow they throw. Drawn rather than derived, so
// it is edited here, by hand, as art.
//
// Forty-eight columns, which is the number to hold — an 80-column terminal is
// the one width that is always there, and a mark that wraps is worse than one
// that does not print.

import (
	"fmt"
	"os"
	"strings"

	"golang.org/x/term"
)

const logoArt = `
████████╗███████╗███╗   ██╗███╗   ██╗██╗███████╗
╚══██╔══╝██╔════╝████╗  ██║████╗  ██║██║██╔════╝
   ██║   █████╗  ██╔██╗ ██║██╔██╗ ██║██║███████╗
   ██║   ██╔══╝  ██║╚██╗██║██║╚██╗██║██║╚════██║
   ██║   ███████╗██║ ╚████║██║ ╚████║██║███████║
   ╚═╝   ╚══════╝╚═╝  ╚═══╝╚═╝  ╚═══╝╚═╝╚══════╝
`

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
	// The mark is box-drawing, so it wants a terminal that has those glyphs and
	// is reading UTF-8. TERM=dumb is a terminal saying it cannot be relied on
	// for anything past the letters, and this is past the letters.
	if t := os.Getenv("TERM"); t == "" || t == "dumb" {
		return false
	}
	// Narrower than the mark and every line of it wraps.
	w, _, err := term.GetSize(int(f.Fd()))
	return err == nil && w >= logoWidth()
}
