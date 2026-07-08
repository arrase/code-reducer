package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/security"
	"github.com/arrase/code-reducer/internal/tools"
)

type Event struct {
	Type    string
	Message string
}

func reduceInChunks(ctx context.Context, c *LLMClient, nodePath string, items []string, logEvent func(string, string)) (string, error) {
	if len(items) == 0 {
		return "", nil
	}

	maxChunkChars := c.NumCtx * 3
	if maxChunkChars <= 0 {
		maxChunkChars = 24576 // 8192 * 3
	}

	var batches [][]string
	var currentBatch []string
	currentLen := 0

	for _, item := range items {
		if currentLen+len(item) > maxChunkChars && len(currentBatch) > 0 {
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

	// Prevent infinite loop if items cannot be batched together
	if len(batches) == len(items) && len(items) > 1 {
		logEvent("status", "➜ Warning: Items too large to batch, truncating to fit context window")
		allowedPerItem := maxChunkChars / len(items)
		var truncatedItems []string
		for _, item := range items {
			if len(item) > allowedPerItem {
				truncatedItems = append(truncatedItems, item[:allowedPerItem]+"\n...[truncated]")
			} else {
				truncatedItems = append(truncatedItems, item)
			}
		}
		batches = [][]string{truncatedItems}
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
	gitignorePatterns, err := tools.LoadGitignore(repoRoot)
	if err != nil {
		logEvent("status", fmt.Sprintf("Warning: failed to load .gitignore: %v", err))
	}
	ignores := append(cfg.Ignore, docsDir)
	ignores = append(ignores, gitignorePatterns...)

	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores, cfg.IgnoreExtensions)
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
	gitignorePatterns, err := tools.LoadGitignore(repoRoot)
	if err != nil {
		logEvent("status", fmt.Sprintf("Warning: failed to load .gitignore: %v", err))
	}
	ignores := append(cfg.Ignore, docsDir)
	ignores = append(ignores, gitignorePatterns...)

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

	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores, cfg.IgnoreExtensions)
	if err != nil {
		return err
	}

	var filteredChanges []FileChange
	currentFilesMap := make(map[string]string)
	var allowedCodeFiles []string

	for _, f := range codeFiles {
		if isAllowedFile(repoRoot, f, ignores, cfg.IgnoreExtensions) {
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
