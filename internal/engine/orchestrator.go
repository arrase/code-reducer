package engine

import (
	"context"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/security"
	"github.com/arrase/code-reducer/internal/tools"
)

const (
	agentsFileName   = "AGENTS.md"
	agentsBlockStart = "<!-- CODE-REDUCER:START -->"
	agentsBlockEnd   = "<!-- CODE-REDUCER:END -->"
)

// RunMode represents the operation mode of the documentation pipeline.
type RunMode string

const (
	// ModeInit initializes the project documentation.
	ModeInit RunMode = "init"
	// ModeUpdate updates the existing project documentation incrementally.
	ModeUpdate RunMode = "update"
)

// StatusFunc is a callback function for reporting pipeline progress and warnings.
type StatusFunc func(string)

// Run executes the documentation pipeline for the specified mode.
func Run(ctx context.Context, repoRoot string, cfg *config.Config, mode RunMode, onStatus StatusFunc) error {
	if onStatus == nil {
		onStatus = func(string) {
			// No-op when caller does not provide a status callback.
		}
	}

	if err := tools.EnsureGitignoreHasLockfile(repoRoot); err != nil {
		onStatus(fmt.Sprintf("Warning: failed to ensure gitignore has lockfile: %v", err))
	}

	lock, err := security.AcquireLock(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to acquire repository lock: %w", err)
	}
	defer func() { _ = lock.Unlock() }()

	client := newLLMClient(cfg.ModelID, cfg.OllamaBaseURL, cfg.OllamaNumCtx, onStatus)
	orch := &orchestrator{client: client}

	switch mode {
	case ModeInit:
		return orch.RunInit(ctx, repoRoot, cfg, onStatus)
	case ModeUpdate:
		return orch.RunUpdate(ctx, repoRoot, cfg, onStatus)
	default:
		return fmt.Errorf("unsupported mode: %s", mode)
	}
}

type orchestrator struct {
	client llmCaller
}

func (o *orchestrator) generateDoc(ctx context.Context, repoRoot, relPath, sysPrompt, promptHeader string, contextItems []string, onStatus StatusFunc) error {
	redCfg := reductionConfig{
		sysPrompt: sysPrompt,
		buildPrompt: func(batch []string) string {
			return fmt.Sprintf("%s\n\n%s", promptHeader, strings.Join(batch, "\n\n"))
		},
		logMsg: func(batch []string) string {
			return fmt.Sprintf("➜ Generating %s (%d items)...", relPath, len(batch))
		},
		errMsg:   fmt.Sprintf("failed to generate %s", relPath),
		onStatus: onStatus,
	}
	doc, err := reduceWithLLM(ctx, o.client, contextItems, redCfg)
	if err != nil {
		return err
	}
	if err := tools.WriteFileSafely(repoRoot, relPath, []byte(doc)); err != nil {
		return fmt.Errorf("failed to write %s: %w", relPath, err)
	}
	return nil
}

func buildProjectContextItems(rootSum string, tree *DirNode, cache *MetadataCache) []string {
	nodesByPath := collectTreeNodes(tree)
	var allDirs []string
	for d := range nodesByPath {
		allDirs = append(allDirs, d)
	}
	sort.Strings(allDirs)

	var mapBuilder strings.Builder
	mapBuilder.WriteString("## Project Directories & Module Documentation Map\n")
	mapBuilder.WriteString(strings.Join(allDirs, ", ") + "\n")
	for _, dirPath := range allDirs {
		if cache.Modules[dirPath] == "" {
			continue
		}
		node := nodesByPath[dirPath]
		docPath := filepath.ToSlash(filepath.Join("modules", toSafeMarkdownFilename(dirPath)))
		files := make([]string, len(node.Files))
		copy(files, node.Files)
		sort.Strings(files)
		if len(files) > 0 {
			_, _ = fmt.Fprintf(&mapBuilder, "- `%s` -> `%s` (files: %s)\n", dirPath, docPath, strings.Join(files, ", "))
		} else {
			_, _ = fmt.Fprintf(&mapBuilder, "- `%s` -> `%s`\n", dirPath, docPath)
		}
	}

	items := []string{
		strings.TrimSpace(mapBuilder.String()),
		fmt.Sprintf("## Root Module Summary\n%s", rootSum),
	}

	var childNames []string
	for name := range tree.Children {
		childNames = append(childNames, name)
	}
	sort.Strings(childNames)

	for _, name := range childNames {
		childPath := tree.Children[name].Path
		if sum := cache.Modules[childPath]; sum != "" {
			items = append(items, fmt.Sprintf("### Top-Level Submodule: %s\n%s", childPath, sum))
		}
	}
	return items
}

