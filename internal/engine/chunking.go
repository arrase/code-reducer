package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/arrase/code-reducer/internal/config"
)

type reductionConfig struct {
	sysPrompt   string
	buildPrompt func(batch []string) string
	logMsg      func(batch []string) string
	errMsg      string
	logEvent    LogEventFunc
}

func reduceWithLLM(
	ctx context.Context,
	c llmCaller,
	items []string,
	cfg reductionConfig,
) (string, error) {
	if len(items) == 0 {
		return "", nil
	}
	maxChars := c.NumCtx() * maxCharsMultiplier
	return reduceItems(ctx, items, maxChars, func(batch []string) (string, error) {
		prompt := cfg.buildPrompt(batch)
		cfg.logEvent(EventStatus, cfg.logMsg(batch))
		res, err := c.CallLLM(ctx, cfg.sysPrompt, []Message{{Role: "user", Content: prompt}}, false)
		if err != nil {
			return "", fmt.Errorf("%s: %w", cfg.errMsg, err)
		}
		return stripOuterMarkdownFence(res), nil
	})
}

func reduceInChunks(ctx context.Context, c llmCaller, nodePath string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error) {
	redCfg := reductionConfig{
		sysPrompt: cfg.SystemPrompt + "\n" + cfg.ModuleSynthesisPrompt,
		buildPrompt: func(batch []string) string {
			return fmt.Sprintf("Synthesize architecture for %s:\n%s", nodePath, strings.Join(batch, "\n\n"))
		},
		logMsg: func(batch []string) string {
			return fmt.Sprintf("➜ LLM Synthesizing chunk for %s (%d items)", nodePath, len(batch))
		},
		errMsg:   "LLM error during synthesis",
		logEvent: logEvent,
	}
	return reduceWithLLM(ctx, c, items, redCfg)
}

func reduceFileFacts(ctx context.Context, c llmCaller, filePath string, stepName string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error) {
	if len(items) == 1 {
		return items[0], nil
	}
	redCfg := reductionConfig{
		sysPrompt: cfg.SystemPrompt + "\n" + cfg.FileFactConsolidationPrompt,
		buildPrompt: func(batch []string) string {
			return fmt.Sprintf("Consolidate and deduplicate the extracted facts for %s regarding step '%s':\n%s", filePath, stepName, strings.Join(batch, "\n\n"))
		},
		logMsg: func(batch []string) string {
			return fmt.Sprintf("➜ LLM Consolidating facts for %s (%d items)", filePath, len(batch))
		},
		errMsg:   "LLM error during file fact consolidation",
		logEvent: logEvent,
	}
	return reduceWithLLM(ctx, c, items, redCfg)
}

func expandOversizedItems(items []string, maxChars int) ([]string, error) {
	var expanded []string
	for _, item := range items {
		if utf8.RuneCountInString(item) <= maxChars {
			expanded = append(expanded, item)
			continue
		}
		chunks, err := chunkTextWithOverlap(item, maxChars/2, maxChars/10)
		if err != nil {
			return nil, err
		}
		expanded = append(expanded, chunks...)
	}
	return expanded, nil
}

func batchItems(items []string, maxChars int) [][]string {
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
	return batches
}

func countRunes(items []string) int {
	total := 0
	for _, item := range items {
		total += utf8.RuneCountInString(item)
	}
	return total
}

func reduceItems(ctx context.Context, items []string, maxChars int, reduceFn func(batch []string) (string, error)) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}

	expanded, err := expandOversizedItems(items, maxChars)
	if err != nil {
		return "", err
	}
	items = expanded

	batches := batchItems(items, maxChars)
	if len(batches) == 1 {
		return reduceFn(batches[0])
	}

	totalInputRunes := countRunes(items)
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

	// Loop Prevention: If the LLM is failing to condense the information (output >= 95% of input),
	// we stop adding layers and concatenate. This prevents infinite map-reduce loops.
	if countRunes(intermediate) >= (totalInputRunes * 95 / 100) {
		return strings.Join(intermediate, "\n\n"), nil
	}

	return reduceItems(ctx, intermediate, maxChars, reduceFn)
}

// chunkTextWithOverlap splits text into chunks of maxRunes length, with overlapRunes of overlap between adjacent chunks.
func chunkTextWithOverlap(text string, maxRunes int, overlapRunes int) ([]string, error) {
	if maxRunes <= 0 {
		return nil, fmt.Errorf("maxRunes must be greater than 0")
	}
	if overlapRunes >= maxRunes {
		return nil, fmt.Errorf("overlapRunes must be strictly less than maxRunes")
	}

	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}, nil
	}

	var chunks []string
	step := maxRunes - overlapRunes
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
	return chunks, nil
}
