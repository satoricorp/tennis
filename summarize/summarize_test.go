package summarize

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func in(transcript string) Input {
	return Input{Source: "chatgpt", Turns: 2, Transcript: transcript}
}

// TestKeyResolution: the provider is chosen from what is present, and its
// absence is a sentinel the CLI can turn into instructions rather than a
// stack trace.
func TestKeyResolution(t *testing.T) {
	t.Run("anthropic wins when both are set", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
		t.Setenv("OPENAI_API_KEY", "sk-x")
		t.Setenv("TENNIS_SUMMARY_MODEL", "")
		s, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if got := s.Provider(); got != "anthropic:"+defaultAnthropicModel {
			t.Errorf("Provider() = %q, want the Anthropic default", got)
		}
	})

	t.Run("falls through to openai", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "sk-x")
		t.Setenv("TENNIS_SUMMARY_MODEL", "")
		s, err := New()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(s.Provider(), "openai:") {
			t.Errorf("Provider() = %q, want an OpenAI provider", s.Provider())
		}
	})

	t.Run("model override applies to whichever provider is chosen", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "sk-ant-x")
		t.Setenv("OPENAI_API_KEY", "")
		t.Setenv("TENNIS_SUMMARY_MODEL", "claude-haiku-4-5")
		s, _ := New()
		if got := s.Provider(); got != "anthropic:claude-haiku-4-5" {
			t.Errorf("Provider() = %q, want the override honored", got)
		}
	})

	t.Run("no key is a sentinel", func(t *testing.T) {
		t.Setenv("ANTHROPIC_API_KEY", "")
		t.Setenv("OPENAI_API_KEY", "")
		_, err := New()
		if !errors.Is(err, ErrNoKey) {
			t.Errorf("err = %v, want ErrNoKey so the CLI can print instructions", err)
		}
	})
}

// TestFallbackIsOpeningMessage: with no key, or after a failed call, the card
// still gets something useful. The first thing a person types is usually the
// clearest statement of what they wanted.
func TestFallbackIsOpeningMessage(t *testing.T) {
	const opening = "How does the wash sale rule apply to ETFs?"
	if got := Fallback("  " + opening + "  "); got != opening {
		t.Errorf("Fallback = %q, want %q", got, opening)
	}
	if got := Fallback(""); got != "" {
		t.Errorf("Fallback on an empty turn = %q, want empty", got)
	}
	long := strings.Repeat("word ", 200)
	if got := Fallback(long); !strings.HasSuffix(got, "…") {
		t.Errorf("a long opening was not truncated: %q", got)
	}
}

// TestPromptTruncationIsRuneSafe: a long agent session runs to megabytes and
// gets elided in the middle. Slicing on a byte index would split a multi-byte
// character and send mojibake to the model.
func TestPromptTruncationIsRuneSafe(t *testing.T) {
	long := strings.Repeat("日本語のテキストです。", 6000) // multi-byte throughout
	p := prompt(in(long))

	if len(p) >= len(long) {
		t.Fatalf("prompt was not truncated: %d bytes for a %d-byte transcript", len(p), len(long))
	}
	if !strings.Contains(p, "characters omitted") {
		t.Error("truncation was not marked in the prompt")
	}
	if !utf8Valid(p) {
		t.Error("truncation split a multi-byte character")
	}
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == '�' {
			return false
		}
	}
	return true
}