func (o *orchestrator) GenerateStandardDocs(ctx context.Context, pState *pipelineState, rootSum string, tree *DirNode) error {
	contextItems := buildProjectContextItems(rootSum, tree, pState.cache)
	langInstr := config.LanguageInstruction(pState.cfg.Language)
	docsDir := pState.cfg.DocsDir

	pState.onStatus("Step 3: Global Architecture Synthesis...")
	archPath := filepath.Join(docsDir, "architecture.md")
	archHeader := fmt.Sprintf("Write the global architecture overview (%s) based on the project context.", archPath)
	if err := o.generateDoc(ctx, pState.repoRoot, archPath, pState.cfg.SystemPrompt+"\n"+pState.cfg.ArchitecturePrompt+langInstr, archHeader, contextItems, pState.onStatus); err != nil {
		return err
	}

	pState.onStatus("Step 4: Generating Quickstart...")
	qsPath := filepath.Join(docsDir, "quickstart.md")
	qsHeader := fmt.Sprintf("Write the %s page based on the project context.", qsPath)
	return o.generateDoc(ctx, pState.repoRoot, qsPath, pState.cfg.SystemPrompt+"\n"+pState.cfg.QuickstartPrompt+langInstr, qsHeader, contextItems, pState.onStatus)
}

func setupPipeline(repoRoot string, cfg *config.Config) (string, []string, *MetadataCache, error) {
	docsDir := cfg.DocsDir
	gitignorePatterns, err := tools.LoadGitignore(repoRoot)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to load .gitignore: %w", err)
	}
	ignores := make([]string, 0, len(cfg.Ignore)+1+len(gitignorePatterns))
	ignores = append(ignores, cfg.Ignore...)
	ignores = append(ignores, docsDir)
	ignores = append(ignores, gitignorePatterns...)

	cache, err := loadMetadataCache(repoRoot, docsDir)
	if err != nil {
		return "", nil, nil, fmt.Errorf("failed to load metadata cache: %w", err)
	}
	return docsDir, ignores, cache, nil
}

func teardownPipeline(repoRoot, docsDir string, cache *MetadataCache, onStatus StatusFunc, successMsg string) {
	if err := saveMetadataCache(repoRoot, docsDir, cache); err != nil {
		onStatus(fmt.Sprintf("Warning: failed to save metadata cache: %v", err))
	}
	onStatus(successMsg)
}

func hashCodeFiles(repoRoot string, codeFiles []string, onStatus StatusFunc) ([]string, map[string]string) {
	var validFiles []string
	currentHashes := make(map[string]string, len(codeFiles))
	for _, f := range codeFiles {
		hash, err := computeSHA256(repoRoot, f)
		if err != nil {
			onStatus(fmt.Sprintf("Warning: failed to compute hash for %s: %v", f, err))
			continue
		}
		validFiles = append(validFiles, f)
		currentHashes[f] = hash
	}
	return validFiles, currentHashes
}

func updateAgentGuidelines(repoRoot, docsDir string) error {
	block := fmt.Sprintf(`%s
# AI Agent Guidelines

This repository contains automatically generated documentation under the %s directory to help AI coding agents understand the system architecture, design patterns, and module structure:

- **System Blueprint**: Refer to %s/architecture.md for a high-level system overview, module relationships, and boundary definitions.
- **Developer Quickstart & Task Routing**: Refer to %s/quickstart.md for onboarding steps, task routing table, coding patterns, and configuration settings.
- **Documentation Index**: Refer to %s/index.md for a navigable catalog of all system and module pages.
- **Module Details**: Explore %s/modules/ for directory-level summaries and API descriptions of internal packages.
- **CLI Search & Read**: Run `+"`"+`code-reducer search "<query>"`+"`"+` and `+"`"+`code-reducer read "<page#section>"`+"`"+` to query the documentation directly.
%s`, agentsBlockStart, docsDir, docsDir, docsDir, docsDir, docsDir, agentsBlockEnd)

	agentFileBytes, err := tools.ReadFileSafely(repoRoot, agentsFileName)
	if err != nil {
		if err := tools.WriteFileSafely(repoRoot, agentsFileName, []byte(block+"\n")); err != nil {
			return fmt.Errorf("failed to write %s: %w", agentsFileName, err)
		}
		return nil
	}

	content := string(agentFileBytes)
	startIdx := strings.Index(content, agentsBlockStart)
	endIdx := strings.Index(content, agentsBlockEnd)
	if startIdx != -1 && endIdx != -1 && endIdx > startIdx {
		endIdx += len(agentsBlockEnd)
		newContent := content[:startIdx] + block + content[endIdx:]
		if newContent == content {
			return nil
		}
		if err := tools.WriteFileSafely(repoRoot, agentsFileName, []byte(newContent)); err != nil {
			return fmt.Errorf("failed to update %s: %w", agentsFileName, err)
		}
		return nil
	}

	if strings.Contains(content, "AI Agent Guidelines") {
		return nil
	}

	separator := "\n\n"
	if strings.HasSuffix(content, "\n\n") {
		separator = ""
	} else if strings.HasSuffix(content, "\n") {
		separator = "\n"
	}
	newContent := content + separator + block + "\n"
	if err := tools.WriteFileSafely(repoRoot, agentsFileName, []byte(newContent)); err != nil {
		return fmt.Errorf("failed to append to %s: %w", agentsFileName, err)
	}
	return nil
}

