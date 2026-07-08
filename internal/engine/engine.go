package engine

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
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

type FileCacheEntry struct {
	SHA256 string `json:"sha256"`
	Facts  string `json:"facts"`
}

type MetadataCache struct {
	LastDocumentedCommit string                    `json:"last_documented_commit"`
	Files                map[string]FileCacheEntry `json:"files"`
	Modules              map[string]string         `json:"modules"`
}

func loadMetadataCache(repoRoot string, docsDir string) (*MetadataCache, error) {
	metadataPath := filepath.Join(docsDir, ".metadata.json")
	data, err := tools.ReadFileSafely(repoRoot, metadataPath)
	if err != nil {
		return &MetadataCache{
			Files:   make(map[string]FileCacheEntry),
			Modules: make(map[string]string),
		}, nil
	}
	var cache MetadataCache
	if err := json.Unmarshal(data, &cache); err != nil {
		return &MetadataCache{
			Files:   make(map[string]FileCacheEntry),
			Modules: make(map[string]string),
		}, nil
	}
	if cache.Files == nil {
		cache.Files = make(map[string]FileCacheEntry)
	}
	if cache.Modules == nil {
		cache.Modules = make(map[string]string)
	}
	return &cache, nil
}

func saveMetadataCache(repoRoot string, docsDir string, cache *MetadataCache) error {
	metadataPath := filepath.Join(docsDir, ".metadata.json")
	data, err := json.MarshalIndent(cache, "", "  ")
	if err != nil {
		return err
	}
	return tools.WriteFileSafely(repoRoot, metadataPath, data)
}

func computeSHA256(repoRoot, virtualPath string) (string, error) {
	content, err := tools.ReadFileSafely(repoRoot, virtualPath)
	if err != nil {
		return "", err
	}
	hash := sha256.Sum256(content)
	return hex.EncodeToString(hash[:]), nil
}



type FileChange struct {
	Path   string
	Status string // "Added", "Modified", "Deleted"
}

func isAllowedFile(repoRoot, relPath string, ignores []string) bool {
	if tools.ShouldIgnorePath(relPath, ignores) {
		return false
	}

	components := strings.Split(relPath, string(filepath.Separator))
	ignoredDirs := map[string]bool{
		".git":             true,
		"node_modules":     true,
		"dist":             true,
		"build":            true,
		"cache":            true,
		"code-reducer":     true,
		".gemini":          true,
		"bower_components": true,
		"__pycache__":      true,
		".pytest_cache":    true,
		".mypy_cache":      true,
		".tox":             true,
		"venv":             true,
		".venv":            true,
	}
	for _, comp := range components {
		if ignoredDirs[comp] || strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".egg-info") {
			return false
		}
	}

	name := filepath.Base(relPath)
	ext := strings.ToLower(filepath.Ext(name))
	if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".pdf" ||
		ext == ".exe" || ext == ".dll" || ext == ".so" || ext == ".o" || ext == ".a" ||
		ext == ".zip" || ext == ".gz" || ext == ".tar" || ext == ".lock" || ext == ".pyc" ||
		ext == ".pyo" || ext == ".pyd" || tools.NameSuffixIgnored(name) {
		return false
	}

	absPath, err := security.SafeResolve(repoRoot, relPath)
	if err != nil {
		return false
	}

	if tools.IsBinaryFile(absPath) {
		return false
	}

	return true
}

func propagateAffected(node *DirNode, affectedDirs map[string]bool) bool {
	isAffected := affectedDirs[node.Path]
	for _, child := range node.Children {
		if propagateAffected(child, affectedDirs) {
			isAffected = true
		}
	}
	if isAffected {
		affectedDirs[node.Path] = true
	}
	return isAffected
}

