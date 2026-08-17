// Package summarize turns a conversation transcript into the few sentences that
// go on its markdown card.
//
// This is the one part of tennis that needs a network and a key. Search does
// not: ranking runs entirely on the built-in static embedder, so a machine with
// no key and no connection can still find everything it has already collected.
// Only writing the human-readable card requires a model, and only once per
// conversation.
//
// Requests are raw HTTP rather than a vendor SDK, matching embed/openai.go. An
// SDK would be a larger dependency than the entire rest of this program, for a
// single POST.
package summarize

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"
)

// ErrNoKey reports that no LLM credential was found. The CLI turns this into
// instructions rather than a stack trace, so it is a sentinel rather than a
// formatted string.
var ErrNoKey = errors.New("no LLM API key found")

// ErrRefused reports that the model declined to summarize a conversation.
// Collection continues with a fallback summary: a refusal on one transcript out
// of several hundred should cost that card its prose, not abort the run.
var ErrRefused = errors.New("model declined to summarize")

// Defaults. Both are overridable with TENNIS_SUMMARY_MODEL, which is the lever
// for anyone summarizing a large backlog who would rather trade some quality
// for cost — a first collection can be hundreds of conversations.
const (
	defaultAnthropicModel = "claude-opus-5"
	defaultOpenAIModel    = "gpt-4o-mini"

	anthropicURL = "https://api.anthropic.com/v1/messages"
	openaiURL    = "https://api.openai.com/v1/chat/completions"
)

// maxAttempts and retryWait bound retries on 429/5xx/network failures, matching
// the embedder's policy: doubling waits, and no retry on a 4xx that will not
// improve by asking again.
const (
	maxAttempts = 4
	retryWait   = time.Second
)

// Input caps. A long agent session runs to megabytes, which is both far more
// than a summary needs and enough to be expensive. The head carries what the
// conversation set out to do and the tail carries how it ended; the middle is
// where the repetitive tool work lives.
const (
	headChars = 24000
	tailChars = 8000
)

const systemPrompt = `You summarize AI chat transcripts for a personal archive.

Write 2-4 sentences of plain prose covering what the person was trying to do, what was decided or produced, and anything left unresolved. Be specific: name the actual files, systems, numbers, and decisions rather than describing them in the abstract.

Write only the summary. No preamble, no heading, no bullet points, no restating the title.`

// Input is a conversation reduced to what a summary needs. It is a plain
// struct rather than the importer's own type so this package stays independent
// of how conversations are read — every source ends up here the same shape.
type Input struct {
	Source     string
	Title      string
	Project    string
	Turns      int
	Transcript string
}

// Summarizer writes the prose for a conversation's card.
type Summarizer interface {
	Summarize(ctx context.Context, in Input) (string, error)
	Provider() string
}

// New picks a provider from the environment: Anthropic first, then OpenAI.
//
// The order is a preference, not a judgment — either works. What matters is
// that the choice is made from what is present rather than asked about, and
// that its absence is reported once, up front, before a collection has spent
// several minutes reading files it is about to be unable to summarize.
func New() (Summarizer, error) {
	model := os.Getenv("TENNIS_SUMMARY_MODEL")
	if key := os.Getenv("ANTHROPIC_API_KEY"); key != "" {
		if model == "" {
			model = defaultAnthropicModel
		}
		return &anthropic{key: key, model: model, client: httpClient(), baseURL: anthropicURL}, nil
	}
	if key := os.Getenv("OPENAI_API_KEY"); key != "" {
		if model == "" {
			model = defaultOpenAIModel
		}
		return &openai{key: key, model: model, client: httpClient(), baseURL: openaiURL}, nil
	}
	return nil, ErrNoKey
}

func httpClient() *http.Client { return &http.Client{Timeout: 120 * time.Second} }

// Fallback trims an opening message down to card length. It is what a card
// carries when there is no model, or when one call fails: the first thing a
// person types is almost always the clearest statement of what they wanted. A
// card with that on it is still useful; a collection that aborted because
// summary 400 of 550 failed is not.
func Fallback(firstUserTurn string) string {
	return truncateWords(strings.TrimSpace(firstUserTurn), 60)
}

// prompt assembles the user turn: metadata the transcript does not state, then
// the transcript itself, elided in the middle if long.
func prompt(in Input) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Source: %s\n", in.Source)
	if in.Project != "" {
		fmt.Fprintf(&b, "Project: %s\n", in.Project)
	}
	fmt.Fprintf(&b, "Turns: %d\n\n", in.Turns)

	t := in.Transcript
	if len(t) > headChars+tailChars {
		head := t[:alignRune(t, headChars)]
		tail := t[alignRune(t, len(t)-tailChars):]
		fmt.Fprintf(&b, "%s\n\n[... %d characters omitted ...]\n\n%s", head, len(t)-headChars-tailChars, tail)
	} else {
		b.WriteString(t)
	}
	return b.String()
}

// alignRune moves an index back to a UTF-8 boundary, so slicing a transcript
// never splits a multi-byte character into two pieces of mojibake.
func alignRune(s string, i int) int {
	if i <= 0 {
		return 0
	}
	if i >= len(s) {
		return len(s)
	}
	for i > 0 && s[i]&0xC0 == 0x80 {
		i--
	}
	return i
}

func truncateWords(s string, n int) string {
	fields := strings.Fields(s)
	if len(fields) <= n {
		return strings.Join(fields, " ")
	}
	return strings.Join(fields[:n], " ") + "…"
}

