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
	LangsmithApiKeyEnvKey    = "LANGSMITH_API_KEY"
	LangchainProjectEnvKey   = "LANGCHAIN_PROJECT"
	LangchainTracingEnvKey   = "LANGCHAIN_TRACING_V2"

	OllamaDefaultBaseURL = "http://localhost:11434"
	OllamaDefaultNumCtx  = 8192
	ConfigFileName       = ".code-reducer.yaml"
)

// Config represents the schema of .code-reducer.yaml
type Config struct {
	ModelID            string   `yaml:"model_id"`
	OllamaBaseURL      string   `yaml:"ollama_base_url"`
	OllamaNumCtx       int      `yaml:"ollama_num_ctx"`
	LangsmithAPIKey    string   `yaml:"langsmith_api_key,omitempty"`
	LangchainProject   string   `yaml:"langchain_project,omitempty"`
	LangchainTracingV2 string   `yaml:"langchain_tracing_v2,omitempty"`
	Ignore             []string `yaml:"ignore"`
	IgnoreExtensions   []string `yaml:"ignore_extensions"`
	DocsDir            string   `yaml:"docs_dir"`
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
}

// ResolveConfig merges CLI overrides, environment variables, YAML config, and system defaults.
// It returns a fully resolved Config struct ready to be used by the pipeline runner and LLM client.
// It also sets required external environment variables (such as Langchain/Langsmith tracing variables).
func ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config {
	cfg, err := LoadConfig(repoRoot)
	if err != nil {
		cfg = &Config{}
	}

	var resolvedIgnore []string
	if cfg.Ignore != nil {
		resolvedIgnore = cfg.Ignore
	} else {
		resolvedIgnore = DefaultIgnores
	}

	var resolvedExtensions []string
	if cfg.IgnoreExtensions != nil {
		resolvedExtensions = cfg.IgnoreExtensions
	} else {
		resolvedExtensions = DefaultIgnoredExtensions
	}

	resolved := &Config{
		Ignore:           resolvedIgnore,
		IgnoreExtensions: resolvedExtensions,
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

	// 4. Resolve Langsmith API Key, Langchain Project, and Langchain Tracing V2
	if envVal := os.Getenv(LangsmithApiKeyEnvKey); envVal != "" {
		resolved.LangsmithAPIKey = envVal
	} else {
		resolved.LangsmithAPIKey = cfg.LangsmithAPIKey
	}

	if envVal := os.Getenv(LangchainProjectEnvKey); envVal != "" {
		resolved.LangchainProject = envVal
	} else {
		resolved.LangchainProject = cfg.LangchainProject
	}

	if envVal := os.Getenv(LangchainTracingEnvKey); envVal != "" {
		resolved.LangchainTracingV2 = envVal
	} else {
		resolved.LangchainTracingV2 = cfg.LangchainTracingV2
	}



	// 5. Resolve DocsDir: YAML > Default
	if cfg.DocsDir != "" {
		resolved.DocsDir = cfg.DocsDir
	} else {
		resolved.DocsDir = "wiki"
	}

	return resolved
}

