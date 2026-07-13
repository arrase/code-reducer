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
)

// EventType defines the type of pipeline events.
type EventType string

const (
	// EventStatus represents progress/status updates.
	EventStatus EventType = "status"
	// EventError represents execution errors.
	EventError  EventType = "error"
)

// Event represents a progress or error notification from the pipeline.
type Event struct {
	Type    EventType
	Message string
}

type orchestrator struct {
	client llmCaller
}

func (o *orchestrator) GenerateStandardDocs(ctx context.Context, repoRoot, docsDir, rootSum string, cfg *config.Config, logEvent LogEventFunc) error {
	logEvent(EventStatus, "Step 3: Global Architecture Synthesis...")
	archPath := filepath.Join(docsDir, "architecture.md")
	archMsg := fmt.Sprintf("Write the global architecture overview (%s/architecture.md) based on the root summary.\n\n%s", docsDir, rootSum)
	sysPrompt := cfg.SystemPrompt + "\n" + cfg.ArchitecturePrompt
	archDoc, err := o.client.CallLLM(ctx, sysPrompt, []Message{{Role: "user", Content: archMsg}}, false)
	if err != nil {
		return fmt.Errorf("failed to generate global architecture: %w", err)
	}
	if err := tools.WriteFileSafely(repoRoot, archPath, []byte(stripOuterMarkdownFence(archDoc))); err != nil {
		return fmt.Errorf("failed to write architecture.md: %w", err)
	}

	logEvent(EventStatus, "Step 4: Generating Quickstart...")
	qsPath := filepath.Join(docsDir, "quickstart.md")
	qsMsg := fmt.Sprintf("Write the %s/quickstart.md page based on this architecture.\n\n%s", docsDir, rootSum)
	qsDoc, err := o.client.CallLLM(ctx, sysPrompt, []Message{{Role: "user", Content: qsMsg}}, false)
	if err != nil {
		return fmt.Errorf("failed to generate quickstart documentation: %w", err)
	}
	if err := tools.WriteFileSafely(repoRoot, qsPath, []byte(stripOuterMarkdownFence(qsDoc))); err != nil {
		return fmt.Errorf("failed to write quickstart.md: %w", err)
	}

	return nil
}

func setupPipeline(repoRoot string, cfg *config.Config, logEvent LogEventFunc) (string, []string, *MetadataCache) {
	docsDir := cfg.DocsDir
	gitignorePatterns, err := tools.LoadGitignore(repoRoot)
	if err != nil {
		logEvent(EventStatus, fmt.Sprintf("Warning: failed to load .gitignore: %v", err))
	}
	ignores := append(cfg.Ignore, docsDir)
	ignores = append(ignores, gitignorePatterns...)

	cache, err := loadMetadataCache(repoRoot, docsDir)
	if err != nil {
		logEvent(EventStatus, fmt.Sprintf("Warning: failed to load metadata cache: %v", err))
	}
	return docsDir, ignores, cache
}

func teardownPipeline(repoRoot, docsDir string, cache *MetadataCache, logEvent LogEventFunc, successMsg string) {
	if err := saveMetadataCache(repoRoot, docsDir, cache); err != nil {
		logEvent(EventStatus, fmt.Sprintf("Warning: failed to save metadata cache: %v", err))
	}
	logEvent(EventStatus, successMsg)
}

