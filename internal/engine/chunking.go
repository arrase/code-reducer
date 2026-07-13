package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/arrase/code-reducer/internal/config"
)

func reduceWithLLM(
	ctx context.Context,
	c llmCaller,
	items []string,
	sysPrompt string,
	buildPrompt func(batch []string) string,
	logMsg func(batch []string) string,
	errMsg string,
	logEvent LogEventFunc,
) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	maxChars := c.NumCtx() * maxCharsMultiplier
	return reduceItems(ctx, items, maxChars, func(batch []string) (string, error) {
		prompt := buildPrompt(batch)
		logEvent(EventStatus, logMsg(batch))
		res, err := c.CallLLM(ctx, sysPrompt, []Message{{Role: "user", Content: prompt}}, false)
		if err != nil {
			return "", fmt.Errorf("%s: %w", errMsg, err)
		}
		return stripOuterMarkdownFence(res), nil
	})
}

func reduceInChunks(ctx context.Context, c llmCaller, nodePath string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error) {
	sysPrompt := cfg.SystemPrompt + "\n" + cfg.ModuleSynthesisPrompt
	buildPrompt := func(batch []string) string {
		return fmt.Sprintf("Synthesize architecture for %s:\n%s", nodePath, strings.Join(batch, "\n\n"))
	}
	logMsg := func(batch []string) string {
		return fmt.Sprintf("➜ LLM Synthesizing chunk for %s (%d items)", nodePath, len(batch))
	}
	return reduceWithLLM(ctx, c, items, sysPrompt, buildPrompt, logMsg, "LLM error during synthesis", logEvent)
}

func reduceFileFacts(ctx context.Context, c llmCaller, filePath string, stepName string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error) {
	if len(items) == 1 {
		return items[0], nil
	}
	sysPrompt := cfg.SystemPrompt + "\n" + cfg.FileFactConsolidationPrompt
	buildPrompt := func(batch []string) string {
		return fmt.Sprintf("Consolidate and deduplicate the extracted facts for %s regarding step '%s':\n%s", filePath, stepName, strings.Join(batch, "\n\n"))
	}
	logMsg := func(batch []string) string {
		return fmt.Sprintf("➜ LLM Consolidating facts for %s (%d items)", filePath, len(batch))
	}
	return reduceWithLLM(ctx, c, items, sysPrompt, buildPrompt, logMsg, "LLM error during file fact consolidation", logEvent)
}

func reduceItems(ctx context.Context, items []string, maxChars int, reduceFn func(batch []string) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	// If there is exactly one item and it exceeds the max allowed character limit, truncate it.
	if len(items) == 1 && utf8.RuneCountInString(items[0]) > maxChars {
		runes := []rune(items[0])
		items[0] = string(runes[:maxChars]) + "\n...[truncated]"
	}

	var batches [][]string
	var currentBatch []string
	currentLen := 0

	for _, item := range items {
		itemRunes := utf8.RuneCountInString(item)
		if currentLen+itemRunes > maxChars && len(currentBatch) > 0 {
			batches = append(batches, currentBatch)
			currentBatch = []string{item}
			currentLen = itemRunes
		} else {
			currentBatch = append(currentBatch, item)
			currentLen += itemRunes
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
		if err := ctx.Err(); err != nil {
			return "", err
		}
		chunkRes, err := reduceItems(ctx, batch, maxChars, reduceFn)
		if err != nil {
			return "", err
		}
		intermediate = append(intermediate, chunkRes)
	}
	return reduceItems(ctx, intermediate, maxChars, reduceFn)
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
