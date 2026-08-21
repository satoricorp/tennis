package main

// Color is decoration in the same sense the logo is: removing it costs no
// information. Everything here therefore collapses to the identity function
// the moment the stream is a pipe, the terminal is dumb, or the user has set
// NO_COLOR — output that is grepped, piped or logged never carries a single
// escape byte.
//
// One accent: the fluorescent yellow-green of the ball, #ccff00 — optic
// yellow, the dye tennis balls have worn since television found white ones
// hard to follow. A terminal that cannot mix it exactly gets the nearest
// thing it can say: a 256-color palette has it at index 190, and a 16-color
// terminal gets its bright green.

import (
	"os"
	"strings"

	"golang.org/x/term"
)

type colorLevel int

const (
	colorOff colorLevel = iota
	color16
	color256
	colorTrue
)

// styler colors text for one stream. The zero value is the off switch: every
// method hands back its argument untouched.
type styler struct{ level colorLevel }

// newStyler decides what f can show, by the conventional signals in the
// conventional order: NO_COLOR is the user saying no, TERM=dumb is the
// terminal saying it cannot, and a non-terminal is not a place color goes.
func newStyler(f *os.File) styler {
	if os.Getenv("NO_COLOR") != "" {
		return styler{}
	}
	termEnv := os.Getenv("TERM")
	if termEnv == "" || termEnv == "dumb" {
		return styler{}
	}
	if !term.IsTerminal(int(f.Fd())) {
		return styler{}
	}
	if ct := os.Getenv("COLORTERM"); ct == "truecolor" || ct == "24bit" {
		return styler{colorTrue}
	}
	if strings.Contains(termEnv, "256color") {
		return styler{color256}
	}
	return styler{color16}
}

const reset = "\x1b[0m"

// ball is the accent, at whatever fidelity the terminal offers.
func (s styler) ball(text string) string {
	switch s.level {
	case colorTrue:
		return "\x1b[38;2;204;255;0m" + text + reset
	case color256:
		return "\x1b[38;5;190m" + text + reset
	case color16:
		return "\x1b[92m" + text + reset
	}
	return text
}

// dim is for the secondary line: citations, table headers, the shadow under
// the mark — the parts a reader consults rather than reads.
func (s styler) dim(text string) string {
	if s.level == colorOff {
		return text
	}
	return "\x1b[2m" + text + reset
}

func (s styler) bold(text string) string {
	if s.level == colorOff {
		return text
	}
	return "\x1b[1m" + text + reset
}

// red is spent on exactly one word: the error: prefix.
func (s styler) red(text string) string {
	if s.level == colorOff {
		return text
	}
	return "\x1b[31m" + text + reset
}

// colorizeHelp applies the accent to a help screen without the constants ever
// knowing about it. Structure is recovered from the layout the text already
// has: an all-caps line is a section header, and an indented entry splits at
// its column gap into the half you type and the half you read.
func colorizeHelp(text string, st styler) string {
	if st.level == colorOff {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		switch {
		case i == 0 && strings.HasPrefix(line, "tennis "):
			lines[i] = st.ball("tennis") + strings.TrimPrefix(line, "tennis")
		case isSectionHeader(line):
			lines[i] = st.bold(line)
		case strings.HasPrefix(line, "  "):
			lines[i] = colorizeEntry(line, st)
		}
	}
	return strings.Join(lines, "\n")
}

// isSectionHeader reports whether a line is one of the screen's all-caps
// headings. Anything with a lowercase letter is prose and stays alone.
func isSectionHeader(line string) bool {
	if line == "" {
		return false
	}
	for _, r := range line {
		if (r < 'A' || r > 'Z') && r != ' ' {
			return false
		}
	}
	return true
}

// colorizeEntry greens the typeable half of an entry line: everything left of
// the column gap, or the whole line when it is a single word — version has
// nothing to say about itself, but it is still a command. A sentence inside a
// section has neither a gap nor a single word, and passes through untouched.
// Two spaces is enough to be a gap: nothing typeable contains two in a row,
// and the widest entry in a column sits that close to its description.
func colorizeEntry(line string, st styler) string {
	body := line[2:]
	if i := strings.Index(body, "  "); i > 0 {
		return "  " + st.ball(body[:i]) + body[i:]
	}
	if len(strings.Fields(body)) == 1 {
		return "  " + st.ball(strings.TrimSpace(body))
	}
	return line
}
