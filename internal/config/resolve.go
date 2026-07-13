package config

import (
	"fmt"
	"os"
	"strconv"
)

// deduplicate removes duplicates from a slice.
func deduplicate(a []string) []string {
	seen := make(map[string]bool)
	var result []string
	for _, item := range a {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

func resolveString(yamlVal, defaultVal string) string {
	if yamlVal != "" {
		return yamlVal
	}
	return defaultVal
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
	resolvedIgnore := deduplicate(cfg.Ignore)

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
	resolved.SystemPrompt = resolveString(cfg.SystemPrompt, DefaultSystemPrompt)
	resolved.ModuleSynthesisPrompt = resolveString(cfg.ModuleSynthesisPrompt, DefaultModuleSynthesisPrompt)
	resolved.ArchitecturePrompt = resolveString(cfg.ArchitecturePrompt, DefaultArchitecturePrompt)
	resolved.FileFactConsolidationPrompt = resolveString(cfg.FileFactConsolidationPrompt, DefaultFileFactConsolidationPrompt)

	return resolved, nil
}
