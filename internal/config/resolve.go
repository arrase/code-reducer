package config

import (
	"fmt"
	"os"
	"strconv"
)

// mergeAndDeduplicate merges two slices and removes duplicates.
func mergeAndDeduplicate[T comparable](a, b []T) []T {
	seen := make(map[T]bool)
	var result []T
	for _, item := range append(a, b...) {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

// ResolveConfig merges CLI overrides, environment variables, YAML config, and system defaults.
// It returns a fully resolved Config struct ready to be used by the pipeline runner and LLM client.
func ResolveConfig(repoRoot, modelIDFlag, numCtxFlag string) (*Config, error) {
	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, fmt.Errorf("failed to load configuration file: %w", err)
		}
		cfg = &Config{}
	}

	// Deduplicate ignores
	resolvedIgnore := mergeAndDeduplicate(cfg.Ignore, nil)

	// Extraction steps
	resolvedSteps := cfg.ExtractionSteps
	if len(resolvedSteps) == 0 {
		resolvedSteps = DefaultExtractionSteps
	}

	resolved := &Config{
		Ignore:          resolvedIgnore,
		ExtractionSteps: resolvedSteps,
	}

	// 1. Resolve Model ID: Default > YAML > Env > Flag
	resolved.ModelID = OllamaDefaultModelID
	if cfg.ModelID != "" {
		resolved.ModelID = cfg.ModelID
	}
	if envVal := os.Getenv(CodeReducerModelIDEnvKey); envVal != "" {
		resolved.ModelID = envVal
	}
	if modelIDFlag != "" {
		resolved.ModelID = modelIDFlag
	}

	// 2. Resolve Ollama Base URL: Default > YAML > Env
	resolved.OllamaBaseURL = OllamaDefaultBaseURL
	if cfg.OllamaBaseURL != "" {
		resolved.OllamaBaseURL = cfg.OllamaBaseURL
	}
	if envVal := os.Getenv(OllamaBaseURLEnvKey); envVal != "" {
		resolved.OllamaBaseURL = envVal
	}

	// 3. Resolve Ollama Context Size: Default > YAML > Env > Flag
	resolved.OllamaNumCtx = OllamaDefaultNumCtx
	if cfg.OllamaNumCtx > 0 {
		resolved.OllamaNumCtx = cfg.OllamaNumCtx
	}
	if envVal := os.Getenv(OllamaNumCtxEnvKey); envVal != "" {
		if n, err := strconv.Atoi(envVal); err == nil && n > 0 {
			resolved.OllamaNumCtx = n
		}
	}
	if numCtxFlag != "" {
		if n, err := strconv.Atoi(numCtxFlag); err == nil && n > 0 {
			resolved.OllamaNumCtx = n
		}
	}

	// 4. Resolve DocsDir: Default > YAML
	resolved.DocsDir = DefaultDocsDir
	if cfg.DocsDir != "" {
		resolved.DocsDir = cfg.DocsDir
	}

	// 5. Resolve prompts: Config > Default
	resolved.SystemPrompt = DefaultSystemPrompt
	if cfg.SystemPrompt != "" {
		resolved.SystemPrompt = cfg.SystemPrompt
	}
	resolved.ModuleSynthesisPrompt = DefaultModuleSynthesisPrompt
	if cfg.ModuleSynthesisPrompt != "" {
		resolved.ModuleSynthesisPrompt = cfg.ModuleSynthesisPrompt
	}
	resolved.ArchitecturePrompt = DefaultArchitecturePrompt
	if cfg.ArchitecturePrompt != "" {
		resolved.ArchitecturePrompt = cfg.ArchitecturePrompt
	}
	resolved.FileFactConsolidationPrompt = DefaultFileFactConsolidationPrompt
	if cfg.FileFactConsolidationPrompt != "" {
		resolved.FileFactConsolidationPrompt = cfg.FileFactConsolidationPrompt
	}

	return resolved, nil
}
