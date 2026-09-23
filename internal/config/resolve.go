package config

import (
	"errors"
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

func loadBaseConfig(repoRoot string) (*Config, error) {
	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("failed to load configuration file: %w", err)
		}
		return &Config{}, nil
	}
	return cfg, nil
}

func resolveModelID(cfgVal, flagVal string) string {
	modelID := OllamaDefaultModelID
	if cfgVal != "" {
		modelID = cfgVal
	}
	if envVal := os.Getenv(CodeReducerModelIDEnvKey); envVal != "" {
		modelID = envVal
	}
	if flagVal != "" {
		modelID = flagVal
	}
	return modelID
}

func resolveBaseURL(cfgVal string) string {
	baseURL := OllamaDefaultBaseURL
	if cfgVal != "" {
		baseURL = cfgVal
	}
	if envVal := os.Getenv(OllamaBaseURLEnvKey); envVal != "" {
		baseURL = envVal
	}
	return baseURL
}

func resolveNumCtx(cfgVal int, flagVal string) (int, error) {
	numCtx := OllamaDefaultNumCtx
	if cfgVal > 0 {
		numCtx = cfgVal
	}
	if envVal := os.Getenv(OllamaNumCtxEnvKey); envVal != "" {
		n, err := strconv.Atoi(envVal)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid value for %s: %s", OllamaNumCtxEnvKey, envVal)
		}
		numCtx = n
	}
	if flagVal != "" {
		n, err := strconv.Atoi(flagVal)
		if err != nil || n <= 0 {
			return 0, fmt.Errorf("invalid value for num-ctx flag: %s", flagVal)
		}
		numCtx = n
	}
	return numCtx, nil
}

// ResolveConfig merges CLI overrides, environment variables, YAML config, and system defaults.
// It returns a fully resolved Config struct ready to be used by the pipeline runner and LLM client.
func ResolveConfig(repoRoot, modelIDFlag, numCtxFlag string) (*Config, error) {
	cfg, err := loadBaseConfig(repoRoot)
	if err != nil {
		return nil, err
	}

	numCtx, err := resolveNumCtx(cfg.OllamaNumCtx, numCtxFlag)
	if err != nil {
		return nil, err
	}

	resolvedSteps := cfg.ExtractionSteps
	if len(resolvedSteps) == 0 {
		resolvedSteps = DefaultExtractionSteps
	}

	return &Config{
		Ignore:                      deduplicate(cfg.Ignore),
		ExtractionSteps:             resolvedSteps,
		ModelID:                     resolveModelID(cfg.ModelID, modelIDFlag),
		OllamaBaseURL:               resolveBaseURL(cfg.OllamaBaseURL),
		OllamaNumCtx:                numCtx,
		DocsDir:                     resolveString(cfg.DocsDir, DefaultDocsDir),
		SystemPrompt:                resolveString(cfg.SystemPrompt, DefaultSystemPrompt),
		ModuleSynthesisPrompt:       resolveString(cfg.ModuleSynthesisPrompt, DefaultModuleSynthesisPrompt),
		ArchitecturePrompt:          resolveString(cfg.ArchitecturePrompt, DefaultArchitecturePrompt),
		FileFactConsolidationPrompt: resolveString(cfg.FileFactConsolidationPrompt, DefaultFileFactConsolidationPrompt),
	}, nil
}
