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
		model:  model,
		key:    key,
		dims:   dims,
		client: &http.Client{Timeout: 60 * time.Second},
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

func (o *OpenAI) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	body, err := json.Marshal(openAIRequest{Input: texts, Model: o.model})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://api.openai.com/v1/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+o.key)

	resp, err := o.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	var parsed openAIResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return nil, fmt.Errorf("decoding openai response (HTTP %d): %w", resp.StatusCode, err)
	}
	if parsed.Error != nil {
		return nil, fmt.Errorf("openai: %s", parsed.Error.Message)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openai: HTTP %d", resp.StatusCode)
	}
	if len(parsed.Data) != len(texts) {
		return nil, fmt.Errorf("openai returned %d embeddings for %d inputs", len(parsed.Data), len(texts))
	}

	// The API documents that it may return items out of order, so place each by
	// its stated index rather than trusting the slice order.
	out := make([][]float32, len(texts))
	for _, d := range parsed.Data {
		if d.Index < 0 || d.Index >= len(out) {
			return nil, fmt.Errorf("openai returned out-of-range index %d", d.Index)
		}
		if len(d.Embedding) != o.dims {
			return nil, fmt.Errorf("openai returned %d dims, expected %d", len(d.Embedding), o.dims)
		}
		out[d.Index] = normalize(d.Embedding)
	}
	for i, v := range out {
		if v == nil {
			return nil, fmt.Errorf("openai returned no embedding for input %d", i)
		}
	}
	return out, nil
}
