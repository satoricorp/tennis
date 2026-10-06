package main

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/satoricorp/tennis"
)

// TestLSReadsNamespaceFromEnv is the regression test for a flag default that
// shadowed the environment: ls registered --ns with the default already filled
// in, so resolveNS never saw an empty flag and $TENNIS_NS was ignored by the
// one command people run first. A fresh database has no namespaces at all, so
// the not-found error names whichever namespace ls actually looked for, which
// is the fact under test and needs no model to establish.
func TestLSReadsNamespaceFromEnv(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "ls.sqlite")
	t.Setenv("TENNIS_NS", "fromenv")

	_, err := captureStdout(t, func() error {
		return cmdLS([]string{"--db", dbPath})
	})
	if !errors.Is(err, tennis.ErrNamespaceNotFound) {
		t.Fatalf("want namespace-not-found, got %v", err)
	}
	if !strings.Contains(err.Error(), `"fromenv"`) {
		t.Errorf("ls should target $TENNIS_NS: got %v", err)
	}

	// --ns still outranks the environment, the same way it does for search.
	_, err = captureStdout(t, func() error {
		return cmdLS([]string{"--db", dbPath, "--ns", "work"})
	})
	if err == nil || !strings.Contains(err.Error(), `"work"`) {
		t.Errorf("--ns should outrank $TENNIS_NS: got %v", err)
	}
}

// TestLSListsTheEnvNamespace goes the rest of the way with a real database:
// a document in the default namespace and one in another, $TENNIS_NS naming
// the other, and ls --json listing that one's document and nothing else.
func TestLSListsTheEnvNamespace(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_CARDS", t.TempDir())
	dbPath := filepath.Join(t.TempDir(), "ls.sqlite")

	add := func(ns, line string) {
		t.Helper()
		withStdin(t, line+"\n")
		out, err := captureStdout(t, func() error {
			return cmdAdd([]string{"--db", dbPath, "--json", "--ndjson", "--ns", ns})
		})
		if err != nil {
			t.Fatalf("add --ns %s: %v\noutput: %s", ns, err, out)
		}
	}
	add(defaultNamespace, `{"id":"d1","text":"the default namespace holds this","attributes":{"session":"s-default"}}`)
	add("fromenv", `{"id":"e1","text":"the environment's namespace holds this","attributes":{"session":"s-env"}}`)

	t.Setenv("TENNIS_NS", "fromenv")
	out, err := captureStdout(t, func() error {
		return cmdLS([]string{"--db", dbPath, "--json", "--docs"})
	})
	if err != nil {
		t.Fatalf("ls: %v\noutput: %s", err, out)
	}
	var infos []tennis.DocumentInfo
	if err := json.Unmarshal([]byte(out), &infos); err != nil {
		t.Fatalf("ls --json: %v\noutput: %s", err, out)
	}
	if len(infos) != 1 || infos[0].ID != "e1" {
		t.Errorf("ls should list $TENNIS_NS, not the default: got %s", out)
	}
}

// TestLSListsFiles is the README's quick start followed by ls: add a folder
// of notes, then ask what is there. ls grouped by session and dropped every
// document without one, so a namespace holding nothing but files was reported
// empty. Each file is a row of its own; a session added later takes its place
// among them by date.
func TestLSListsFiles(t *testing.T) {
	cache := ndjsonTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	t.Setenv("TENNIS_CARDS", t.TempDir())
	t.Setenv("TENNIS_NS", "")
	t.Setenv("ANTHROPIC_API_KEY", "")
	t.Setenv("OPENAI_API_KEY", "")
	dbPath := filepath.Join(t.TempDir(), "ls.sqlite")

	notes := t.TempDir()
	for name, mod := range map[string]string{
		"auth.md":   "2026-08-10T09:00:00Z",
		"config.md": "2026-08-20T09:00:00Z",
	} {
		p := filepath.Join(notes, name)
		if err := os.WriteFile(p, []byte("notes about "+name), 0o644); err != nil {
			t.Fatal(err)
		}
		at, _ := time.Parse(time.RFC3339, mod)
		if err := os.Chtimes(p, at, at); err != nil {
			t.Fatal(err)
		}
	}
	add := func(args ...string) {
		t.Helper()
		out, err := captureStdout(t, func() error {
			return cmdAdd(append([]string{"--db", dbPath, "--json"}, args...))
		})
		if err != nil {
			t.Fatalf("add %v: %v\noutput: %s", args, err, out)
		}
	}
	ls := func() (rows []string, tally string) {
		t.Helper()
		var out string
		stderr, err := captureStderr(t, func() error {
			var err error
			out, err = captureStdout(t, func() error {
				return cmdLS([]string{"--db", dbPath})
			})
			return err
		})
		if err != nil {
			t.Fatalf("ls: %v\nstdout: %s\nstderr: %s", err, out, stderr)
		}
		lines := strings.Split(strings.TrimSpace(out), "\n")
		return lines[1:], strings.TrimSpace(stderr) // past the header
	}

	add(notes)
	rows, tally := ls()
	if len(rows) != 2 || !strings.Contains(rows[0], "config.md") || !strings.Contains(rows[1], "auth.md") {
		t.Fatalf("ls after adding a folder listed:\n%s\nwant both files, newest first", strings.Join(rows, "\n"))
	}
	for _, row := range rows {
		if f := strings.Fields(row); len(f) < 5 || f[2] != "file" || f[3] != "1" {
			t.Errorf("file row %q, want SOURCE file and DOCS 1", row)
		}
	}
	if !strings.HasPrefix(rows[1], "2026-08-10") && !strings.HasPrefix(rows[1], "2026-08-09") {
		t.Errorf("auth.md row %q, want its modified date", rows[1])
	}
	if tally != "2 files" {
		t.Errorf("tally = %q, want 2 files", tally)
	}

	// A conversation from between the two files' dates lands between them.
	withStdin(t, `{"id":"c1:m1","text":"which config wins","attributes":{"source":"chatgpt","session":"c1","title":"Config precedence","created":"2026-08-15T12:00:00Z"}}`+"\n")
	add("--ndjson")
	rows, tally = ls()
	if len(rows) != 3 || !strings.Contains(rows[0], "config.md") ||
		!strings.Contains(rows[1], "Config precedence") || !strings.Contains(rows[2], "auth.md") {
		t.Fatalf("ls with a session and files listed:\n%s\nwant the session between the files by date", strings.Join(rows, "\n"))
	}
	if !strings.Contains(rows[1], "chatgpt") {
		t.Errorf("session row %q, want its source", rows[1])
	}
	if tally != "3 sessions and files" {
		t.Errorf("tally = %q, want 3 sessions and files", tally)
	}
}