func (o *orchestrator) RunInit(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error {
	logEvent := makeLogEvent(onEvent)
	logEvent(EventStatus, "Starting Map-Reduce pipeline: init")
	logEvent(EventStatus, "Step 1: Code Discovery & Building Tree...")
	
	docsDir, ignores, cache := setupPipeline(repoRoot, cfg, logEvent)
	cache.StepsHash = computeStepsHash(cfg.ExtractionSteps)

	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		return err
	}

	precalculatedHashes := make(map[string]string)
	for _, f := range codeFiles {
		hash, err := computeSHA256(repoRoot, f)
		if err == nil {
			precalculatedHashes[f] = hash
		} else {
			logEvent(EventStatus, fmt.Sprintf("Warning: failed to compute hash for %s: %v", f, err))
		}
	}

	tree := buildTree(codeFiles)
	modulesDir := filepath.Join(repoRoot, docsDir, "modules")
	if err := os.MkdirAll(modulesDir, defaultDirPerm); err != nil {
		return fmt.Errorf("failed to create modules directory: %w", err)
	}

	logEvent(EventStatus, "Step 2: Hierarchical Tree-Merging (Map-Reduce)...")

	affectedDirs := make(map[string]bool)
	var markAllAffected func(n *DirNode)
	markAllAffected = func(n *DirNode) {
		affectedDirs[n.Path] = true
		for _, child := range n.Children {
			markAllAffected(child)
		}
	}
	markAllAffected(tree)

	pCtx := &pipelineContext{
		ctx:                 ctx,
		client:              o.client,
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               cache,
		affectedDirs:        affectedDirs,
		precalculatedHashes: precalculatedHashes,
		logEvent:            logEvent,
	}

	rootSum, err := synthesizeNode(pCtx, tree)
	if err != nil {
		return err
	}

	if err := o.GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum, cfg, logEvent); err != nil {
		return err
	}

	logEvent(EventStatus, fmt.Sprintf("Step 5: Updating %s...", agentsFileName))
	agentFilePath := agentsFileName
	agentGuidelines := fmt.Sprintf(`# AI Agent Guidelines

This repository contains automatically generated documentation under the %s directory to help AI coding agents understand the system architecture, design patterns, and module structure:

- **System Blueprint**: Refer to %s/architecture.md for a high-level system overview, module relationships, and boundary definitions.
- **Developer Quickstart**: Refer to %s/quickstart.md for onboarding steps, coding patterns, and configuration settings.
- **Module Details**: Explore %s/modules/ for directory-level summaries and API descriptions of internal packages.

Before making changes, analyze these files to align with existing design choices and code structures.
`, docsDir, docsDir, docsDir, docsDir)

	agentFileBytes, err := tools.ReadFileSafely(repoRoot, agentFilePath)
	if err != nil {
		if err := tools.WriteFileSafely(repoRoot, agentFilePath, []byte(agentGuidelines)); err != nil {
			return fmt.Errorf("failed to write %s: %w", agentsFileName, err)
		}
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
			if err := tools.WriteFileSafely(repoRoot, agentFilePath, []byte(newContent)); err != nil {
				return fmt.Errorf("failed to append to %s: %w", agentsFileName, err)
			}
		}
	}

	teardownPipeline(repoRoot, docsDir, cache, logEvent, "Pipeline completed successfully!")
	return nil
}

func (o *orchestrator) RunUpdate(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error {
	logEvent := makeLogEvent(onEvent)
	logEvent(EventStatus, "Starting Map-Reduce pipeline: update")

	docsDir, ignores, cache := setupPipeline(repoRoot, cfg, logEvent)

	currentStepsHash := computeStepsHash(cfg.ExtractionSteps)
	if cache.StepsHash != currentStepsHash {
		logEvent(EventStatus, "Warning: Extraction steps changed. Invalidating metadata cache to force full documentation regeneration.")
		cache.Files = make(map[string]FileCacheEntry)
		cache.Modules = make(map[string]string)
		cache.StepsHash = currentStepsHash
	}

	logEvent(EventStatus, "Step 1: Detecting changed files...")

	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		return err
	}

	var filteredChanges []FileChange
	currentFilesMap := make(map[string]string)
	var allowedCodeFiles []string

	for _, f := range codeFiles {
		hash, err := computeSHA256(repoRoot, f)
		if err != nil {
			logEvent(EventStatus, fmt.Sprintf("Warning: failed to compute hash for %s: %v", f, err))
			continue
		}
		currentFilesMap[f] = hash
		allowedCodeFiles = append(allowedCodeFiles, f)
	}

	for _, f := range allowedCodeFiles {
		currentHash := currentFilesMap[f]
		cachedEntry, exists := cache.Files[f]
		if !exists {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: StatusAdded})
		} else if cachedEntry.SHA256 != currentHash {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: StatusModified})
		}
	}

	var cachedFiles []string
	for f := range cache.Files {
		cachedFiles = append(cachedFiles, f)
	}
	sort.Strings(cachedFiles)
	for _, f := range cachedFiles {
		if _, exists := currentFilesMap[f]; !exists {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: StatusDeleted})
		}
	}

	affectedDirs := make(map[string]bool)

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
			moduleFile := filepath.Join(docsDir, "modules", toSafeMarkdownFilename(mPath))
			if absModuleFile, err := security.SafeResolve(repoRoot, moduleFile); err == nil {
				_ = os.Remove(absModuleFile)
			}
		}
	}

	determineAffected(tree, repoRoot, docsDir, cache, filteredChanges, affectedDirs)
	propagateAffected(tree, affectedDirs)

	if len(affectedDirs) == 0 {
		logEvent(EventStatus, "No modifications detected. Documentation is up to date.")
		return nil
	}

	logEvent(EventStatus, fmt.Sprintf("Step 2: Hierarchical Tree-Merging (Map-Reduce)... (Affected modules: %d)", len(affectedDirs)))
	pCtx := &pipelineContext{
		ctx:                 ctx,
		client:              o.client,
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               cache,
		affectedDirs:        affectedDirs,
		precalculatedHashes: currentFilesMap,
		logEvent:            logEvent,
	}

	rootSum, err := synthesizeNode(pCtx, tree)
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
		if err := o.GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum, cfg, logEvent); err != nil {
			return err
		}
	} else {
		logEvent(EventStatus, "Global architecture and quickstart pages are up to date. Skipping regeneration.")
	}

	teardownPipeline(repoRoot, docsDir, cache, logEvent, "Pipeline update completed successfully!")
	return nil
}