func determineAffected(node *DirNode, repoRoot, docsDir string, cache *MetadataCache, filteredChanges []FileChange, affectedDirs map[string]bool) {
	changedFiles := make(map[string]bool)
	for _, c := range filteredChanges {
		changedFiles[c.Path] = true
	}

	var checkNode func(n *DirNode)
	checkNode = func(n *DirNode) {
		for _, f := range n.Files {
			if changedFiles[f] {
				affectedDirs[n.Path] = true
				break
			}
		}

		safeName := strings.ReplaceAll(n.Path, string(filepath.Separator), "_")
		if safeName == "." || safeName == "" {
			safeName = "root"
		}
		modulePath := filepath.Join(docsDir, "modules", safeName+".md")
		absModulePath, err := security.SafeResolve(repoRoot, modulePath)
		if err == nil {
			if _, err := os.Stat(absModulePath); os.IsNotExist(err) {
				affectedDirs[n.Path] = true
			}
		}

		if cache.Modules[n.Path] == "" {
			affectedDirs[n.Path] = true
		}

		for _, child := range n.Children {
			checkNode(child)
		}
	}
	checkNode(node)
}

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

	if err := ctx.Err(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

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

type Event struct {
	Type    string
	Message string
}

var markdownFenceRe = regexp.MustCompile("(?s)^\\x60{3,}(?:markdown|json)?\\s*(.*?)\\s*\\x60{3,}$")

func stripOuterMarkdownFence(content string) string {
	trimmed := strings.TrimSpace(content)
	if matches := markdownFenceRe.FindStringSubmatch(trimmed); len(matches) > 1 {
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

func reduceInChunks(ctx context.Context, c *LLMClient, nodePath string, items []string, logEvent func(string, string)) (string, error) {
	if len(items) == 0 {
		return "", nil
	}

	var batches [][]string
	var currentBatch []string
	currentLen := 0

	for _, item := range items {
		if currentLen+len(item) > 15000 && len(currentBatch) > 0 {
			batches = append(batches, currentBatch)
			currentBatch = []string{item}
			currentLen = len(item)
		} else {
			currentBatch = append(currentBatch, item)
			currentLen += len(item)
		}
	}
	if len(currentBatch) > 0 {
		batches = append(batches, currentBatch)
	}

	if len(batches) == 1 {
		prompt := fmt.Sprintf("Synthesize architecture for %s:\n%s", nodePath, strings.Join(batches[0], "\n\n"))
		logEvent("status", fmt.Sprintf("➜ LLM Synthesizing chunk for %s (%d items)", nodePath, len(batches[0])))
		res, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("module_synthesis"), []Message{{Role: "user", Content: prompt}}, false)
		if err != nil {
			return "", fmt.Errorf("LLM error during synthesis: %w", err)
		}
		return stripOuterMarkdownFence(res), nil
	}

	var intermediate []string
	for _, batch := range batches {
		chunkRes, err := reduceInChunks(ctx, c, nodePath, batch, logEvent)
		if err != nil {
			return "", err
		}
		intermediate = append(intermediate, chunkRes)
	}
	return reduceInChunks(ctx, c, nodePath, intermediate, logEvent)
}

func synthesizeNode(ctx context.Context, c *LLMClient, node *DirNode, repoRoot string, docsDir string, cache *MetadataCache, affectedDirs map[string]bool, logEvent func(string, string)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// If this node (and all descendants) is NOT affected, reuse cached summary!
	if !affectedDirs[node.Path] && cache.Modules[node.Path] != "" {
		logEvent("status", fmt.Sprintf("➜ Reusing cached summary for directory: %s", node.Path))
		return cache.Modules[node.Path], nil
	}

	childSummaries := make(map[string]string)
	for name, child := range node.Children {
		sum, err := synthesizeNode(ctx, c, child, repoRoot, docsDir, cache, affectedDirs, logEvent)
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

		// Calculate current SHA256 of the file
		fileHash, err := computeSHA256(repoRoot, f)
		if err != nil {
			continue // Skip if file can't be read
		}

		var facts string
		cachedEntry, exists := cache.Files[f]
		if exists && cachedEntry.SHA256 == fileHash {
			facts = cachedEntry.Facts
		} else {
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
			res, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("extract_file"), []Message{{Role: "user", Content: userMsg}}, false)
			if err != nil {
				return "", fmt.Errorf("LLM error extracting %s: %w", f, err)
			}
			facts = stripOuterMarkdownFence(res)

			// Update cache
			cache.Files[f] = FileCacheEntry{
				SHA256: fileHash,
				Facts:  facts,
			}
		}

		components = append(components, fmt.Sprintf("### File: %s\n%s", filepath.Base(f), facts))
	}

	for childName, childSum := range childSummaries {
		components = append(components, fmt.Sprintf("### Subsystem: %s\n%s", childName, childSum))
	}

	if len(components) == 0 {
		cache.Modules[node.Path] = ""
		return "", nil
	}

	logEvent("status", fmt.Sprintf("➜ Synthesizing directory: %s (%d total components)", node.Path, len(components)))
	finalSum, err := reduceInChunks(ctx, c, node.Path, components, logEvent)
	if err != nil {
		return "", err
	}

	// Update module cache
	cache.Modules[node.Path] = finalSum

	safeName := strings.ReplaceAll(node.Path, string(filepath.Separator), "_")
	if safeName == "." || safeName == "" {
		safeName = "root"
	}
	modulePath := filepath.Join(docsDir, "modules", safeName+".md")
	if err := tools.WriteFileSafely(repoRoot, modulePath, []byte(finalSum)); err != nil {
		return "", fmt.Errorf("failed to write module documentation for %s: %w", node.Path, err)
	}

	return finalSum, nil
}

func (c *LLMClient) RunInit(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error {
	logEvent := func(t, m string) {
		if onEvent != nil {
			onEvent(Event{Type: t, Message: m})
		}
	}

	logEvent("status", "Starting Map-Reduce pipeline: init")

	logEvent("status", "Step 1: Code Discovery & Building Tree...")
	docsDir := cfg.DocsDir
	ignores := append(cfg.Ignore, docsDir)

	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		return err
	}

	tree := buildTree(codeFiles)
	modulesDir := filepath.Join(repoRoot, docsDir, "modules")
	if err := os.MkdirAll(modulesDir, 0755); err != nil {
		return fmt.Errorf("failed to create modules directory: %w", err)
	}

	logEvent("status", "Step 2: Hierarchical Tree-Merging (Map-Reduce)...")

	// Load existing cache (or initialize empty)
	cache, err := loadMetadataCache(repoRoot, docsDir)
	if err != nil {
		cache = &MetadataCache{
			Files:   make(map[string]FileCacheEntry),
			Modules: make(map[string]string),
		}
	}

	// Mark all directories as affected during init
	affectedDirs := make(map[string]bool)
	var markAllAffected func(n *DirNode)
	markAllAffected = func(n *DirNode) {
		affectedDirs[n.Path] = true
		for _, child := range n.Children {
			markAllAffected(child)
		}
	}
	markAllAffected(tree)

	rootSum, err := synthesizeNode(ctx, c, tree, repoRoot, docsDir, cache, affectedDirs, logEvent)
	if err != nil {
		return err
	}

	logEvent("status", "Step 3: Global Architecture Synthesis...")
	archMsg := fmt.Sprintf("Write the global architecture overview (%s/architecture.md) based on the root summary.\n\n%s", docsDir, rootSum)
	archDoc, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("architecture"), []Message{{Role: "user", Content: archMsg}}, false)
	if err != nil {
		return fmt.Errorf("failed to generate global architecture: %w", err)
	}
	if err := tools.WriteFileSafely(repoRoot, filepath.Join(docsDir, "architecture.md"), []byte(stripOuterMarkdownFence(archDoc))); err != nil {
		return fmt.Errorf("failed to write architecture.md: %w", err)
	}

	logEvent("status", "Step 4: Generating Quickstart...")
	qsMsg := fmt.Sprintf("Write the %s/quickstart.md page based on this architecture.\n\n%s", docsDir, rootSum)
	qsDoc, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("architecture"), []Message{{Role: "user", Content: qsMsg}}, false)
	if err != nil {
		return fmt.Errorf("failed to generate quickstart documentation: %w", err)
	}
	if err := tools.WriteFileSafely(repoRoot, filepath.Join(docsDir, "quickstart.md"), []byte(stripOuterMarkdownFence(qsDoc))); err != nil {
		return fmt.Errorf("failed to write quickstart.md: %w", err)
	}

	logEvent("status", "Step 5: Updating AGENTS.md...")
	agentFilePath := "AGENTS.md"
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

	// Save metadata cache
	cache.LastDocumentedCommit = "local"
	if err := saveMetadataCache(repoRoot, docsDir, cache); err != nil {
		logEvent("status", fmt.Sprintf("Warning: failed to save metadata cache: %v", err))
	}

	logEvent("status", "Pipeline completed successfully!")
	return nil
}

