package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"
)

func reduceInChunks(ctx context.Context, c *LLMClient, nodePath string, items []string, logEvent func(string, string)) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	maxChars := c.NumCtx * 3
	return reduceItems(items, maxChars, func(batch []string) (string, error) {
		prompt := fmt.Sprintf("Synthesize architecture for %s:\n%s", nodePath, strings.Join(batch, "\n\n"))
		logEvent("status", fmt.Sprintf("➜ LLM Synthesizing chunk for %s (%d items)", nodePath, len(batch)))
		res, err := c.CallLLM(ctx, c.GetDefaultSystemPrompt("module_synthesis"), []Message{{Role: "user", Content: prompt}}, false)
		if err != nil {
			return "", fmt.Errorf("LLM error during synthesis: %w", err)
		}
		return StripOuterMarkdownFence(res), nil
	})
}

func reduceFileFacts(ctx context.Context, c *LLMClient, filePath string, stepName string, items []string, logEvent func(string, string)) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	if len(items) == 1 {
		return items[0], nil
	}
	maxChars := c.NumCtx * 3
	return reduceItems(items, maxChars, func(batch []string) (string, error) {
		prompt := fmt.Sprintf("Consolidate and deduplicate the extracted facts for %s regarding step '%s':\n%s", filePath, stepName, strings.Join(batch, "\n\n"))
		logEvent("status", fmt.Sprintf("➜ LLM Consolidating facts for %s (%d items)", filePath, len(batch)))
		sysPrompt := c.GetBaseSystemPrompt() + "You are a specialized code documentation assistant. Consolidate, deduplicate and merge the following facts extracted from different chunks of the same file into a single, cohesive summary."
		res, err := c.CallLLM(ctx, sysPrompt, []Message{{Role: "user", Content: prompt}}, false)
		if err != nil {
			return "", fmt.Errorf("LLM error during file fact consolidation: %w", err)
		}
		return StripOuterMarkdownFence(res), nil
	})
}

func reduceItems(items []string, maxChars int, reduceFn func(batch []string) (string, error)) (string, error) {
	// If there is exactly one item and it exceeds the max allowed character limit, truncate it.
	if len(items) == 1 && utf8.RuneCountInString(items[0]) > maxChars {
		runes := []rune(items[0])
		items[0] = string(runes[:maxChars]) + "\n...[truncated]"
	}

	var batches [][]string
	var currentBatch []string
	currentLen := 0

	for _, item := range items {
		if currentLen+len(item) > maxChars && len(currentBatch) > 0 {
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
		allowedPerItem := maxChars / len(items)
		var truncatedItems []string
		for _, item := range items {
			if utf8.RuneCountInString(item) > allowedPerItem {
				runes := []rune(item)
				truncatedItems = append(truncatedItems, string(runes[:allowedPerItem])+"\n...[truncated]")
			} else {
				truncatedItems = append(truncatedItems, item)
			}
		}
		batches = [][]string{truncatedItems}
	}

	if len(batches) == 1 {
		return reduceFn(batches[0])
	}

	var intermediate []string
	for _, batch := range batches {
		chunkRes, err := reduceItems(batch, maxChars, reduceFn)
		if err != nil {
			return "", err
		}
		intermediate = append(intermediate, chunkRes)
	}
	return reduceItems(intermediate, maxChars, reduceFn)
}

// chunkTextWithOverlap splits text into chunks of maxRunes length, with overlapRunes of overlap between adjacent chunks.
func chunkTextWithOverlap(text string, maxRunes int, overlapRunes int) []string {
	runes := []rune(text)
	if maxRunes <= 0 {
		return []string{text}
	}
	if overlapRunes >= maxRunes {
		overlapRunes = maxRunes / 2
	}
	if len(runes) <= maxRunes {
		return []string{text}
	}
	var chunks []string
	step := maxRunes - overlapRunes
	if step <= 0 {
		return []string{text}
	}
	for i := 0; i < len(runes); i += step {
		end := i + maxRunes
		if end > len(runes) {
			end = len(runes)
		}
		chunks = append(chunks, string(runes[i:end]))
		if end == len(runes) {
			break
		}
	}
	return chunks
}
