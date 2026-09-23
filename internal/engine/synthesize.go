package engine

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/tools"
)

type pipelineContext struct {
	ctx                 context.Context
	client              llmCaller
	repoRoot            string
	cfg                 *config.Config
	cache               *MetadataCache
	affectedDirs        map[string]bool
	precalculatedHashes map[string]string
	logEvent            LogEventFunc
}

func extractFileFacts(p *pipelineContext, f string, nodePath string, fileLimit int) (string, error) {
	fileHash, hashOk := p.precalculatedHashes[f]
	cachedEntry, cacheExists := p.cache.Files[f]

	// 1. Fast path: cache hit with precalculated hash
	if hashOk && cacheExists && cachedEntry.SHA256 == fileHash {
		return cachedEntry.Facts, nil
	}

	// 2. Read file content once
	contentBytes, err := tools.ReadFileSafely(p.repoRoot, f)
	if err != nil {
		p.logEvent(EventStatus, fmt.Sprintf("Warning: failed to read file %s: %v", f, err))
		return "", nil
	}

	// 3. Compute hash if not precalculated and check cache again
	if !hashOk {
		hashSum := sha256.Sum256(contentBytes)
		fileHash = hex.EncodeToString(hashSum[:])
		if cacheExists && cachedEntry.SHA256 == fileHash {
			return cachedEntry.Facts, nil
		}
	}

	// 4. Extract facts
	contentStr := string(contentBytes)
	overlap := defaultChunkOverlap
	if overlap > fileLimit/4 {
		overlap = fileLimit / 4
	}
	chunks, err := chunkTextWithOverlap(contentStr, fileLimit, overlap)
	if err != nil {
		return "", fmt.Errorf("failed to chunk file %s: %w", f, err)
	}

	var factsBuilder strings.Builder
	for i, step := range p.cfg.ExtractionSteps {
		var stepFacts []string
		for chunkIdx, chunk := range chunks {
			chunkMsg := ""
			if len(chunks) > 1 {
				chunkMsg = fmt.Sprintf(" (Chunk %d of %d)", chunkIdx+1, len(chunks))
			}
			p.logEvent(EventStatus, fmt.Sprintf("➜ Extracting file (Step %d/%d - %s)%s: %s", i+1, len(p.cfg.ExtractionSteps), step.Name, chunkMsg, f))

			systemPrompt := p.cfg.SystemPrompt + "\n" + step.Prompt
			userContent := fmt.Sprintf("File: %s%s inside Module: %s\n```\n%s\n```", filepath.Base(f), chunkMsg, nodePath, chunk)
			res, err := p.client.CallLLM(p.ctx, systemPrompt, []Message{{Role: "user", Content: userContent}}, false)
			if err != nil {
				return "", fmt.Errorf("LLM error extracting %s for %s: %w", step.Name, f, err)
			}
			stepFacts = append(stepFacts, stripOuterMarkdownFence(res))
		}

		consolidatedFact, err := reduceFileFacts(p.ctx, p.client, f, step.Name, stepFacts, p.cfg, p.logEvent)
		if err != nil {
			return "", err
		}

		factsBuilder.WriteString(fmt.Sprintf("#### [%s]\n%s\n\n", step.Name, consolidatedFact))
	}
	facts := strings.TrimSpace(factsBuilder.String())

	// 5. Update cache
	p.cache.Files[f] = FileCacheEntry{
		SHA256: fileHash,
		Facts:  facts,
	}

	return facts, nil
}

func calculateFileLimit(numCtx int) int {
	if numCtx < minNumCtxFloor {
		numCtx = minNumCtxFloor
	}
	return int(float64(numCtx*4) * contextWindowAllocRatio)
}

func synthesizeChildren(p *pipelineContext, node *DirNode) (map[string]string, []string, error) {
	var childNames []string
	for name := range node.Children {
		childNames = append(childNames, name)
	}
	sort.Strings(childNames)

	childSummaries := make(map[string]string)
	for _, name := range childNames {
		sum, err := synthesizeNode(p, node.Children[name])
		if err != nil {
			return nil, nil, err
		}
		if sum != "" {
			childSummaries[name] = sum
		}
	}
	return childSummaries, childNames, nil
}

func collectComponents(p *pipelineContext, node *DirNode, childSummaries map[string]string, childNames []string) ([]string, error) {
	fileLimit := calculateFileLimit(p.client.NumCtx())

	var components []string
	for _, f := range node.Files {
		if err := p.ctx.Err(); err != nil {
			return nil, err
		}

		facts, err := extractFileFacts(p, f, node.Path, fileLimit)
		if err != nil {
			return nil, err
		}
		if facts != "" {
			components = append(components, fmt.Sprintf("### File: %s\n%s", filepath.Base(f), facts))
		}
	}

	for _, childName := range childNames {
		if sum := childSummaries[childName]; sum != "" {
			components = append(components, fmt.Sprintf("### Subsystem: %s\n%s", childName, sum))
		}
	}
	return components, nil
}

func synthesizeNode(p *pipelineContext, node *DirNode) (string, error) {
	if err := p.ctx.Err(); err != nil {
		return "", err
	}

	// If this node (and all descendants) is NOT affected, reuse cached summary!
	if !p.affectedDirs[node.Path] && p.cache.Modules[node.Path] != "" {
		p.logEvent(EventStatus, fmt.Sprintf("➜ Reusing cached summary for directory: %s", node.Path))
		return p.cache.Modules[node.Path], nil
	}

	childSummaries, childNames, err := synthesizeChildren(p, node)
	if err != nil {
		return "", err
	}

	components, err := collectComponents(p, node, childSummaries, childNames)
	if err != nil {
		return "", err
	}

	if len(components) == 0 {
		p.cache.Modules[node.Path] = ""
		return "", nil
	}

	p.logEvent(EventStatus, fmt.Sprintf("➜ Synthesizing directory: %s (%d total components)", node.Path, len(components)))
	finalSum, err := reduceInChunks(p.ctx, p.client, node.Path, components, p.cfg, p.logEvent)
	if err != nil {
		return "", err
	}

	// Update module cache
	p.cache.Modules[node.Path] = finalSum

	modulePath := filepath.Join(p.cfg.DocsDir, "modules", toSafeMarkdownFilename(node.Path))
	if err := tools.WriteFileSafely(p.repoRoot, modulePath, []byte(finalSum)); err != nil {
		return "", fmt.Errorf("failed to write module documentation for %s: %w", node.Path, err)
	}

	return finalSum, nil
}