func (c *LLMClient) RunUpdate(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error {
	logEvent := func(t, m string) {
		if onEvent != nil {
			onEvent(Event{Type: t, Message: m})
		}
	}

	logEvent("status", "Starting Map-Reduce pipeline: update")

	docsDir := cfg.DocsDir
	ignores := append(cfg.Ignore, docsDir)

	// 1. Load existing cache
	cache, err := loadMetadataCache(repoRoot, docsDir)
	if err != nil {
		logEvent("status", fmt.Sprintf("Warning: failed to load metadata cache: %v. Rebuilding cache...", err))
		cache = &MetadataCache{
			Files:   make(map[string]FileCacheEntry),
			Modules: make(map[string]string),
		}
	}

	logEvent("status", "Step 1: Detecting changed files...")

	// 2. Discover all code files to build the current codebase tree
	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		return err
	}

	var filteredChanges []FileChange
	currentFilesMap := make(map[string]string)
	var allowedCodeFiles []string

	for _, f := range codeFiles {
		if isAllowedFile(repoRoot, f, ignores) {
			allowedCodeFiles = append(allowedCodeFiles, f)
			hash, err := computeSHA256(repoRoot, f)
			if err == nil {
				currentFilesMap[f] = hash
			}
		}
	}

	for f, currentHash := range currentFilesMap {
		cachedEntry, exists := cache.Files[f]
		if !exists {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: "Added"})
		} else if cachedEntry.SHA256 != currentHash {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: "Modified"})
		}
	}

	for f := range cache.Files {
		if _, exists := currentFilesMap[f]; !exists {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: "Deleted"})
		}
	}

	// Group changes by affected directories to initially mark them
	affectedDirs := make(map[string]bool)
	for _, change := range filteredChanges {
		dir := filepath.Dir(change.Path)
		affectedDirs[dir] = true

		if change.Status == "Deleted" {
			delete(cache.Files, change.Path)
		}
	}

	tree := buildTree(allowedCodeFiles)

	// Clean cache files that are no longer in the codebase (garbage collection)
	codeFileMap := make(map[string]bool)
	for _, f := range allowedCodeFiles {
		codeFileMap[f] = true
	}
	for f := range cache.Files {
		if !codeFileMap[f] {
			delete(cache.Files, f)
		}
	}

	// 4. Determine affected modules (including missing documentation files or missing cache summaries)
	determineAffected(tree, repoRoot, docsDir, cache, filteredChanges, affectedDirs)

	// Propagate affected status upwards
	propagateAffected(tree, affectedDirs)

	// If no modules are affected, we are done
	if len(affectedDirs) == 0 {
		logEvent("status", "No modifications detected. Documentation is up to date.")
		return nil
	}

	logEvent("status", fmt.Sprintf("Step 2: Hierarchical Tree-Merging (Map-Reduce)... (Affected modules: %d)", len(affectedDirs)))
	rootSum, err := synthesizeNode(ctx, c, tree, repoRoot, docsDir, cache, affectedDirs, logEvent)
	if err != nil {
		return err
	}

	// 5. Global Architecture & Quickstart Sync
	archPath := filepath.Join(docsDir, "architecture.md")
	qsPath := filepath.Join(docsDir, "quickstart.md")
	archExists := false
	qsExists := false
	if absArch, err := security.SafeResolve(repoRoot, archPath); err == nil {
		if _, err := os.Stat(absArch); err == nil {
			archExists = true
		}
	}
	if absQs, err := security.SafeResolve(repoRoot, qsPath); err == nil {
		if _, err := os.Stat(absQs); err == nil {
			qsExists = true
		}
	}

	// Regenerate global files if the root summary was affected or either of the global files is missing
	if affectedDirs["."] || !archExists || !qsExists {
		logEvent("status", "Step 3: Global Architecture Synthesis...")
		archMsg := fmt.Sprintf("Write the global architecture overview (%s/architecture.md) based on the root summary.\n\n%s", docsDir, rootSum)
		archDoc, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("architecture"), []Message{{Role: "user", Content: archMsg}}, false)
		if err != nil {
			return fmt.Errorf("failed to generate global architecture: %w", err)
		}
		if err := tools.WriteFileSafely(repoRoot, archPath, []byte(stripOuterMarkdownFence(archDoc))); err != nil {
			return fmt.Errorf("failed to write architecture.md: %w", err)
		}

		logEvent("status", "Step 4: Generating Quickstart...")
		qsMsg := fmt.Sprintf("Write the %s/quickstart.md page based on this architecture.\n\n%s", docsDir, rootSum)
		qsDoc, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("architecture"), []Message{{Role: "user", Content: qsMsg}}, false)
		if err != nil {
			return fmt.Errorf("failed to generate quickstart documentation: %w", err)
		}
		if err := tools.WriteFileSafely(repoRoot, qsPath, []byte(stripOuterMarkdownFence(qsDoc))); err != nil {
			return fmt.Errorf("failed to write quickstart.md: %w", err)
		}
	} else {
		logEvent("status", "Global architecture and quickstart pages are up to date. Skipping regeneration.")
	}

	// 6. Save metadata cache
	cache.LastDocumentedCommit = "local"
	if err := saveMetadataCache(repoRoot, docsDir, cache); err != nil {
		logEvent("status", fmt.Sprintf("Warning: failed to save metadata cache: %v", err))
	}

	logEvent("status", "Pipeline update completed successfully!")
	return nil
}
