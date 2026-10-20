package engine

import (
	"context"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"unicode/utf8"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/tools"
)

// toSafeMarkdownFilename converts a module path to a safe filename for markdown docs.
func toSafeMarkdownFilename(modulePath string) string {
	if modulePath == "." || modulePath == "" {
		return "README.md"
	}
	return filepath.Join(modulePath, "README.md")
}

type pipelineState struct {
	client              llmCaller
	repoRoot            string
	cfg                 *config.Config
	cache               *MetadataCache
	affectedDirs        map[string]bool
	precalculatedHashes map[string]string
	onStatus            StatusFunc
}

func buildExtractionPrompt(filePath, siblingList, chunk string, step config.ExtractionStep) string {
	return fmt.Sprintf("Target File: %s\nDirectory Files: %s\n---\n%s\n---\nTask (%s): %s\nReminder: Be concise, extract only verifiable facts from the code above, do not copy code blocks, and output 'None' if not applicable.",
		filePath, siblingList, chunk, step.Name, step.Prompt)
}

func maxExtractionOverhead(sysPrompt, filePath, siblingList string, steps []config.ExtractionStep) string {
	maxWrapper := ""
	maxRunes := 0
	for _, step := range steps {
		wrapper := buildExtractionPrompt(filePath, siblingList, "", step)
		if runes := utf8.RuneCountInString(wrapper); runes > maxRunes {
			maxRunes = runes
			maxWrapper = wrapper
		}
	}
	return sysPrompt + maxWrapper
}

func extractFileFacts(ctx context.Context, p *pipelineState, f string, siblingFiles []string) (string, error) {
	fileHash := p.precalculatedHashes[f]
	if cachedEntry, cacheExists := p.cache.Files[f]; cacheExists && cachedEntry.SHA256 == fileHash {
		return cachedEntry.Facts, nil
	}

	contentBytes, err := tools.ReadFileSafely(p.repoRoot, f)
	if err != nil {
		p.onStatus(fmt.Sprintf("Warning: failed to read file %s: %v", f, err))
		return "", nil
	}

	sysPrompt := p.cfg.SystemPrompt + config.LanguageInstruction(p.cfg.Language)
	siblingList := strings.Join(siblingFiles, ", ")
	overhead := maxExtractionOverhead(sysPrompt, f, siblingList, p.cfg.ExtractionSteps)
	fileLimit := calculateFileLimit(p.client.NumCtx(), overhead)

	contentStr := string(contentBytes)
	overlap := defaultChunkOverlap
	if overlap > fileLimit/4 {
		overlap = fileLimit / 4
	}
	chunks, err := chunkTextWithOverlap(contentStr, fileLimit, overlap)
	if err != nil {
		return "", fmt.Errorf("failed to chunk file %s: %w", f, err)
	}

	stepFacts := make([][]string, len(p.cfg.ExtractionSteps))
	for chunkIdx, chunk := range chunks {
		chunkMsg := ""
		if len(chunks) > 1 {
			chunkMsg = fmt.Sprintf(" (Chunk %d of %d)", chunkIdx+1, len(chunks))
		}
		for i, step := range p.cfg.ExtractionSteps {
			p.onStatus(fmt.Sprintf("➜ Extracting file (Step %d/%d - %s)%s: %s", i+1, len(p.cfg.ExtractionSteps), step.Name, chunkMsg, f))

			userPrompt := buildExtractionPrompt(f, siblingList, chunk, step)
			res, err := p.client.CallLLM(ctx, sysPrompt, userPrompt)
			if err != nil {
				return "", fmt.Errorf("LLM error extracting %s for %s: %w", step.Name, f, err)
			}
			stepFacts[i] = append(stepFacts[i], stripOuterMarkdownFence(res))
		}
	}

	var factsBuilder strings.Builder
	for i, step := range p.cfg.ExtractionSteps {
		consolidatedFact, err := reduceFileFacts(ctx, p.client, f, step.Name, stepFacts[i], p.cfg, p.onStatus)
		if err != nil {
			return "", err
		}
		if consolidatedFact != "" {
			factsBuilder.WriteString(fmt.Sprintf("#### [%s]\n%s\n\n", step.Name, consolidatedFact))
		}
	}
	facts := strings.TrimSpace(factsBuilder.String())

	p.cache.Files[f] = FileCacheEntry{
		SHA256: fileHash,
		Facts:  facts,
	}

	return facts, nil
}

