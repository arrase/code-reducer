package engine

import (
	"context"
	"fmt"
	"strings"
)

func reduceInChunks(ctx context.Context, c *LLMClient, nodePath string, items []string, logEvent func(string, string)) (string, error) {
	if len(items) == 0 {
		return "", nil
	}

	maxChunkChars := c.NumCtx * 3

	// If there is exactly one item and it exceeds the max allowed character limit, truncate it.
	if len(items) == 1 && len(items[0]) > maxChunkChars {
		logEvent("status", fmt.Sprintf("➜ Warning: Item for %s too large, truncating to fit context window", nodePath))
		items[0] = items[0][:maxChunkChars] + "\n...[truncated]"
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
		return StripOuterMarkdownFence(res), nil
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
