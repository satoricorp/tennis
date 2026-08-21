package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// ansi matches every escape sequence the styler can emit.
var ansi = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// The zero styler is the off switch, and off has to mean byte-for-byte
// identical: this is the styler every pipe, log and test sees.
func TestZeroStylerIsIdentity(t *testing.T) {
	var st styler
	for _, s := range []string{"", "add", "USAGE", "  --db <path>    database file"} {
		for name, f := range map[string]func(string) string{
			"ball": st.ball, "dim": st.dim, "bold": st.bold, "red": st.red,
		} {
			if got := f(s); got != s {
				t.Errorf("styler{}.%s(%q) = %q, want it untouched", name, s, got)
			}
		}
	}
	if got := colorizeHelp(helpText(false), st); got != helpText(false) {
		t.Error("colorizeHelp with the zero styler altered the text")
	}
}

// A regular file is not a terminal, so a styler for one must be off — the
// same reasoning logoFits applies to the mark.
func TestNewStylerRefusesAFile(t *testing.T) {
	f, err := os.Create(filepath.Join(t.TempDir(), "not-a-terminal"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if st := newStyler(f); st.level != colorOff {
		t.Errorf("newStyler on a file returned level %d, want off", st.level)
	}
}

// Color is decoration: strip the escapes and the help screen must read
// exactly as it does with no color at all. Anything else means the accent
// smuggled in or destroyed a character.
func TestColorizeHelpChangesNoText(t *testing.T) {
	st := styler{colorTrue}
	for _, agents := range []bool{false, true} {
		plain := helpText(agents)
		if got := ansi.ReplaceAllString(colorizeHelp(plain, st), ""); got != plain {
			t.Errorf("agents=%v: stripped help differs from plain help", agents)
		}
	}
}

// The pieces the accent should land on: headers bolded, commands greened,
// prose left alone.
func TestColorizeHelpPlacement(t *testing.T) {
	st := styler{color256}
	got := colorizeHelp(helpText(false), st)

	for _, want := range []string{
		st.bold("COMMANDS"),
		st.ball("version"), // a single-word entry is still a command
	} {
		if !strings.Contains(got, want) {
			t.Errorf("colorized help is missing %q", want)
		}
	}
	// Prose inside a section keeps no accent: "Without a source flag..." is a
	// sentence, not something to type.
	if strings.Contains(got, st.ball("Without")) {
		t.Error("colorizeHelp put the accent on prose")
	}
	// The command column is greened up to the gap, description untouched.
	if !strings.Contains(got, st.ball("add <path...>")) {
		t.Error("colorizeHelp did not green the add entry's typeable half")
	}
}

// The mark's paint must also strip back to the original art, line for line.
func TestColorizeLogoChangesNoText(t *testing.T) {
	st := styler{colorTrue}
	for _, line := range logoLines() {
		if got := ansi.ReplaceAllString(colorizeLogo(line, st), ""); got != line {
			t.Errorf("stripped logo line differs:\n got %q\nwant %q", got, line)
		}
	}
}
