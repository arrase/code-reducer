package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	defaultHTTPTimeout = 10 * time.Minute
	maxErrorBodyBytes  = 1024
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type llmCaller interface {
	CallLLM(ctx context.Context, systemPrompt, userPrompt string) (string, error)
	NumCtx() int
}

type llmClient struct {
	modelID    string
	baseURL    string
	numCtx     int
	onStatus   StatusFunc
	httpClient *http.Client
}

func newLLMClient(modelID, baseURL string, numCtx int, onStatus StatusFunc) *llmClient {
	return &llmClient{
		modelID:    modelID,
		baseURL:    baseURL,
		numCtx:     numCtx,
		onStatus:   onStatus,
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
	Options  *ollamaOptions `json:"options,omitempty"`
}

type ollamaOptions struct {
	NumCtx      int     `json:"num_ctx,omitempty"`
	Temperature float64 `json:"temperature"`
}

type ollamaResponse struct {
	Message    Message `json:"message"`
	DoneReason string  `json:"done_reason"`
}

// prepareOllamaRequest serializes the payload for the Ollama api/chat endpoint.
func (c *llmClient) prepareOllamaRequest(systemPrompt, userPrompt string) ([]byte, error) {
	reqBody := ollamaRequest{
		Model: c.modelID,
		Messages: []Message{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: userPrompt},
		},
		Stream: false,
		Options: &ollamaOptions{
			NumCtx:      c.numCtx,
			Temperature: 0.1,
		},
	}
	return json.Marshal(reqBody)
}

// CallLLM invokes the LLM via HTTP failing fast without retries.
func (c *llmClient) CallLLM(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	bodyBytes, err := c.prepareOllamaRequest(systemPrompt, userPrompt)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	url := strings.TrimSuffix(c.baseURL, "/") + "/api/chat"
	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode == http.StatusOK {
		respData, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		var result ollamaResponse
		if err := json.Unmarshal(respData, &result); err != nil {
			return "", fmt.Errorf("failed to parse response: %w", err)
		}
		if result.DoneReason == "length" {
			c.onStatus("Warning: LLM response was truncated due to context length limit (done_reason: length)")
		}
		return result.Message.Content, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return "", fmt.Errorf("ollama api error: status %d, response: %s", resp.StatusCode, string(body))
}
