package embed

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"sort"
	"time"
)

// OpenAIModels are the models tennis will talk to, with their native widths.
var OpenAIModels = map[string]int{
	"text-embedding-3-small": 1536,
	"text-embedding-3-large": 3072,
}

// openaiMaxBatch is the most inputs the embeddings endpoint accepts in one
// request. Without splitting, the first large seed on an OpenAI namespace
// would simply error after the user had already waited on chunking.
const openaiMaxBatch = 2048

// openaiMaxAttempts bounds retries on 429/5xx/network failures; waits double
// between attempts (1s, 2s, 4s).
const openaiMaxAttempts = 4

// OpenAI embeds via the OpenAI API. It is never selected automatically.
//
// The temptation is to check for OPENAI_API_KEY and quietly use it when
// present. That produces the worst bug this design can have: a namespace
// indexed with the key set, then queried from a shell, a cron job, or CI where
// it is not, returns confident nonsense rather than an error. Requiring an
// explicit opt-in makes the choice visible once, and binding it to the
// namespace makes it enforced forever after.
//
// Choosing it also trades away two of the three things tennis promises. It is
// no longer offline, and it is no longer free.
type OpenAI struct {
	model  string
	key    string
	dims   int
	client *http.Client

	// Overridable in tests; production values set by NewOpenAI.
	baseURL   string
	maxBatch  int
	retryWait time.Duration
}

// NewOpenAI builds an OpenAI embedder. The key is read from OPENAI_API_KEY, but
// only ever because a caller explicitly asked for this embedder.
func NewOpenAI(model string) (*OpenAI, error) {
	dims, ok := OpenAIModels[model]
	if !ok {
		known := make([]string, 0, len(OpenAIModels))
		for k := range OpenAIModels {
			known = append(known, k)
		}
		sort.Strings(known)
		return nil, fmt.Errorf("unknown OpenAI model %q (have: %v)", model, known)
	}
	key := os.Getenv("OPENAI_API_KEY")
	if key == "" {
		return nil, fmt.Errorf("openai embedder requested but OPENAI_API_KEY is not set")
	}
	return &OpenAI{
		model:     model,
		key:       key,
		dims:      dims,
		client:    &http.Client{Timeout: 60 * time.Second},
		baseURL:   "https://api.openai.com/v1/embeddings",
		maxBatch:  openaiMaxBatch,
		retryWait: time.Second,
	}, nil
}

func (o *OpenAI) ID() string { return "openai:" + o.model }
func (o *OpenAI) Dims() int  { return o.dims }

type openAIRequest struct {
	Input []string `json:"input"`
	Model string   `json:"model"`
}

type openAIResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Embed splits texts into API-sized batches and retries transient failures.
func (o *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	out := make([][]float32, 0, len(texts))
	for start := 0; start < len(texts); start += o.maxBatch {
		end := min(start+o.maxBatch, len(texts))
		vecs, err := o.embedBatch(ctx, texts[start:end])
		if err != nil {
			return nil, err
		}
		out = append(out, vecs...)
	}
	return out, nil
}

// embedBatch sends one request, retrying rate limits, server errors, and
// network failures with doubling waits. 4xx other than 429 is not retried —
// a bad key or a malformed request will not get better by asking again.
func (o *OpenAI) embedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	var lastErr error
	for attempt := 0; attempt < openaiMaxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return nil, ctx.Err()
			case <-time.After(o.retryWait << (attempt - 1)):
			}
		}
		vecs, retryable, err := o.doRequest(ctx, texts)
		if err == nil {
			return vecs, nil
		}
		if !retryable {
			return nil, err
		}
		lastErr = err
	}
	return nil, fmt.Errorf("openai: giving up after %d attempts: %w", openaiMaxAttempts, lastErr)
}

func (o *OpenAI) doRequest(ctx context.Context, texts []string) (vecs [][]float32, retryable bool, err error) {
	body, err := json.Marshal(openAIRequest{Input: texts, Model: o.model})
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, o.baseURL, bytes.NewReader(body))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.key)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, true, err // network failure: worth retrying
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
		return nil, true, fmt.Errorf("openai: HTTP %d", resp.StatusCode)
	}

	var parsed openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, false, fmt.Errorf("decoding openai response (HTTP %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != nil {
		return nil, false, fmt.Errorf("openai: %s", parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, false, fmt.Errorf("openai: HTTP %d", resp.StatusCode)
	}
	if len(parsed.Data) != len(texts) {
		return nil, false, fmt.Errorf("openai returned %d embeddings for %d inputs", len(parsed.Data), len(texts))
	}

	// The API documents that it may return items out of order, so place each by
	// its stated index rather than trusting the slice order.
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, false, fmt.Errorf("openai returned out-of-range index %d", d.Index)
		}
		if len(d.Embedding) != o.dims {
			return nil, false, fmt.Errorf("openai returned %d dims, expected %d", len(d.Embedding), o.dims)
		}
		out[d.Index] = normalize(d.Embedding)
	}
	for i, v := range out {
		if v == nil {
			return nil, false, fmt.Errorf("openai returned no embedding for input %d", i)
		}
	}
	return out, false, nil
}
