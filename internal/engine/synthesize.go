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

func synthesizeNode(ctx context.Context, c *LLMClient, node *DirNode, repoRoot string, cfg *config.Config, cache *MetadataCache, affectedDirs map[string]bool, precalculatedHashes map[string]string, logEvent func(string, string)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// If this node (and all descendants) is NOT affected, reuse cached summary!
	if !affectedDirs[node.Path] && cache.Modules[node.Path] != "" {
		logEvent("status", fmt.Sprintf("➜ Reusing cached summary for directory: %s", node.Path))
		return cache.Modules[node.Path], nil
	}

	var childNames []string
	for name := range node.Children {
		childNames = append(childNames, name)
	}
	sort.Strings(childNames)

	childSummaries := make(map[string]string)
	for _, name := range childNames {
		child := node.Children[name]
		sum, err := synthesizeNode(ctx, c, child, repoRoot, cfg, cache, affectedDirs, precalculatedHashes, logEvent)
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
	numCtx := c.NumCtx
	if numCtx < 512 {
		numCtx = 512
	}
	fileLimit := int(float64(numCtx * 4) * 0.75)

	for _, f := range node.Files {
		if err := ctx.Err(); err != nil {
			return "", err
		}

		var fileHash string
		var ok bool
		if precalculatedHashes != nil {
			fileHash, ok = precalculatedHashes[f]
		}

		var contentBytes []byte
		var err error

		// Check cache hit using precalculated hash without reading file
		var facts string
		cachedEntry, exists := cache.Files[f]
		if ok && exists && cachedEntry.SHA256 == fileHash {
			facts = cachedEntry.Facts
		} else {
			// Read file only on cache miss
			contentBytes, err = tools.ReadFileSafely(repoRoot, f)
			if err != nil {
				logEvent("status", fmt.Sprintf("Warning: failed to read file %s: %v", f, err))
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
			contentStr := string(contentBytes)
			overlap := 800
			if overlap > fileLimit/4 {
				overlap = fileLimit / 4
			}
			chunks := chunkTextWithOverlap(contentStr, fileLimit, overlap)

			var factsBuilder strings.Builder
			for i, step := range cfg.ExtractionSteps {
				var stepFacts []string
				for chunkIdx, chunk := range chunks {
					chunkMsg := ""
					if len(chunks) > 1 {
						chunkMsg = fmt.Sprintf(" (Chunk %d of %d)", chunkIdx+1, len(chunks))
					}
					logEvent("status", fmt.Sprintf("➜ Extracting file (Step %d/%d - %s)%s: %s", i+1, len(cfg.ExtractionSteps), step.Name, chunkMsg, f))
					
					systemPrompt := c.GetBaseSystemPrompt() + step.Prompt
					userContent := fmt.Sprintf("File: %s%s inside Module: %s\n```\n%s\n```", filepath.Base(f), chunkMsg, node.Path, chunk)
					res, err := c.CallLLM(ctx, systemPrompt, []Message{{Role: "user", Content: userContent}}, false)
					if err != nil {
						return "", fmt.Errorf("LLM error extracting %s for %s: %w", step.Name, f, err)
					}
					stepFacts = append(stepFacts, StripOuterMarkdownFence(res))
				}

				consolidatedFact, err := reduceFileFacts(ctx, c, f, step.Name, stepFacts, logEvent)
				if err != nil {
					return "", err
				}

				factsBuilder.WriteString(fmt.Sprintf("#### [%s]\n%s\n\n", step.Name, consolidatedFact))
			}
			facts = strings.TrimSpace(factsBuilder.String())

			// Update cache
			cache.Files[f] = FileCacheEntry{
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

	modulePath := filepath.Join(cfg.DocsDir, "modules", ToSafeMarkdownFilename(node.Path))
	if err := tools.WriteFileSafely(repoRoot, modulePath, []byte(finalSum)); err != nil {
		return "", fmt.Errorf("failed to write module documentation for %s: %w", node.Path, err)
	}

	return finalSum, nil
}
