package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/satoricorp/tennis"
)

// newTestServer stands up the real mux over a temp database. Tests that need
// the embedding model skip when it is absent (CI runs without the 123MB
// weights; the full path runs locally).
func newTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cache, err := filepath.Abs("../../testdata/cache")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(cache, "models", "potion-retrieval-32M", "model.safetensors")); err != nil {
		t.Skip("test model not present; skipping HTTP API tests")
	}
	t.Setenv("TENNIS_CACHE", cache)

	db, err := tennis.Open(filepath.Join(t.TempDir(), "serve.sqlite"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	srv := httptest.NewServer(newServeMux(db))
	t.Cleanup(srv.Close)
	return srv
}

func post(t *testing.T, url, body string) (int, map[string]any) {
	t.Helper()
	resp, err := http.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var parsed map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		t.Fatalf("non-JSON response (HTTP %d): %v", resp.StatusCode, err)
	}
	return resp.StatusCode, parsed
}

func TestServeWriteQueryDelete(t *testing.T) {
	srv := newTestServer(t)

	code, body := post(t, srv.URL+"/v1/namespaces/notes/write",
		`{"documents":[{"id":"a1","text":"make the login flow remember the user","attributes":{"status":"merged"}}]}`)
	if code != 200 || body["written"] != float64(1) {
		t.Fatalf("write: HTTP %d %v", code, body)
	}

	code, body = post(t, srv.URL+"/v1/namespaces/notes/query",
		`{"text":"keep me signed in","top_k":5,"where":"status=merged"}`)
	if code != 200 {
		t.Fatalf("query: HTTP %d %v", code, body)
	}
	results := body["results"].([]any)
	if len(results) != 1 {
		t.Fatalf("query: want 1 result, got %v", results)
	}

	code, body = post(t, srv.URL+"/v1/namespaces/notes/delete", `{"ids":["a1"]}`)
	if code != 200 || body["deleted"] != float64(1) {
		t.Fatalf("delete: HTTP %d %v", code, body)
	}
}

func TestServeStatusCodes(t *testing.T) {
	srv := newTestServer(t)

	// Unknown namespace on query: 404 via the sentinel, not a doomed create.
	code, _ := post(t, srv.URL+"/v1/namespaces/nope/query", `{"text":"x"}`)
	if code != http.StatusNotFound {
		t.Errorf("missing namespace: want 404, got %d", code)
	}

	// Malformed JSON: 400.
	code, _ = post(t, srv.URL+"/v1/namespaces/notes/write", `{"documents": [`)
	if code != http.StatusBadRequest {
		t.Errorf("bad JSON: want 400, got %d", code)
	}

	// A body past the cap must be refused, not decoded forever.
	huge := `{"documents":[{"id":"big","text":"` + strings.Repeat("x", maxRequestBody) + `"}]}`
	code, _ = post(t, srv.URL+"/v1/namespaces/notes/write", huge)
	if code != http.StatusBadRequest {
		t.Errorf("oversized body: want 400, got %d", code)
	}
}