// TestAnthropicRequestShape pins the parts of the request that are easy to get
// wrong and that fail loudly at the API: sampling parameters are rejected on
// current models, and effort is the cost lever this workload turns down.
func TestAnthropicRequestShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if got := r.Header.Get("x-api-key"); got != "sk-ant-test" {
			t.Errorf("x-api-key = %q", got)
		}
		if got := r.Header.Get("anthropic-version"); got == "" {
			t.Error("anthropic-version header is required and was not sent")
		}
		io.WriteString(w, `{"content":[{"type":"text","text":"A summary."}],"stop_reason":"end_turn"}`)
	}))
	defer srv.Close()

	a := &anthropic{key: "sk-ant-test", model: "claude-opus-5", client: srv.Client(), baseURL: srv.URL}
	got, err := a.Summarize(context.Background(), in("hi"))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "A summary." {
		t.Errorf("summary = %q", got)
	}

	for _, banned := range []string{"temperature", "top_p", "top_k", "thinking"} {
		if _, ok := body[banned]; ok {
			t.Errorf("request carries %q, which current models reject", banned)
		}
	}
	oc, ok := body["output_config"].(map[string]any)
	if !ok || oc["effort"] != "low" {
		t.Errorf("output_config = %v, want effort low", body["output_config"])
	}
	if body["max_tokens"] == nil {
		t.Error("max_tokens is required")
	}
	// Thinking and response text share max_tokens, so the cap needs headroom
	// well past the length of the summary itself.
	if n, _ := body["max_tokens"].(float64); n < 1024 {
		t.Errorf("max_tokens = %v, too tight to leave room for thinking", n)
	}
}

// TestAnthropicRefusal: a refusal is a successful HTTP 200 whose content is
// empty. Reading content[0] first would panic on exactly the transcripts most
// likely to trip a classifier.
func TestAnthropicRefusal(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, `{"content":[],"stop_reason":"refusal"}`)
	}))
	defer srv.Close()

	a := &anthropic{key: "k", model: "m", client: srv.Client(), baseURL: srv.URL}
	_, err := a.Summarize(context.Background(), in("hi"))
	if !errors.Is(err, ErrRefused) {
		t.Errorf("err = %v, want ErrRefused", err)
	}
}

// TestRetryOnTransientFailure: a rate limit mid-collection must not cost the
// card. A 4xx that will not improve by asking again must not be retried.
func TestRetryOnTransientFailure(t *testing.T) {
	t.Run("429 is retried", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			if calls < 3 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			io.WriteString(w, `{"content":[{"type":"text","text":"ok"}],"stop_reason":"end_turn"}`)
		}))
		defer srv.Close()

		a := &anthropic{key: "k", model: "m", client: srv.Client(), baseURL: srv.URL}
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		got, err := a.Summarize(ctx, in("hi"))
		if err != nil {
			t.Fatalf("Summarize: %v", err)
		}
		if got != "ok" || calls != 3 {
			t.Errorf("got %q after %d calls, want a successful retry", got, calls)
		}
	})

	t.Run("401 is not retried", func(t *testing.T) {
		calls := 0
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			calls++
			w.WriteHeader(http.StatusUnauthorized)
			io.WriteString(w, `{"error":{"message":"invalid x-api-key"}}`)
		}))
		defer srv.Close()

		a := &anthropic{key: "k", model: "m", client: srv.Client(), baseURL: srv.URL}
		_, err := a.Summarize(context.Background(), in("hi"))
		if err == nil {
			t.Fatal("want an error on 401")
		}
		if calls != 1 {
			t.Errorf("made %d calls, want 1 — a bad key does not improve on retry", calls)
		}
		if !strings.Contains(err.Error(), "invalid x-api-key") {
			t.Errorf("err = %v, want the API's own message surfaced", err)
		}
	})
}

func TestOpenAIRequestShape(t *testing.T) {
	var body map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &body)
		if got := r.Header.Get("Authorization"); got != "Bearer sk-test" {
			t.Errorf("Authorization = %q", got)
		}
		io.WriteString(w, `{"choices":[{"message":{"content":"A summary."}}]}`)
	}))
	defer srv.Close()

	o := &openai{key: "sk-test", model: "gpt-4o-mini", client: srv.Client(), baseURL: srv.URL}
	got, err := o.Summarize(context.Background(), in("hi"))
	if err != nil {
		t.Fatalf("Summarize: %v", err)
	}
	if got != "A summary." {
		t.Errorf("summary = %q", got)
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("sent %d messages, want a system turn and a user turn", len(msgs))
	}
}