// retry runs do until it succeeds, it reports a non-retryable failure, or the
// attempts run out.
func retry(ctx context.Context, do func() (string, bool, error)) (string, error) {
	var lastErr error
	for attempt := 0; attempt < maxAttempts; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(retryWait << (attempt - 1)):
			}
		}
		out, retryable, err := do()
		if err == nil {
			return out, nil
		}
		if !retryable {
			return "", err
		}
		lastErr = err
	}
	return "", fmt.Errorf("giving up after %d attempts: %w", maxAttempts, lastErr)
}

func postJSON(ctx context.Context, client *http.Client, url string, headers map[string]string, body any) (*http.Response, error) {
	raw, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	return client.Do(req)
}

// --- Anthropic --------------------------------------------------------------

type anthropic struct {
	key    string
	model  string
	client *http.Client

	// baseURL is overridden in tests, matching embed/openai.go. Production
	// values are set by New and never vary.
	baseURL string
}

func (a *anthropic) Provider() string { return "anthropic:" + a.model }

type anthropicRequest struct {
	Model        string             `json:"model"`
	MaxTokens    int                `json:"max_tokens"`
	System       string             `json:"system"`
	Messages     []anthropicMessage `json:"messages"`
	OutputConfig anthropicOutput    `json:"output_config"`
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Effort is the cost and latency lever. Summarizing a transcript is a short,
// scoped task, and low effort is strong on current models — notably stronger
// than turning thinking off, which is the other way to make a request cheap and
// which introduces its own failure modes.
type anthropicOutput struct {
	Effort string `json:"effort"`
}

type anthropicResponse struct {
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	StopReason string `json:"stop_reason"`
	Error      *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// anthropicMaxTokens bounds thinking and response text together, so it needs
// headroom well past the length of the summary itself — a cap sized to the
// prose alone truncates the answer while the model is still thinking.
const anthropicMaxTokens = 2048

func (a *anthropic) Summarize(ctx context.Context, in Input) (string, error) {
	return retry(ctx, func() (string, bool, error) {
		resp, err := postJSON(ctx, a.client, a.baseURL, map[string]string{
			"x-api-key":         a.key,
			"anthropic-version": "2023-06-01",
		}, anthropicRequest{
			Model:        a.model,
			MaxTokens:    anthropicMaxTokens,
			System:       systemPrompt,
			Messages:     []anthropicMessage{{Role: "user", Content: prompt(in)}},
			OutputConfig: anthropicOutput{Effort: "low"},
		})
		if err != nil {
			return "", true, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return "", true, fmt.Errorf("anthropic: HTTP %d", resp.StatusCode)
		}
		var parsed anthropicResponse
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			return "", false, fmt.Errorf("decoding anthropic response (HTTP %d): %w", resp.StatusCode, err)
		}
		if parsed.Error != nil {
			return "", false, fmt.Errorf("anthropic: %s", parsed.Error.Message)
		}
		if resp.StatusCode != http.StatusOK {
			return "", false, fmt.Errorf("anthropic: HTTP %d", resp.StatusCode)
		}
		// Checked before reading content: a refusal is a successful HTTP 200
		// whose content is empty or partial, so indexing it first would panic on
		// exactly the transcripts most likely to trip a classifier.
		if parsed.StopReason == "refusal" {
			return "", false, ErrRefused
		}

		var out []string
		for _, blk := range parsed.Content {
			if blk.Type == "text" && strings.TrimSpace(blk.Text) != "" {
				out = append(out, blk.Text)
			}
		}
		if len(out) == 0 {
			return "", false, fmt.Errorf("anthropic returned no text")
		}
		return strings.TrimSpace(strings.Join(out, "\n")), false, nil
	})
}

// --- OpenAI -----------------------------------------------------------------

type openai struct {
	key     string
	model   string
	client  *http.Client
	baseURL string
}

func (o *openai) Provider() string { return "openai:" + o.model }

type openaiChatRequest struct {
	Model    string          `json:"model"`
	Messages []openaiChatMsg `json:"messages"`
	MaxTok   int             `json:"max_completion_tokens"`
}

type openaiChatMsg struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type openaiChatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

func (o *openai) Summarize(ctx context.Context, in Input) (string, error) {
	return retry(ctx, func() (string, bool, error) {
		resp, err := postJSON(ctx, o.client, o.baseURL, map[string]string{
			"Authorization": "Bearer " + o.key,
		}, openaiChatRequest{
			Model:  o.model,
			MaxTok: anthropicMaxTokens,
			Messages: []openaiChatMsg{
				{Role: "system", Content: systemPrompt},
				{Role: "user", Content: prompt(in)},
			},
		})
		if err != nil {
			return "", true, err
		}
		defer resp.Body.Close()

		if resp.StatusCode == http.StatusTooManyRequests || resp.StatusCode >= 500 {
			return "", true, fmt.Errorf("openai: HTTP %d", resp.StatusCode)
		}
		var parsed openaiChatResponse
		if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
			return "", false, fmt.Errorf("decoding openai response (HTTP %d): %w", resp.StatusCode, err)
		}
		if parsed.Error != nil {
			return "", false, fmt.Errorf("openai: %s", parsed.Error.Message)
		}
		if resp.StatusCode != http.StatusOK {
			return "", false, fmt.Errorf("openai: HTTP %d", resp.StatusCode)
		}
		if len(parsed.Choices) == 0 || strings.TrimSpace(parsed.Choices[0].Message.Content) == "" {
			return "", false, fmt.Errorf("openai returned no text")
		}
		return strings.TrimSpace(parsed.Choices[0].Message.Content), false, nil
	})
}