func (o *orchestrator) RunInit(ctx context.Context, repoRoot string, cfg *config.Config, onStatus StatusFunc) error {
	onStatus("Starting Map-Reduce pipeline: init")
	onStatus("Step 1: Code Discovery & Building Tree...")

	docsDir, ignores, cache, err := setupPipeline(repoRoot, cfg)
	if err != nil {
		return err
	}
	cache.StepsHash = computeStepsHash(cfg)

	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		return err
	}

	validFiles, precalculatedHashes := hashCodeFiles(repoRoot, codeFiles, onStatus)
	tree := buildTree(validFiles)
	pruneStaleCache(repoRoot, docsDir, cache, tree, precalculatedHashes)

	onStatus("Step 2: Hierarchical Tree-Merging (Map-Reduce)...")
	affectedDirs := collectTreeDirs(tree)

	pState := &pipelineState{
		client:              o.client,
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               cache,
		affectedDirs:        affectedDirs,
		precalculatedHashes: precalculatedHashes,
		onStatus:            onStatus,
	}

	rootSum, err := synthesizeNode(ctx, pState, tree)
	if err != nil {
		return err
	}

	if err := o.GenerateStandardDocs(ctx, pState, rootSum, tree); err != nil {
		return err
	}

	if err := finalizeWiki(repoRoot, docsDir, tree, cache, onStatus); err != nil {
		return err
	}

	onStatus(fmt.Sprintf("Step 5: Updating %s...", agentsFileName))
	if err := updateAgentGuidelines(repoRoot, docsDir); err != nil {
		return err
	}

	teardownPipeline(repoRoot, docsDir, cache, onStatus, "Pipeline completed successfully!")
	return nil
}

func pruneStaleCache(repoRoot string, docsDir string, cache *MetadataCache, tree *DirNode, currentFiles map[string]string) {
	for f := range cache.Files {
		if _, exists := currentFiles[f]; !exists {
			delete(cache.Files, f)
		}
	}
	activeDirs := collectTreeDirs(tree)
	for mPath := range cache.Modules {
		if !activeDirs[mPath] {
			delete(cache.Modules, mPath)
		}
	}

	modulesRoot, err := security.SafeResolve(repoRoot, filepath.Join(docsDir, "modules"))
	if err != nil {
		return
	}
	activeDocs := make(map[string]bool, len(activeDirs))
	for mPath := range activeDirs {
		activeDocs[filepath.Join(modulesRoot, toSafeMarkdownFilename(mPath))] = true
	}

	pruneStaleModuleDocs(modulesRoot, activeDocs)
}

func pruneStaleModuleDocs(modulesRoot string, activeDocs map[string]bool) {
	var subdirs []string
	_ = filepath.WalkDir(modulesRoot, func(p string, d os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return nil
		}
		if d.IsDir() {
			if p != modulesRoot {
				subdirs = append(subdirs, p)
			}
			return nil
		}
		if !activeDocs[p] {
			_ = os.Remove(p)
		}
		return nil
	})
	for i := len(subdirs) - 1; i >= 0; i-- {
		_ = os.Remove(subdirs[i])
	}
}

func detectFileChanges(repoRoot string, ignores []string, cache *MetadataCache, onStatus StatusFunc) ([]FileChange, map[string]string, []string, error) {
	codeFiles, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		return nil, nil, nil, err
	}

	validFiles, currentHashes := hashCodeFiles(repoRoot, codeFiles, onStatus)

	var filteredChanges []FileChange
	for _, f := range validFiles {
		currentHash := currentHashes[f]
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
		if _, exists := currentHashes[f]; !exists {
			filteredChanges = append(filteredChanges, FileChange{Path: f, Status: StatusDeleted})
		}
	}

	return filteredChanges, currentHashes, validFiles, nil
}

