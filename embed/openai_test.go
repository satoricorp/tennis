package embed

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeOpenAI answers the embeddings endpoint with deterministic vectors and
// lets a test script failures per request.
func fakeOpenAI(t *testing.T, dims int, failFirst int32, failStatus int) (*httptest.Server, *int32, *[]int) {
	t.Helper()
	var calls int32
	var batchSizes []int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := atomic.AddInt32(&calls, 1)
		var req openAIRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Errorf("bad request body: %v", err)
		}
		batchSizes = append(batchSizes, len(req.Input))
		if n <= failFirst {
			w.WriteHeader(failStatus)
			return
		}
		var resp openAIResponse
		resp.Data = make([]struct {
			Index     int       `json:"index"`
			Embedding []float32 `json:"embedding"`
		}, len(req.Input))
		for i := range req.Input {
			vec := make([]float32, dims)
			vec[0] = 1 // unit vector; normalize() must not change it
			// Return out of order on purpose: the client is documented to
			// place by index, so prove it.
			j := len(req.Input) - 1 - i
			resp.Data[i].Index = j
			resp.Data[i].Embedding = vec
		}
		json.NewEncoder(w).Encode(resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls, &batchSizes
}

func testClient(url string, maxBatch int) *OpenAI {
	return &OpenAI{
		model:     "text-embedding-3-small",
		key:       "test",
		dims:      OpenAIModels["text-embedding-3-small"],
		client:    http.DefaultClient,
		baseURL:   url,
		maxBatch:  maxBatch,
		retryWait: time.Millisecond,
	}
}

// Regression: the embeddings endpoint caps inputs per request, and the client
// used to send every chunk of a seed in one call — a large first seed on an
// OpenAI namespace simply errored.
func TestOpenAISplitsOversizedBatches(t *testing.T) {
	dims := OpenAIModels["text-embedding-3-small"]
	srv, calls, sizes := fakeOpenAI(t, dims, 0, 0)
	o := testClient(srv.URL, 2)

	texts := []string{"a", "b", "c", "d", "e"}
	vecs, err := o.Embed(context.Background(), texts)
	if err != nil {
		t.Fatal(err)
	}
	if len(vecs) != len(texts) {
		t.Fatalf("got %d vectors for %d inputs", len(vecs), len(texts))
	}
	if *calls != 3 {
		t.Errorf("5 inputs at maxBatch=2: want 3 requests, got %d (sizes %v)", *calls, *sizes)
	}
	for i, v := range vecs {
		if len(v) != dims || v[0] != 1 {
			t.Errorf("vector %d malformed: len=%d first=%v", i, len(v), v[0])
		}
	}
}

func TestOpenAIRetriesRateLimitThenSucceeds(t *testing.T) {
	dims := OpenAIModels["text-embedding-3-small"]
	srv, calls, _ := fakeOpenAI(t, dims, 2, http.StatusTooManyRequests)
	o := testClient(srv.URL, 100)

	if _, err := o.Embed(context.Background(), []string{"x"}); err != nil {
		t.Fatalf("should have survived two 429s: %v", err)
	}
	if *calls != 3 {
		t.Errorf("want 3 attempts (2 failures + success), got %d", *calls)
	}
}

func TestOpenAIDoesNotRetryClientErrors(t *testing.T) {
	dims := OpenAIModels["text-embedding-3-small"]
	srv, calls, _ := fakeOpenAI(t, dims, 99, http.StatusUnauthorized)
	o := testClient(srv.URL, 100)

	if _, err := o.Embed(context.Background(), []string{"x"}); err == nil {
		t.Fatal("401 must be an error")
	}
	if *calls != 1 {
		t.Errorf("a bad key does not get better with retries: want 1 attempt, got %d", *calls)
	}
}
