package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/arrase/code-reducer/internal/config"
)

const doneReasonLength = "length"

var (
	// ErrTruncatedOutput indicates the model stopped because it reached the generation
	// limit, so the response is incomplete and must never be persisted or cached.
	ErrTruncatedOutput = errors.New("llm output was truncated: generation limit reached before completion")
	// ErrEmptyOutput indicates the model returned no visible content, which happens when
	// a reasoning model spends the whole output budget on hidden thinking.
	ErrEmptyOutput = errors.New("llm returned no visible content")
)

// Message is a single chat message. Thinking carries the reasoning block emitted by
// reasoning models and is never populated on responses.
type Message struct {
	Role     string  `json:"role"`
	Content  string  `json:"content"`
	Thinking *string `json:"thinking,omitempty"`
}

// userMessage joins the ordered parts of one user turn. Order matters for prompt
// caching: Ollama reuses the cached tokens of an unchanged prompt prefix, so the
// parts a run keeps constant must come first and the per-call text must follow
// them. Every call of a run shares the system message (cfg.SystemPrompt) and puts
// its own instruction in this turn.
func userMessage(parts ...string) string {
	kept := make([]string, 0, len(parts))
	for _, part := range parts {
		if strings.TrimSpace(part) != "" {
			kept = append(kept, part)
		}
	}
	return strings.Join(kept, "\n\n")
}

// LLMResult carries the generated text together with the generation metadata
// reported by the provider.
type LLMResult struct {
	Content     string
	DoneReason  string
	EvalCount   int
	PromptCount int
}

type llmCaller interface {
	CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (LLMResult, error)
	CallLLMWithNumPredict(ctx context.Context, systemPrompt string, messages []Message, numPredict int) (LLMResult, error)
	NumCtx() int
}

type llmClient struct {
	modelID    string
	baseURL    string
	numCtx     int
	think      bool
	numPredict int
	httpClient *http.Client
}

func newLLMClient(cfg *config.Config) *llmClient {
	return &llmClient{
		modelID:    cfg.ModelID,
		baseURL:    cfg.OllamaBaseURL,
		numCtx:     cfg.OllamaNumCtx,
		think:      cfg.Think,
		numPredict: cfg.NumPredict,
		httpClient: &http.Client{Timeout: defaultHTTPTimeout},
	}
}

func (c *llmClient) NumCtx() int {
	return c.numCtx
}

// Structs for Ollama requests and responses
type ollamaRequest struct {
	Model    string         `json:"model"`
	Messages []Message      `json:"messages"`
	Stream   bool           `json:"stream"`
	Think    *bool          `json:"think,omitempty"`
	Format   string         `json:"format,omitempty"`
	Options  *ollamaOptions `json:"options,omitempty"`
}

type ollamaOptions struct {
	NumCtx     int `json:"num_ctx,omitempty"`
	NumPredict int `json:"num_predict,omitempty"`
}

type ollamaResponse struct {
	Message         Message `json:"message"`
	DoneReason      string  `json:"done_reason"`
	EvalCount       int     `json:"eval_count"`
	PromptEvalCount int     `json:"prompt_eval_count"`
}

// requireCompleteContent rejects LLM output that must never be written to disk or
// cached. A generation that stopped on "length" is incomplete, and an empty content
// body means a reasoning model consumed the whole output budget on thinking.
func requireCompleteContent(res LLMResult, subject string) (string, error) {
	if res.DoneReason == doneReasonLength {
		return "", fmt.Errorf("%s: %w (prompt_tokens=%d, generated_tokens=%d): raise num_predict, raise slot_num_predict or paragraph_num_predict for document slots, or set think: false if a reasoning model is charging its thinking tokens against this budget",
			subject, ErrTruncatedOutput, res.PromptCount, res.EvalCount)
	}
	content := stripOuterMarkdownFence(res.Content)
	if content == "" {
		return "", fmt.Errorf("%s: %w: the model emitted only reasoning output, set think: false to disable it", subject, ErrEmptyOutput)
	}
	return content, nil
}

// prepareOllamaRequest creates and serializes the HTTP request for the Ollama api/chat endpoint.
// A numPredict of zero falls back to the client-wide generation budget.
func (c *llmClient) prepareOllamaRequest(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool, numPredict int) (*http.Request, error) {
	url := strings.TrimSuffix(c.baseURL, "/") + "/api/chat"

	if numPredict <= 0 {
		numPredict = c.numPredict
	}

	reqBody := ollamaRequest{
		Model:    c.modelID,
		Messages: append([]Message{{Role: "system", Content: systemPrompt}}, messages...),
		Stream:   false,
		Think:    &c.think,
		Options:  &ollamaOptions{NumCtx: c.numCtx, NumPredict: numPredict},
	}
	if jsonFormat {
		reqBody.Format = "json"
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	return req, nil
}

// CallLLM invokes the LLM via HTTP failing fast without retries.
func (c *llmClient) CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (LLMResult, error) {
	return c.call(ctx, systemPrompt, messages, jsonFormat, 0)
}

// CallLLMWithNumPredict invokes the LLM with a per-call generation bound, used by
// document prose slots so a micro-task cannot expand into a full-page generation.
func (c *llmClient) CallLLMWithNumPredict(ctx context.Context, systemPrompt string, messages []Message, numPredict int) (LLMResult, error) {
	return c.call(ctx, systemPrompt, messages, false, numPredict)
}

func (c *llmClient) call(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool, numPredict int) (LLMResult, error) {

	req, err := c.prepareOllamaRequest(ctx, systemPrompt, messages, jsonFormat, numPredict)
	if err != nil {
		return LLMResult{}, err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return LLMResult{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		respData, err := io.ReadAll(resp.Body)
		if err != nil {
			return LLMResult{}, err
		}

		var result ollamaResponse
		if err := json.Unmarshal(respData, &result); err != nil {
			return LLMResult{}, fmt.Errorf("failed to parse response: %w", err)
		}
		return LLMResult{
			Content:     result.Message.Content,
			DoneReason:  result.DoneReason,
			EvalCount:   result.EvalCount,
			PromptCount: result.PromptEvalCount,
		}, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return LLMResult{}, fmt.Errorf("ollama api error: status %d, response: %s", resp.StatusCode, string(body))
}
