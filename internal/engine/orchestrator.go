package engine

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/security"
	"github.com/arrase/code-reducer/internal/tools"
	ignore "github.com/sabhiram/go-gitignore"
)

type Event struct {
	Type    string
	Message string
}

func (c *LLMClient) GenerateStandardDocs(ctx context.Context, repoRoot, docsDir, rootSum string, logEvent func(string, string)) error {
	logEvent("status", "Step 3: Global Architecture Synthesis...")
	archPath := filepath.Join(docsDir, "architecture.md")
	archMsg := fmt.Sprintf("Write the global architecture overview (%s/architecture.md) based on the root summary.\n\n%s", docsDir, rootSum)
	archDoc, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("architecture"), []Message{{Role: "user", Content: archMsg}}, false)
	if err != nil {
		return fmt.Errorf("failed to generate global architecture: %w", err)
	}
	if err := tools.WriteFileSafely(repoRoot, archPath, []byte(StripOuterMarkdownFence(archDoc))); err != nil {
		return fmt.Errorf("failed to write architecture.md: %w", err)
	}

	logEvent("status", "Step 4: Generating Quickstart...")
	qsPath := filepath.Join(docsDir, "quickstart.md")
	qsMsg := fmt.Sprintf("Write the %s/quickstart.md page based on this architecture.\n\n%s", docsDir, rootSum)
	qsDoc, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("architecture"), []Message{{Role: "user", Content: qsMsg}}, false)
	if err != nil {
		return fmt.Errorf("failed to generate quickstart documentation: %w", err)
	}
	if err := tools.WriteFileSafely(repoRoot, qsPath, []byte(StripOuterMarkdownFence(qsDoc))); err != nil {
		return fmt.Errorf("failed to write quickstart.md: %w", err)
	}

	return nil
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
	cache, err := loadMetadataCache(repoRoot, docsDir)
	if err != nil || cache == nil {
		logEvent("status", fmt.Sprintf("Warning: failed to load metadata cache: %v", err))
		cache = &MetadataCache{
			Files:   make(map[string]FileCacheEntry),
			Modules: make(map[string]string),
		}
	}

	affectedDirs := make(map[string]bool)
	var markAllAffected func(n *DirNode)
	markAllAffected = func(n *DirNode) {
		affectedDirs[n.Path] = true
		for _, child := range n.Children {
			markAllAffected(child)
		}
	}
	markAllAffected(tree)

	rootSum, err := synthesizeNode(ctx, c, tree, repoRoot, cfg, cache, affectedDirs, func(m, t string) { logEvent(m, t) })
	if err != nil {
		return err
	}

	if err := c.GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum, func(m, t string) { logEvent(m, t) }); err != nil {
		return err
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

	cache, err := loadMetadataCache(repoRoot, docsDir)
	if err != nil || cache == nil {
		logEvent("status", fmt.Sprintf("Warning: failed to load metadata cache: %v", err))
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

	gitIgnore := ignore.CompileIgnoreLines(ignores...)

	for _, f := range codeFiles {
		if isAllowedFile(repoRoot, f, gitIgnore, cfg.IgnoreExtensions) {
			allowedCodeFiles = append(allowedCodeFiles, f)
			hash, err := computeSHA256(repoRoot, f)
			if err == nil {
				currentFilesMap[f] = hash
			}
		}
	}

	for _, f := range allowedCodeFiles {
		currentHash := currentFilesMap[f]
		cachedEntry, exists := cache.Files[f]
		if !exists {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: "Added"})
		} else if cachedEntry.SHA256 != currentHash {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: "Modified"})
		}
	}

	var cachedFiles []string
	for f := range cache.Files {
		cachedFiles = append(cachedFiles, f)
	}
	sort.Strings(cachedFiles)
	for _, f := range cachedFiles {
		if _, exists := currentFilesMap[f]; !exists {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: "Deleted"})
		}
	}

	affectedDirs := make(map[string]bool)
	for _, change := range filteredChanges {
		curr := filepath.Dir(change.Path)
		for {
			affectedDirs[curr] = true
			if curr == "." || curr == "" {
				break
			}
			curr = filepath.Dir(curr)
		}
		if change.Status == "Deleted" {
			delete(cache.Files, change.Path)
		}
	}

	tree := buildTree(allowedCodeFiles)

	codeFileMap := make(map[string]bool)
	for _, f := range allowedCodeFiles {
		codeFileMap[f] = true
	}
	for f := range cache.Files {
		if !codeFileMap[f] {
			delete(cache.Files, f)
		}
	}

	activeDirs := make(map[string]bool)
	var collectDirs func(n *DirNode)
	collectDirs = func(n *DirNode) {
		activeDirs[n.Path] = true
		for _, child := range n.Children {
			collectDirs(child)
		}
	}
	collectDirs(tree)

	for mPath := range cache.Modules {
		if !activeDirs[mPath] {
			delete(cache.Modules, mPath)
			moduleFile := filepath.Join(docsDir, "modules", ToSafeMarkdownFilename(mPath))
			if absModuleFile, err := security.SafeResolve(repoRoot, moduleFile); err == nil {
				_ = os.Remove(absModuleFile)
			}
		}
	}

	determineAffected(tree, repoRoot, docsDir, cache, filteredChanges, affectedDirs)
	propagateAffected(tree, affectedDirs)

	if len(affectedDirs) == 0 {
		logEvent("status", "No modifications detected. Documentation is up to date.")
		return nil
	}

	logEvent("status", fmt.Sprintf("Step 2: Hierarchical Tree-Merging (Map-Reduce)... (Affected modules: %d)", len(affectedDirs)))
	rootSum, err := synthesizeNode(ctx, c, tree, repoRoot, cfg, cache, affectedDirs, func(m, t string) { logEvent(m, t) })
	if err != nil {
		return err
	}

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

	if affectedDirs["."] || !archExists || !qsExists {
		if err := c.GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum, func(m, t string) { logEvent(m, t) }); err != nil {
			return err
		}
	} else {
		logEvent("status", "Global architecture and quickstart pages are up to date. Skipping regeneration.")
	}

	cache.LastDocumentedCommit = "local"
	if err := saveMetadataCache(repoRoot, docsDir, cache); err != nil {
		logEvent("status", fmt.Sprintf("Warning: failed to save metadata cache: %v", err))
	}

	logEvent("status", "Pipeline update completed successfully!")
	return nil
}
