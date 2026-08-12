package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/satoricorp/tennis"
)

// TestParsePutDoc is the table test for the NDJSON contract itself: one
// object per line, "id" and "text" required, "attributes" optional.
func TestParsePutDoc(t *testing.T) {
	cases := []struct {
		name    string
		line    string
		wantErr bool
		want    tennis.Document
	}{
		{
			name: "happy path with attributes",
			line: `{"id":"a1","text":"hello world","attributes":{"kind":"event","cost":3}}`,
			want: tennis.Document{ID: "a1", Text: "hello world", Attributes: map[string]any{"kind": "event", "cost": float64(3)}},
		},
		{
			name: "attributes are optional",
			line: `{"id":"a2","text":"hi there"}`,
			want: tennis.Document{ID: "a2", Text: "hi there"},
		},
		{
			name:    "malformed json",
			line:    `{"id":"a3","text":`,
			wantErr: true,
		},
		{
			name:    "not an object",
			line:    `"just a string"`,
			wantErr: true,
		},
		{
			name:    "missing id",
			line:    `{"text":"no id here"}`,
			wantErr: true,
		},
		{
			name:    "empty id",
			line:    `{"id":"","text":"empty id"}`,
			wantErr: true,
		},
		{
			name:    "missing text",
			line:    `{"id":"a4"}`,
			wantErr: true,
		},
		{
			name:    "empty text",
			line:    `{"id":"a5","text":""}`,
			wantErr: true,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := parsePutDoc([]byte(c.line))
			if c.wantErr {
				if err == nil {
					t.Fatalf("expected an error, got doc %+v", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.ID != c.want.ID || got.Text != c.want.Text {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
			if !reflect.DeepEqual(got.Attributes, c.want.Attributes) {
				t.Errorf("attributes: got %v, want %v", got.Attributes, c.want.Attributes)
			}
		})
	}
}

// TestReadPutDocsSkipsBadLinesAndKeepsGoing is the batch-level contract: a
// malformed line costs that line, not the run, and is reported with its
// 1-based line number.
func TestReadPutDocsSkipsBadLinesAndKeepsGoing(t *testing.T) {
	input := strings.Join([]string{
		`{"id":"a1","text":"one"}`,
		``, // blank line: skipped silently, not a failure
		`not json at all`,
		`{"id":"a2","text":"two"}`,
		`{"text":"missing id"}`,
	}, "\n")

	var errOut bytes.Buffer
	docs, failed := readPutDocs(strings.NewReader(input), &errOut)

	if len(docs) != 2 || docs[0].ID != "a1" || docs[1].ID != "a2" {
		t.Fatalf("docs: got %+v, want [a1 a2]", docs)
	}
	if failed != 2 {
		t.Errorf("failed: got %d, want 2", failed)
	}
	msg := errOut.String()
	if !strings.Contains(msg, "line 3") {
		t.Errorf("stderr should cite line 3 (bad json), got: %q", msg)
	}
	if !strings.Contains(msg, "line 5") {
		t.Errorf("stderr should cite line 5 (missing id), got: %q", msg)
	}
}

func TestReadPutDocsAllGood(t *testing.T) {
	input := `{"id":"a1","text":"one"}` + "\n" + `{"id":"a2","text":"two","attributes":{"k":"v"}}` + "\n"
	var errOut bytes.Buffer
	docs, failed := readPutDocs(strings.NewReader(input), &errOut)
	if failed != 0 {
		t.Errorf("failed: got %d, want 0 (stderr: %s)", failed, errOut.String())
	}
	if len(docs) != 2 {
		t.Fatalf("docs: got %d, want 2", len(docs))
	}
}

func TestReadPutDocsEmptyInput(t *testing.T) {
	var errOut bytes.Buffer
	docs, failed := readPutDocs(strings.NewReader(""), &errOut)
	if failed != 0 || len(docs) != 0 {
		t.Errorf("empty input: got docs=%v failed=%d, want none", docs, failed)
	}
}

// --- CLI-level integration tests, exercising cmdPut and cmdMatch exactly as
// the binary would run them. These need the embedding model, so they skip
// cleanly (like the rest of the suite) when the 123MB weights are absent.

func putTestCache(t *testing.T) string {
	t.Helper()
	cache, err := filepath.Abs("../../testdata/cache")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "models", "potion-retrieval-32M", "model.safetensors")); err != nil {
		t.Skip("test model not present; skipping put integration tests")
	}
	return cache
}

// withStdin replaces os.Stdin for the duration of the test with a pipe fed
// from data, restoring the original on cleanup. cmdPut reads os.Stdin
// directly, the same way the real binary does, so this exercises the actual
// wiring rather than a stand-in.
func withStdin(t *testing.T, data string) {
	t.Helper()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	old := os.Stdin
	os.Stdin = r
	t.Cleanup(func() { os.Stdin = old })
	go func() {
		io.WriteString(w, data)
		w.Close()
	}()
}

// captureStdout runs fn with os.Stdout redirected to a pipe and returns
// everything written plus fn's own error. Output here is small (JSON summaries
// and a handful of match results), well under a pipe's buffer, so no
// concurrent drain is needed before fn returns.
func captureStdout(t *testing.T, fn func() error) (string, error) {
	t.Helper()
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	os.Stdout = w
	runErr := fn()
	w.Close()
	os.Stdout = old

	var buf bytes.Buffer
	if _, err := io.Copy(&buf, r); err != nil {
		t.Fatal(err)
	}
	return buf.String(), runErr
}

func TestPutEndToEnd(t *testing.T) {
	cache := putTestCache(t)
	t.Setenv("TENNIS_CACHE", cache)
	dbPath := filepath.Join(t.TempDir(), "put.sqlite")
	const ns = "agents"

	// Happy path: two documents, auto-creating the namespace.
	batch := strings.Join([]string{
		`{"id":"e1","text":"the deploy failed with a timeout","attributes":{"kind":"event","session":"s1"}}`,
		`{"id":"e2","text":"retrying the deploy after backoff","attributes":{"kind":"event","session":"s1"}}`,
	}, "\n") + "\n"

	withStdin(t, batch)
	out, err := captureStdout(t, func() error {
		return cmdPut([]string{"--db", dbPath, "--json", ns})
	})
	if err != nil {
		t.Fatalf("put: %v\noutput: %s", err, out)
	}
	res := decodePutResult(t, out)
	if res["written"] != float64(2) || res["skipped"] != float64(0) || res["failed"] != float64(0) {
		t.Fatalf("first put: want written=2 skipped=0 failed=0, got %v", res)
	}

	// Unchanged-skip: re-running the identical batch must write nothing.
	withStdin(t, batch)
	out, err = captureStdout(t, func() error {
		return cmdPut([]string{"--db", dbPath, "--json", ns})
	})
	if err != nil {
		t.Fatalf("put (repeat): %v\noutput: %s", err, out)
	}
	res = decodePutResult(t, out)
	if res["written"] != float64(0) || res["skipped"] != float64(2) {
		t.Errorf("repeat put: want written=0 skipped=2, got %v", res)
	}

	// Upsert-same-id: same ID with different text must be written again, not
	// skipped, and must replace rather than duplicate.
	withStdin(t, `{"id":"e1","text":"the deploy failed with a DIFFERENT timeout","attributes":{"kind":"event","session":"s1"}}`+"\n")
	out, err = captureStdout(t, func() error {
		return cmdPut([]string{"--db", dbPath, "--json", ns})
	})
	if err != nil {
		t.Fatalf("put (upsert): %v\noutput: %s", err, out)
	}
	res = decodePutResult(t, out)
	if res["written"] != float64(1) || res["skipped"] != float64(0) {
		t.Errorf("upsert put: want written=1 skipped=0, got %v", res)
	}

	// Malformed lines: must not abort the batch, must be reported, and must
	// cause a nonzero exit (an error return here).
	mixed := strings.Join([]string{
		`{"id":"e3","text":"a good line","attributes":{"kind":"event"}}`,
		`not json at all`,
		`{"text":"missing id"}`,
		`{"id":"e4","text":"another good line"}`,
	}, "\n") + "\n"
	withStdin(t, mixed)
	out, err = captureStdout(t, func() error {
		return cmdPut([]string{"--db", dbPath, "--json", ns})
	})
	if err == nil {
		t.Fatal("expected a nonzero-exit error when some lines fail to parse")
	}
	res = decodePutResult(t, out)
	if res["written"] != float64(2) {
		t.Errorf("partial batch: want written=2 (the good lines), got %v", res)
	}
	if res["failed"] != float64(2) {
		t.Errorf("partial batch: want failed=2, got %v", res)
	}

	// Attribute round-trip through match --json: yeet needs kind/session back
	// on every hit to render it and jump to the session.
	out, err = captureStdout(t, func() error {
		return cmdMatch([]string{"--db", dbPath, "--json", "-n", "10", ns, "deploy timeout"})
	})
	if err != nil {
		t.Fatalf("match: %v\noutput: %s", err, out)
	}
	var results []map[string]any
	if err := json.Unmarshal([]byte(out), &results); err != nil {
		t.Fatalf("match --json output did not parse: %v\noutput: %s", err, out)
	}
	var found bool
	for _, r := range results {
		if r["id"] != "e1" {
			continue
		}
		found = true
		attrs, ok := r["attributes"].(map[string]any)
		if !ok {
			t.Fatalf("e1 result has no attributes object: %v", r)
		}
		if attrs["kind"] != "event" || attrs["session"] != "s1" {
			t.Errorf("e1 attributes did not round-trip: %v", attrs)
		}
	}
	if !found {
		t.Fatalf("match did not return e1 among results: %v", results)
	}
}

func decodePutResult(t *testing.T, out string) map[string]any {
	t.Helper()
	var res map[string]any
	if err := json.Unmarshal([]byte(out), &res); err != nil {
		t.Fatalf("put --json output did not parse: %v\noutput: %s", err, out)
	}
	return res
}