func synthesizeChildren(ctx context.Context, p *pipelineState, node *DirNode) ([]string, error) {
	var childNames []string
	for name := range node.Children {
		childNames = append(childNames, name)
	}
	sort.Strings(childNames)

	var subsystems []string
	for _, name := range childNames {
		sum, err := synthesizeNode(ctx, p, node.Children[name])
		if err != nil {
			return nil, err
		}
		if sum != "" {
			subsystems = append(subsystems, fmt.Sprintf("### Subsystem: %s\n%s", name, sum))
		}
	}
	return subsystems, nil
}

func collectComponents(ctx context.Context, p *pipelineState, node *DirNode, subsystems []string) ([]string, error) {
	var components []string
	for _, f := range node.Files {
		if err := ctx.Err(); err != nil {
			return nil, err
		}

		facts, err := extractFileFacts(ctx, p, f, node.Files)
		if err != nil {
			return nil, err
		}
		if facts != "" {
			components = append(components, fmt.Sprintf("### File: %s\n%s", path.Base(f), facts))
		}
	}

	return append(components, subsystems...), nil
}

func formatModuleDocument(node *DirNode, summary string) string {
	if len(node.Files) == 0 {
		return fmt.Sprintf("> **Module:** `%s`\n\n%s", node.Path, summary)
	}
	names := make([]string, len(node.Files))
	for i, f := range node.Files {
		names[i] = path.Base(f)
	}
	sort.Strings(names)
	for i, name := range names {
		names[i] = "`" + name + "`"
	}
	return fmt.Sprintf("> **Module:** `%s` | **Source Files:** %s\n\n%s", node.Path, strings.Join(names, ", "), summary)
}

func synthesizeNode(ctx context.Context, p *pipelineState, node *DirNode) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// If this node (and all descendants) is NOT affected, reuse cached summary!
	if !p.affectedDirs[node.Path] && p.cache.Modules[node.Path] != "" {
		p.onStatus(fmt.Sprintf("➜ Reusing cached summary for directory: %s", node.Path))
		return p.cache.Modules[node.Path], nil
	}

	subsystems, err := synthesizeChildren(ctx, p, node)
	if err != nil {
		return "", err
	}

	components, err := collectComponents(ctx, p, node, subsystems)
	if err != nil {
		return "", err
	}

	modulePath := filepath.Join(p.cfg.DocsDir, "modules", toSafeMarkdownFilename(node.Path))
	if len(components) == 0 {
		delete(p.cache.Modules, node.Path)
		_ = tools.RemoveFileSafely(p.repoRoot, modulePath)
		return "", nil
	}

	p.onStatus(fmt.Sprintf("➜ Synthesizing directory: %s (%d total components)", node.Path, len(components)))
	finalSum, err := reduceInChunks(ctx, p.client, node.Path, components, p.cfg, p.onStatus)
	if err != nil {
		return "", err
	}

	// Update module cache
	p.cache.Modules[node.Path] = finalSum

	docContent := formatModuleDocument(node, finalSum)
	if err := tools.WriteFileSafely(p.repoRoot, modulePath, []byte(docContent)); err != nil {
		return "", fmt.Errorf("failed to write module documentation for %s: %w", node.Path, err)
	}

	if err := saveMetadataCache(p.repoRoot, p.cfg.DocsDir, p.cache); err != nil {
		return "", err
	}

	return finalSum, nil
}
