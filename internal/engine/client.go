package engine

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type llmCaller interface {
	CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (string, error)
	NumCtx() int
}

type llmClient struct {
	modelID    string
	baseURL    string
	numCtx     int
	httpClient *http.Client
}

func newLLMClient(modelID, baseURL string, numCtx int) *llmClient {
	return &llmClient{
		modelID:    modelID,
		baseURL:    baseURL,
		numCtx:     numCtx,
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
	Format   string         `json:"format,omitempty"`
	Options  *ollamaOptions `json:"options,omitempty"`
}

type ollamaOptions struct {
	NumCtx int `json:"num_ctx,omitempty"`
}

type ollamaResponse struct {
	Message Message `json:"message"`
}

// prepareOllamaRequest creates and serializes the HTTP request for the Ollama api/chat endpoint.
func (c *llmClient) prepareOllamaRequest(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (*http.Request, error) {
	url := strings.TrimSuffix(c.baseURL, "/") + "/api/chat"

	reqBody := ollamaRequest{
		Model:    c.modelID,
		Messages: append([]Message{{Role: "system", Content: systemPrompt}}, messages...),
		Stream:   false,
		Options:  &ollamaOptions{NumCtx: c.numCtx},
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
func (c *llmClient) CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (string, error) {

	req, err := c.prepareOllamaRequest(ctx, systemPrompt, messages, jsonFormat)
	if err != nil {
		return "", err
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		respData, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}

		var result ollamaResponse
		if err := json.Unmarshal(respData, &result); err != nil {
			return "", fmt.Errorf("failed to parse response: %w", err)
		}
		return result.Message.Content, nil
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, maxErrorBodyBytes))
	return "", fmt.Errorf("ollama api error: status %d, response: %s", resp.StatusCode, string(body))
}


