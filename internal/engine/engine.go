package engine

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/security"
	"github.com/arrase/code-reducer/internal/tools"
)

type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type LLMClient struct {
	ModelID string
	BaseURL string
	NumCtx  int
}

func NewLLMClient(modelID, baseURL string, numCtx int) *LLMClient {
	return &LLMClient{
		ModelID: modelID,
		BaseURL: baseURL,
		NumCtx:  numCtx,
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

// CallLLM invokes the LLM via HTTP.
func (c *LLMClient) CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (string, error) {
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	url := strings.TrimSuffix(baseURL, "/") + "/api/chat"

	options := &ollamaOptions{NumCtx: c.NumCtx}
	if options.NumCtx <= 0 {
		options.NumCtx = 8192
	}

	reqBody := ollamaRequest{
		Model:    c.ModelID,
		Messages: append([]Message{{Role: "system", Content: systemPrompt}}, messages...),
		Stream:   false,
		Options:  options,
	}
	if jsonFormat {
		reqBody.Format = "json"
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", fmt.Errorf("ollama api error: status %d, response: %s", resp.StatusCode, string(body))
	}

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

// StreamLLM streams responses in real time from the LLM.
func (c *LLMClient) StreamLLM(ctx context.Context, systemPrompt string, messages []Message, onChunk func(string)) error {
	baseURL := c.BaseURL
	if baseURL == "" {
		baseURL = "http://localhost:11434"
	}
	url := strings.TrimSuffix(baseURL, "/") + "/api/chat"

	options := &ollamaOptions{NumCtx: c.NumCtx}
	if options.NumCtx <= 0 {
		options.NumCtx = 8192
	}

	reqBody := ollamaRequest{
		Model:    c.ModelID,
		Messages: append([]Message{{Role: "system", Content: systemPrompt}}, messages...),
		Stream:   true,
		Options:  options,
	}

	bodyBytes, err := json.Marshal(reqBody)
	if err != nil {
		return fmt.Errorf("failed to marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: 10 * time.Minute}
	resp, err := client.Do(req)
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

type Event struct {
	Type    string
	Message string
}

func stripOuterMarkdownFence(content string) string {
	trimmed := strings.TrimSpace(content)
	re := regexp.MustCompile("(?s)^\\x60{3,}(?:markdown|json)?\\s*(.*?)\\s*\\x60{3,}$")
	if matches := re.FindStringSubmatch(trimmed); len(matches) > 1 {
		return strings.TrimSpace(matches[1])
	}
	return trimmed
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

func (c *LLMClient) LoadSystemPrompt(command string) (string, error) {
	return c.GetDefaultSystemPrompt(command), nil
}

type DirNode struct {
	Path     string
	Files    []string
	Children map[string]*DirNode
}

func buildTree(files []string) *DirNode {
	root := &DirNode{Path: ".", Children: make(map[string]*DirNode)}
	for _, f := range files {
		d := filepath.Dir(f)
		if d == "." {
			root.Files = append(root.Files, f)
			continue
		}

		parts := strings.Split(d, string(filepath.Separator))
		curr := root
		currPath := ""
		for _, part := range parts {
			if currPath == "" {
				currPath = part
			} else {
				currPath = currPath + string(filepath.Separator) + part
			}
			if _, ok := curr.Children[part]; !ok {
				curr.Children[part] = &DirNode{Path: currPath, Children: make(map[string]*DirNode)}
			}
			curr = curr.Children[part]
		}
		curr.Files = append(curr.Files, f)
	}
	return root
}

func reduceInChunks(ctx context.Context, c *LLMClient, nodePath string, items []string, maxBatchSize int, logEvent func(string, string)) string {
	if len(items) == 0 {
		return ""
	}
	if len(items) <= maxBatchSize {
		prompt := fmt.Sprintf("Synthesize architecture for %s:\n%s", nodePath, strings.Join(items, "\n\n"))
		logEvent("status", fmt.Sprintf("➜ LLM Synthesizing chunk for %s (%d items)", nodePath, len(items)))
		res, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("module_synthesis"), []Message{{Role: "user", Content: prompt}}, false)
		if err != nil {
			logEvent("error", fmt.Sprintf("LLM Error: %v", err))
			return ""
		}
		return stripOuterMarkdownFence(res)
	}

	var intermediate []string
	for i := 0; i < len(items); i += maxBatchSize {
		end := i + maxBatchSize
		if end > len(items) {
			end = len(items)
		}
		intermediate = append(intermediate, reduceInChunks(ctx, c, nodePath, items[i:end], maxBatchSize, logEvent))
	}
	return reduceInChunks(ctx, c, nodePath, intermediate, maxBatchSize, logEvent)
}

func synthesizeNode(ctx context.Context, c *LLMClient, node *DirNode, repoRoot string, docsDir string, logEvent func(string, string)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	childSummaries := make(map[string]string)
	for name, child := range node.Children {
		sum, err := synthesizeNode(ctx, c, child, repoRoot, docsDir, logEvent)
		if err != nil {
			return "", err
		}
		if sum != "" {
			childSummaries[name] = sum
		}
	}

	var components []string
	for _, f := range node.Files {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		contentBytes, err := tools.ReadFileSafely(repoRoot, f)
		if err != nil {
			continue
		}
		contentStr := string(contentBytes)
		if len(contentStr) > 8000 {
			contentStr = contentStr[:8000] + "\n...(truncated)..."
		}

		logEvent("status", fmt.Sprintf("➜ Extracting file: %s", f))
		userMsg := fmt.Sprintf("Analyze this file: %s\n\n```\n%s\n```\n\nOutput ONLY a Markdown list of exported functions, classes, and data structures with a 1-sentence technical description for each. No fluff.", f, contentStr)
		facts, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("extract_file"), []Message{{Role: "user", Content: userMsg}}, false)
		if err != nil {
			logEvent("error", fmt.Sprintf("LLM Error extracting %s: %v", f, err))
			continue
		}
		components = append(components, fmt.Sprintf("### File: %s\n%s", filepath.Base(f), stripOuterMarkdownFence(facts)))
	}

	for childName, childSum := range childSummaries {
		components = append(components, fmt.Sprintf("### Subsystem: %s\n%s", childName, childSum))
	}

	if len(components) == 0 {
		return "", nil
	}

	logEvent("status", fmt.Sprintf("➜ Synthesizing directory: %s (%d total components)", node.Path, len(components)))
	finalSum := reduceInChunks(ctx, c, node.Path, components, 5, logEvent)

	safeName := strings.ReplaceAll(node.Path, string(filepath.Separator), "_")
	if safeName == "." || safeName == "" {
		safeName = "root"
	}
	modulePath := filepath.Join(docsDir, "modules", safeName+".md")
	tools.WriteFileSafely(repoRoot, modulePath, []byte(finalSum))

	return finalSum, nil
}

func (c *LLMClient) RunInit(ctx context.Context, repoRoot string, userMessage string, onEvent func(Event)) error {
	logEvent := func(t, m string) {
		if onEvent != nil {
			onEvent(Event{Type: t, Message: m})
		}
	}

	logEvent("status", "Starting Code-Reducer V3 Map-Reduce pipeline: init")

	_ = security.EnsureGitignoreHasLockfile(repoRoot)

	logEvent("status", "Step 1: Code Discovery & Building Tree...")
	var ignores []string
	docsDir := "wiki"
	if cfg, err := config.LoadConfig(repoRoot); err == nil && cfg != nil {
		ignores = cfg.Ignore
		if cfg.DocsDir != "" {
			docsDir = cfg.DocsDir
		}
	}
	ignores = append(ignores, docsDir)

	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		return err
	}

	tree := buildTree(codeFiles)
	modulesDir := filepath.Join(repoRoot, docsDir, "modules")
	os.MkdirAll(modulesDir, 0755)

	logEvent("status", "Step 2: Hierarchical Tree-Merging (Map-Reduce)...")
	rootSum, err := synthesizeNode(ctx, c, tree, repoRoot, docsDir, logEvent)
	if err != nil {
		return err
	}

	logEvent("status", "Step 3: Global Architecture Synthesis...")
	archMsg := fmt.Sprintf("Write the global architecture overview (%s/architecture.md) based on the root summary.\n\n%s", docsDir, rootSum)
	archDoc, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("architecture"), []Message{{Role: "user", Content: archMsg}}, false)
	if err == nil {
		tools.WriteFileSafely(repoRoot, filepath.Join(docsDir, "architecture.md"), []byte(stripOuterMarkdownFence(archDoc)))
	}

	logEvent("status", "Step 4: Generating Quickstart...")
	qsMsg := fmt.Sprintf("Write the %s/quickstart.md page based on this architecture.\n\n%s", docsDir, rootSum)
	qsDoc, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("architecture"), []Message{{Role: "user", Content: qsMsg}}, false)
	if err == nil {
		tools.WriteFileSafely(repoRoot, filepath.Join(docsDir, "quickstart.md"), []byte(stripOuterMarkdownFence(qsDoc)))
	}

	logEvent("status", "Step 5: Updating AGENT.md...")
	agentFilePath := "AGENT.md"
	agentGuidelines := fmt.Sprintf(`# AI Agent Guidelines

This repository contains automatically generated documentation under the %s directory to help AI coding agents understand the system architecture, design patterns, and module structure:

- **System Blueprint**: Refer to %s/architecture.md for a high-level system overview, module relationships, and boundary definitions.
- **Developer Quickstart**: Refer to %s/quickstart.md for onboarding steps, coding patterns, and configuration settings.
- **Module Details**: Explore %s/modules/ for directory-level summaries and API descriptions of internal packages.

Before making changes, analyze these files to align with existing design choices and code structures.
`, docsDir, docsDir, docsDir, docsDir)

	agentFileBytes, err := tools.ReadFileSafely(repoRoot, agentFilePath)
	if err != nil {
		// File does not exist or read failed, write new
		_ = tools.WriteFileSafely(repoRoot, agentFilePath, []byte(agentGuidelines))
	} else {
		content := string(agentFileBytes)
		if !strings.Contains(content, "AI Agent Guidelines") {
			separator := "\n\n"
			if strings.HasSuffix(content, "\n\n") {
				separator = ""
			} else if strings.HasSuffix(content, "\n") {
				separator = "\n"
			}
			newContent := content + separator + agentGuidelines
			_ = tools.WriteFileSafely(repoRoot, agentFilePath, []byte(newContent))
		}
	}

	logEvent("status", "Pipeline completed successfully!")
	return nil
}
