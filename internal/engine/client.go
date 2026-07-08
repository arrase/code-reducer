package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMClient struct {
	ModelID    string
	BaseURL    string
	NumCtx     int
	HTTPClient *http.Client
}

func NewLLMClient(modelID, baseURL string, numCtx int) *LLMClient {
	return &LLMClient{
		ModelID:    modelID,
		BaseURL:    baseURL,
		NumCtx:     numCtx,
		HTTPClient: &http.Client{Timeout: 10 * time.Minute},
	}
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

type ollamaChunk struct {
	Message Message `json:"message"`
	Done    bool    `json:"done"`
}

// CallLLM invokes the LLM via HTTP failing fast without retries.
func (c *LLMClient) prepareOllamaRequest(ctx context.Context, systemPrompt string, messages []Message, stream bool, jsonFormat bool) (*http.Request, error) {
	url := strings.TrimSuffix(c.BaseURL, "/") + "/api/chat"

	reqBody := ollamaRequest{
		Model:    c.ModelID,
		Messages: append([]Message{{Role: "system", Content: systemPrompt}}, messages...),
		Stream:   stream,
		Options:  &ollamaOptions{NumCtx: c.NumCtx},
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
func (c *LLMClient) CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (string, error) {

	req, err := c.prepareOllamaRequest(ctx, systemPrompt, messages, false, jsonFormat)
	if err != nil {
		return "", err
	}

	resp, err := c.HTTPClient.Do(req)
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

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
	return "", fmt.Errorf("ollama api error: status %d, response: %s", resp.StatusCode, string(body))
}

// StreamLLM streams responses in real time from the LLM.
func (c *LLMClient) StreamLLM(ctx context.Context, systemPrompt string, messages []Message, onChunk func(string)) error {
	req, err := c.prepareOllamaRequest(ctx, systemPrompt, messages, true, false)
	if err != nil {
		return err
	}

	resp, err := c.HTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("ollama api error (stream): status %d, response: %s", resp.StatusCode, string(body))
	}

	reader := bufio.NewReader(resp.Body)
	for {
		line, err := reader.ReadString('\n')
		if err != nil {
			if err == io.EOF {
				break
			}
			return fmt.Errorf("error reading stream: %w", err)
		}

		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		var chunk ollamaChunk
		if err := json.Unmarshal([]byte(line), &chunk); err != nil {
			continue
		}
		if chunk.Message.Content != "" && onChunk != nil {
			onChunk(chunk.Message.Content)
		}
		if chunk.Done {
			break
		}
	}

	return nil
}

func (c *LLMClient) GetDefaultSystemPrompt(command string) string {
	basePrompt := "You are Code-Reducer, an expert technical writer and code analyzer. Your job is to strictly follow instructions. You do not yap, you do not write filler.\n"
	switch command {
	case "extract_file":
		return basePrompt + "Task: Analyze a single source code file.\nOutput: A strict Markdown list of all exported functions, classes, interfaces, and core data structures. Under each item, write EXACTLY ONE sentence explaining its technical purpose. Do not write any introductory paragraphs."
	case "module_synthesis":
		return basePrompt + "Task: Write a technical documentation page for a code module based on the provided list of its internal components.\nRule 1: Group related functions and classes under appropriate Markdown headings.\nRule 2: Explain the responsibility of the module and the data flow.\nRule 3: Keep it highly technical and dense."
	case "architecture":
		return basePrompt + "Task: Write a global architecture or quickstart document based on the module summaries.\nRule 1: Explain the system boundaries and how the modules interact.\nRule 2: Provide a dense, developer-friendly overview."
	default:
		return basePrompt
	}
}
