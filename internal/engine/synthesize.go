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

func synthesizeNode(p *pipelineContext, node *DirNode) (string, error) {
	if err := p.ctx.Err(); err != nil {
		return "", err
	}

	// If this node (and all descendants) is NOT affected, reuse cached summary!
	if !p.affectedDirs[node.Path] && p.cache.Modules[node.Path] != "" {
		p.logEvent(EventStatus, fmt.Sprintf("➜ Reusing cached summary for directory: %s", node.Path))
		return p.cache.Modules[node.Path], nil
	}

	var childNames []string
	for name := range node.Children {
		childNames = append(childNames, name)
	}
	sort.Strings(childNames)

	childSummaries := make(map[string]string)
	for _, name := range childNames {
		child := node.Children[name]
		sum, err := synthesizeNode(p, child)
		if err != nil {
			return "", err
		}
		if sum != "" {
			childSummaries[name] = sum
		}
	}

	var components []string
	
	// Calculate a 100% dynamic file truncation limit based purely on the context size.
	// Assuming ~4 characters per token, we allocate 75% of the total context window 
	// for the file content, proportionally reserving the remaining 25% for prompts and output.
	numCtx := p.client.NumCtx()
	if numCtx < minNumCtxFloor {
		numCtx = minNumCtxFloor
	}
	fileLimit := int(float64(numCtx * 4) * contextWindowAllocRatio)

	for _, f := range node.Files {
		if err := p.ctx.Err(); err != nil {
			return "", err
		}

		var fileHash string
		var ok bool
		if p.precalculatedHashes != nil {
			fileHash, ok = p.precalculatedHashes[f]
		}

		var contentBytes []byte
		var err error

		// Check cache hit using precalculated hash without reading file
		var facts string
		cachedEntry, exists := p.cache.Files[f]
		if ok && exists && cachedEntry.SHA256 == fileHash {
			facts = cachedEntry.Facts
		} else {
			// Read file only on cache miss
			contentBytes, err = tools.ReadFileSafely(p.repoRoot, f)
			if err != nil {
				p.logEvent(EventStatus, fmt.Sprintf("Warning: failed to read file %s: %v", f, err))
				continue
			}

			if !ok {
				hashSum := sha256.Sum256(contentBytes)
				fileHash = hex.EncodeToString(hashSum[:])
				
				// Re-check cache in case hash was not precalculated but exists in cache
				if exists && cachedEntry.SHA256 == fileHash {
					facts = cachedEntry.Facts
				}
			}
		}

		if facts == "" {
			if contentBytes == nil {
				contentBytes, err = tools.ReadFileSafely(p.repoRoot, f)
				if err != nil {
					p.logEvent(EventStatus, fmt.Sprintf("Warning: failed to read file %s: %v", f, err))
					continue
				}
			}
			contentStr := string(contentBytes)
			overlap := defaultChunkOverlap
			if overlap > fileLimit/4 {
				overlap = fileLimit / 4
			}
			chunks := chunkTextWithOverlap(contentStr, fileLimit, overlap)

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
					userContent := fmt.Sprintf("File: %s%s inside Module: %s\n```\n%s\n```", filepath.Base(f), chunkMsg, node.Path, chunk)
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
			facts = strings.TrimSpace(factsBuilder.String())

			// Update cache
			p.cache.Files[f] = FileCacheEntry{
				SHA256: fileHash,
				Facts:  facts,
			}
		}

		components = append(components, fmt.Sprintf("### File: %s\n%s", filepath.Base(f), facts))
	}

	for _, childName := range childNames {
		if sum, exists := childSummaries[childName]; exists && sum != "" {
			components = append(components, fmt.Sprintf("### Subsystem: %s\n%s", childName, sum))
		}
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
