package engine

import (
	"context"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/tools"
)

func synthesizeNode(ctx context.Context, c *LLMClient, node *DirNode, repoRoot string, cfg *config.Config, cache *MetadataCache, affectedDirs map[string]bool, logEvent func(string, string)) (string, error) {
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
		sum, err := synthesizeNode(ctx, c, child, repoRoot, cfg, cache, affectedDirs, logEvent)
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

			// Calculate a safe dynamic file truncation limit based on the context size
			limit := c.NumCtx * 3
			if limit > 12000 {
				limit = limit - 4000
			} else {
				limit = 8000
			}

			if len(contentStr) > limit {
				contentStr = contentStr[:limit] + "\n...(truncated)..."
			}

			var factsBuilder strings.Builder
			for i, step := range cfg.ExtractionSteps {
				logEvent("status", fmt.Sprintf("➜ Extracting file (Step %d/%d - %s): %s", i+1, len(cfg.ExtractionSteps), step.Name, f))
				
				systemPrompt := c.GetBaseSystemPrompt() + step.Prompt
				res, err := c.CallLLM(ctx, systemPrompt, []Message{{Role: "user", Content: fmt.Sprintf("File: %s\n```\n%s\n```", f, contentStr)}}, false)
				if err != nil {
					return "", fmt.Errorf("LLM error extracting %s for %s: %w", step.Name, f, err)
				}
				
				factsBuilder.WriteString(fmt.Sprintf("#### [%s]\n%s\n\n", step.Name, StripOuterMarkdownFence(res)))
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
