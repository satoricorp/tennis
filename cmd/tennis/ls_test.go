package main

import (
	"encoding/json"
	"errors"
	"path/filepath"
	"strings"
	"testing"

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