func hasModifiedFileFactsChanged(ctx context.Context, pState *pipelineState, nodesByPath map[string]*DirNode, filePath string) (bool, error) {
	prevEntry, ok := pState.cache.Files[filePath]
	if !ok {
		return true, nil
	}
	var siblingFiles []string
	if dirNode := nodesByPath[path.Dir(filePath)]; dirNode != nil {
		siblingFiles = dirNode.Files
	}
	newFacts, err := extractFileFacts(ctx, pState, filePath, siblingFiles)
	if err != nil {
		return false, err
	}
	if newFacts == prevEntry.Facts {
		pState.onStatus(fmt.Sprintf("➜ Unchanged facts for modified file, skipping module re-synthesis: %s", filePath))
		return false, nil
	}
	return true, nil
}

func resolveEffectiveChanges(ctx context.Context, pState *pipelineState, tree *DirNode, changes []FileChange) ([]FileChange, bool, error) {
	nodesByPath := collectTreeNodes(tree)
	var effective []FileChange
	structureChanged := false

	for _, change := range changes {
		if change.Status == StatusAdded || change.Status == StatusDeleted {
			structureChanged = true
		}
		if change.Status == StatusModified {
			changed, err := hasModifiedFileFactsChanged(ctx, pState, nodesByPath, change.Path)
			if err != nil {
				return nil, false, err
			}
			if !changed {
				continue
			}
		}
		effective = append(effective, change)
	}
	return effective, structureChanged, nil
}

func (o *orchestrator) RunUpdate(ctx context.Context, repoRoot string, cfg *config.Config, onStatus StatusFunc) error {
	onStatus("Starting Map-Reduce pipeline: update")

	docsDir, ignores, cache, err := setupPipeline(repoRoot, cfg)
	if err != nil {
		return err
	}

	currentStepsHash := computeStepsHash(cfg)
	if cache.StepsHash != currentStepsHash {
		onStatus("Warning: Extraction steps changed. Invalidating metadata cache to force full documentation regeneration.")
		cache.Files = make(map[string]FileCacheEntry)
		cache.Modules = make(map[string]string)
		cache.StepsHash = currentStepsHash
	}

	onStatus("Step 1: Detecting changed files...")

	filteredChanges, currentFilesMap, allowedCodeFiles, err := detectFileChanges(repoRoot, ignores, cache, onStatus)
	if err != nil {
		return err
	}

	tree := buildTree(allowedCodeFiles)
	pruneStaleCache(repoRoot, docsDir, cache, tree, currentFilesMap)

	pState := &pipelineState{
		client:              o.client,
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               cache,
		precalculatedHashes: currentFilesMap,
		onStatus:            onStatus,
	}

	effectiveChanges, structureChanged, err := resolveEffectiveChanges(ctx, pState, tree, filteredChanges)
	if err != nil {
		return err
	}

	affectedDirs := propagateAffected(tree, determineAffected(tree, repoRoot, docsDir, cache, effectiveChanges))
	pState.affectedDirs = affectedDirs

	archPath := filepath.Join(docsDir, "architecture.md")
	qsPath := filepath.Join(docsDir, "quickstart.md")
	indexPath := filepath.Join(docsDir, "index.md")
	missingGlobalDocs := !tools.FileExistsSafely(repoRoot, archPath) ||
		!tools.FileExistsSafely(repoRoot, qsPath) ||
		!tools.FileExistsSafely(repoRoot, indexPath)

	if len(affectedDirs) == 0 && !missingGlobalDocs {
		teardownPipeline(repoRoot, docsDir, cache, onStatus, "No modifications detected. Documentation is up to date.")
		return nil
	}

	prevRootSum := cache.Modules["."]
	prevTopModules := make(map[string]string, len(tree.Children))
	for name, child := range tree.Children {
		prevTopModules[name] = cache.Modules[child.Path]
	}

	onStatus(fmt.Sprintf("Step 2: Hierarchical Tree-Merging (Map-Reduce)... (Affected modules: %d)", len(affectedDirs)))
	rootSum, err := synthesizeNode(ctx, pState, tree)
	if err != nil {
		return err
	}

	topModulesChanged := false
	for name, child := range tree.Children {
		if cache.Modules[child.Path] != prevTopModules[name] {
			topModulesChanged = true
			break
		}
	}

	if missingGlobalDocs || structureChanged || rootSum != prevRootSum || topModulesChanged {
		if err := o.GenerateStandardDocs(ctx, pState, rootSum, tree); err != nil {
			return err
		}
	} else {
		onStatus("➜ Skipping global docs regeneration (top-level summaries unchanged)")
	}

	if err := finalizeWiki(repoRoot, docsDir, tree, cache, onStatus); err != nil {
		return err
	}

	teardownPipeline(repoRoot, docsDir, cache, onStatus, "Pipeline update completed successfully!")
	return nil
}
