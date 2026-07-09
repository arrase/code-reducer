package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"gopkg.in/yaml.v3"
)

const (
	CodeReducerModelIdEnvKey = "CODE_REDUCER_MODEL_ID"
	OllamaBaseUrlEnvKey      = "OLLAMA_BASE_URL"
	OllamaNumCtxEnvKey       = "OLLAMA_NUM_CTX"

	OllamaDefaultBaseURL = "http://localhost:11434"
	OllamaDefaultNumCtx  = 8192
	ConfigFileName       = ".code-reducer.yaml"
)

type ExtractionStep struct {
	Name   string `yaml:"name"`
	Prompt string `yaml:"prompt"`
}

// Config represents the schema of .code-reducer.yaml
type Config struct {
	ModelID          string           `yaml:"model_id"`
	OllamaBaseURL    string           `yaml:"ollama_base_url"`
	OllamaNumCtx     int              `yaml:"ollama_num_ctx"`
	DocsDir          string           `yaml:"docs_dir"`
	ExtractionSteps  []ExtractionStep `yaml:"extraction_steps"`
	Ignore           []string         `yaml:"ignore"`
	IgnoreExtensions []string         `yaml:"ignore_extensions"`
}

// ConfigExists checks if .code-reducer.yaml exists in the specified directory.
func ConfigExists(cwd string) bool {
	configPath := filepath.Join(cwd, ConfigFileName)
	_, err := os.Stat(configPath)
	return err == nil
}

// LoadConfig reads and parses .code-reducer.yaml from the specified directory.
func LoadConfig(cwd string) (*Config, error) {
	configPath := filepath.Join(cwd, ConfigFileName)
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse yaml config: %w", err)
	}
	return &cfg, nil
}

// SaveConfig writes the configuration to .code-reducer.yaml in the specified directory.
func SaveConfig(cwd string, cfg *Config) error {
	configPath := filepath.Join(cwd, ConfigFileName)
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %w", err)
	}
	if err := os.WriteFile(configPath, data, 0600); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}
	return nil
}

var DefaultIgnores = []string{
	".git",
	"node_modules",
	"bower_components",
	"dist",
	"build",
	"cache",
	"__pycache__",
	".pytest_cache",
	".mypy_cache",
	".tox",
	"venv",
	".venv",
	"code-reducer",
	".gemini",
	".code-reducer.yaml",
	".code-reducer.lock",
}

var DefaultIgnoredExtensions = []string{
	".png",
	".jpg",
	".jpeg",
	".gif",
	".pdf",
	".exe",
	".dll",
	".so",
	".o",
	".a",
	".zip",
	".gz",
	".tar",
	".lock",
	".pyc",
	".pyo",
	".pyd",
	"-lock.json",
	".lock.yaml",
	"pnpm-lock.yaml",
}

var DefaultExtractionSteps = []ExtractionStep{
	{
		Name:   "API_SIGNATURES",
		Prompt: "Task: Extract the public surface area of the file.\nOutput: A strict Markdown list of all exported structs, interfaces, and methods. For each, note the actual input and output types. Ignore internal logic.",
	},
	{
		Name:   "BUSINESS_LOGIC",
		Prompt: "Task: Analyze the business logic and domain concepts.\nOutput: Explain what business rules or domain concepts this file solves. Describe the high-level algorithmic flow. Ignore implementation details.",
	},
	{
		Name:   "STATE_AND_CONCURRENCY",
		Prompt: "Task: Analyze state mutation and concurrency.\nOutput: List all mutable state (global variables, changing struct fields) and what concurrency mechanisms (e.g., sync.Mutex, channels) protect them. If none, state 'No mutable state'.",
	},
	{
		Name:   "ERRORS_AND_SIDE_EFFECTS",
		Prompt: "Task: Analyze side effects and error handling.\nOutput: Detail how this code communicates with the outside world (I/O like network, disk, DB) and how it handles/returns errors (wrap, sentinel, panic).",
	},
}

// MergeAndDeduplicate merges two slices and removes duplicates.
func MergeAndDeduplicate[T comparable](a, b []T) []T {
	seen := make(map[T]bool)
	var result []T
	for _, item := range a {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	for _, item := range b {
		if !seen[item] {
			seen[item] = true
			result = append(result, item)
		}
	}
	return result
}

// ResolveConfig merges CLI overrides, environment variables, YAML config, and system defaults.
// It returns a fully resolved Config struct ready to be used by the pipeline runner and LLM client.
func ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config {
	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		cfg = &Config{}
	}

	// Deduplicate ignores: start with default ignores, then add user config ignores
	resolvedIgnore := MergeAndDeduplicate(DefaultIgnores, cfg.Ignore)

	// Deduplicate extensions: start with default extensions, then add user config extensions
	resolvedExtensions := MergeAndDeduplicate(DefaultIgnoredExtensions, cfg.IgnoreExtensions)

	// Extraction steps
	resolvedSteps := cfg.ExtractionSteps
	if len(resolvedSteps) == 0 {
		resolvedSteps = DefaultExtractionSteps
	}

	resolved := &Config{
		Ignore:           resolvedIgnore,
		IgnoreExtensions: resolvedExtensions,
		ExtractionSteps:  resolvedSteps,
	}

	// 1. Resolve Model ID: Flag > Env > YAML > Default
	if modelIdFlag != "" {
		resolved.ModelID = modelIdFlag
	} else if envVal := os.Getenv(CodeReducerModelIdEnvKey); envVal != "" {
		resolved.ModelID = envVal
	} else if cfg.ModelID != "" {
		resolved.ModelID = cfg.ModelID
	} else {
		resolved.ModelID = "gemma4:26b-a4b-it-qat"
	}

	// 2. Resolve Ollama Base URL: Env > YAML > Default
	if envVal := os.Getenv(OllamaBaseUrlEnvKey); envVal != "" {
		resolved.OllamaBaseURL = envVal
	} else if cfg.OllamaBaseURL != "" {
		resolved.OllamaBaseURL = cfg.OllamaBaseURL
	} else {
		resolved.OllamaBaseURL = OllamaDefaultBaseURL
	}

	// 3. Resolve Ollama Context Size: Flag > Env > YAML > Default
	var numCtx int
	if numCtxFlag != "" {
		if n, err := strconv.Atoi(numCtxFlag); err == nil && n > 0 {
			numCtx = n
		}
	}
	if numCtx == 0 {
		if envVal := os.Getenv(OllamaNumCtxEnvKey); envVal != "" {
			if n, err := strconv.Atoi(envVal); err == nil && n > 0 {
				numCtx = n
			}
		}
	}
	if numCtx == 0 {
		if cfg.OllamaNumCtx > 0 {
			numCtx = cfg.OllamaNumCtx
		}
	}
	if numCtx == 0 {
		numCtx = OllamaDefaultNumCtx
	}
	resolved.OllamaNumCtx = numCtx

	// 4. Resolve DocsDir: YAML > Default
	if cfg.DocsDir != "" {
		resolved.DocsDir = cfg.DocsDir
	} else {
		resolved.DocsDir = "wiki"
	}

	return resolved
}
