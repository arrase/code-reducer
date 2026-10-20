package engine

import (
	"context"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/arrase/code-reducer/internal/config"
)

const (
	defaultChunkOverlap     = 800
	maxCharsMultiplier      = 2
	partialBatchInstruction = "Summarize and consolidate the key technical facts from this partial batch without repeating boilerplate headers"
)

// stripOuterMarkdownFence strips surrounding markdown code fences only if there are no intermediate fences.
func stripOuterMarkdownFence(text string) string {
	trimmed := strings.TrimSpace(text)
	lines := strings.Split(trimmed, "\n")
	if len(lines) < 2 {
		return trimmed
	}
	if !strings.HasPrefix(strings.TrimSpace(lines[0]), "```") || strings.TrimSpace(lines[len(lines)-1]) != "```" {
		return trimmed
	}
	for _, line := range lines[1 : len(lines)-1] {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			return trimmed
		}
	}
	return strings.TrimSpace(strings.Join(lines[1:len(lines)-1], "\n"))
}

type reductionConfig struct {
	sysPrompt   string
	buildPrompt func(batch []string) string
	logMsg      func(batch []string) string
	errMsg      string
	onStatus    StatusFunc
}

func calculateFileLimit(numCtx int, sysPrompt string) int {
	limit := numCtx*maxCharsMultiplier - utf8.RuneCountInString(sysPrompt)
	if limit < defaultChunkOverlap*2 {
		return defaultChunkOverlap * 2
	}
	return limit
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
	promptOverhead := cfg.sysPrompt + "\n" + cfg.buildPrompt(nil) + "\n" + partialBatchInstruction
	maxChars := calculateFileLimit(c.NumCtx(), promptOverhead)
	return reduceItems(ctx, items, maxChars, func(batch []string) (string, error) {
		prompt := cfg.buildPrompt(batch)
		cfg.onStatus(cfg.logMsg(batch))
		res, err := c.CallLLM(ctx, cfg.sysPrompt, prompt)
		if err != nil {
			return "", fmt.Errorf("%s: %w", cfg.errMsg, err)
		}
		return stripOuterMarkdownFence(res), nil
	})
}

func reduceInChunks(ctx context.Context, c llmCaller, nodePath string, items []string, cfg *config.Config, onStatus StatusFunc) (string, error) {
	redCfg := reductionConfig{
		sysPrompt: cfg.SystemPrompt + "\n" + cfg.ModuleSynthesisPrompt + config.LanguageInstruction(cfg.Language),
		buildPrompt: func(batch []string) string {
			return fmt.Sprintf("Synthesize architecture for %s:\n%s", nodePath, strings.Join(batch, "\n\n"))
		},
		logMsg: func(batch []string) string {
			return fmt.Sprintf("➜ LLM Synthesizing chunk for %s (%d items)", nodePath, len(batch))
		},
		errMsg:   "LLM error during synthesis",
		onStatus: onStatus,
	}
	return reduceWithLLM(ctx, c, items, redCfg)
}

func isNoneFact(s string) bool {
	cleaned := strings.TrimSpace(s)
	cleaned = strings.TrimPrefix(cleaned, "- ")
	cleaned = strings.TrimPrefix(cleaned, "* ")
	cleaned = strings.TrimSuffix(strings.TrimSpace(cleaned), ".")
	return cleaned == "" || strings.EqualFold(cleaned, "none") || strings.EqualFold(cleaned, "n/a")
}

func reduceFileFacts(ctx context.Context, c llmCaller, filePath string, stepName string, items []string, cfg *config.Config, onStatus StatusFunc) (string, error) {
	var validItems []string
	for _, item := range items {
		if !isNoneFact(item) {
			validItems = append(validItems, item)
		}
	}
	if len(validItems) == 0 {
		return "", nil
	}
	if len(validItems) == 1 {
		return validItems[0], nil
	}
	redCfg := reductionConfig{
		sysPrompt: cfg.SystemPrompt + "\n" + cfg.FileFactConsolidationPrompt + config.LanguageInstruction(cfg.Language),
		buildPrompt: func(batch []string) string {
			return fmt.Sprintf("Consolidate and deduplicate the extracted facts for %s regarding step '%s':\n%s", filePath, stepName, strings.Join(batch, "\n\n"))
		},
		logMsg: func(batch []string) string {
			return fmt.Sprintf("➜ LLM Consolidating facts for %s (%d items)", filePath, len(batch))
		},
		errMsg:   "LLM error during file fact consolidation",
		onStatus: onStatus,
	}
	res, err := reduceWithLLM(ctx, c, validItems, redCfg)
	if err != nil {
		return "", err
	}
	if isNoneFact(res) {
		return "", nil
	}
	return res, nil
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
		partialBatch := append([]string{partialBatchInstruction}, batch...)
		chunkRes, err := reduceFn(partialBatch)
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
	if overlapRunes < 0 || overlapRunes >= maxRunes {
		return nil, fmt.Errorf("overlapRunes must be between 0 and maxRunes-1")
	}

	runes := []rune(text)
	if len(runes) <= maxRunes {
		return []string{text}, nil
	}

	var chunks []string
	for start := 0; start < len(runes); {
		end := start + maxRunes
		if end >= len(runes) {
			chunks = append(chunks, string(runes[start:]))
			break
		}
		for j := end - 1; j >= start+(maxRunes*3)/4; j-- {
			if runes[j] == '\n' {
				end = j + 1
				break
			}
		}
		chunks = append(chunks, string(runes[start:end]))
		nextStart := end - overlapRunes
		if nextStart <= start {
			nextStart = end
		}
		start = nextStart
	}
	return chunks, nil
}
