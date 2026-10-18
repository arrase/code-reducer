package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
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

// ApplyDefaults populates empty or zero fields in cfg with system default values.
func ApplyDefaults(cfg *Config) {
	if cfg.ModelID == "" {
		cfg.ModelID = OllamaDefaultModelID
	}
	if cfg.OllamaBaseURL == "" {
		cfg.OllamaBaseURL = OllamaDefaultBaseURL
	}
	if cfg.OllamaNumCtx <= 0 {
		cfg.OllamaNumCtx = OllamaDefaultNumCtx
	}
	cfg.DocsDir = strings.TrimSpace(cfg.DocsDir)
	if cfg.DocsDir == "" {
		cfg.DocsDir = DefaultDocsDir
	}
	cfg.DocsDir = filepath.ToSlash(filepath.Clean(cfg.DocsDir))
	if len(cfg.ExtractionSteps) == 0 {
		cfg.ExtractionSteps = DefaultExtractionSteps
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = DefaultSystemPrompt
	}
	if cfg.ModuleSynthesisPrompt == "" {
		cfg.ModuleSynthesisPrompt = DefaultModuleSynthesisPrompt
	}
	if cfg.ArchitecturePrompt == "" {
		cfg.ArchitecturePrompt = DefaultArchitecturePrompt
	}
	if cfg.QuickstartPrompt == "" {
		cfg.QuickstartPrompt = DefaultQuickstartPrompt
	}
	if cfg.FileFactConsolidationPrompt == "" {
		cfg.FileFactConsolidationPrompt = DefaultFileFactConsolidationPrompt
	}
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
	modelID := cfgVal
	if envVal := os.Getenv(CodeReducerModelIDEnvKey); envVal != "" {
		modelID = envVal
	}
	if flagVal != "" {
		modelID = flagVal
	}
	return modelID
}

func resolveBaseURL(cfgVal string) string {
	if envVal := os.Getenv(OllamaBaseURLEnvKey); envVal != "" {
		return envVal
	}
	return cfgVal
}

func parsePositiveInt(val, errMsg string) (int, error) {
	n, err := strconv.Atoi(val)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("%s: %s", errMsg, val)
	}
	return n, nil
}

func resolveNumCtx(cfgVal int, flagVal string) (int, error) {
	numCtx := cfgVal
	if envVal := os.Getenv(OllamaNumCtxEnvKey); envVal != "" {
		n, err := parsePositiveInt(envVal, "invalid value for "+OllamaNumCtxEnvKey)
		if err != nil {
			return 0, err
		}
		numCtx = n
	}
	if flagVal != "" {
		n, err := parsePositiveInt(flagVal, "invalid value for num-ctx flag")
		if err != nil {
			return 0, err
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
	ApplyDefaults(cfg)

	numCtx, err := resolveNumCtx(cfg.OllamaNumCtx, numCtxFlag)
	if err != nil {
		return nil, err
	}

	cfg.Ignore = deduplicate(cfg.Ignore)
	cfg.ModelID = resolveModelID(cfg.ModelID, modelIDFlag)
	cfg.OllamaBaseURL = resolveBaseURL(cfg.OllamaBaseURL)
	cfg.OllamaNumCtx = numCtx
	return cfg, nil
}
