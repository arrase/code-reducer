package config

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

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
	ModelID            string `yaml:"model_id"`
	OllamaBaseURL      string `yaml:"ollama_base_url"`
	OllamaNumCtx       int    `yaml:"ollama_num_ctx"`
	LangsmithAPIKey    string `yaml:"langsmith_api_key,omitempty"`
	LangchainProject   string `yaml:"langchain_project,omitempty"`
	LangchainTracingV2 string `yaml:"langchain_tracing_v2,omitempty"`
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

// LoadAndApplyConfig loads the configuration and sets the corresponding environment variables
// in the current process (only if they are not already set in the parent environment).
func LoadAndApplyConfig(cwd string) error {
	cfg, err := LoadConfig(cwd)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	if cfg.ModelID != "" && os.Getenv(CodeReducerModelIdEnvKey) == "" {
		_ = os.Setenv(CodeReducerModelIdEnvKey, cfg.ModelID)
	}
	if cfg.OllamaBaseURL != "" && os.Getenv(OllamaBaseUrlEnvKey) == "" {
		_ = os.Setenv(OllamaBaseUrlEnvKey, cfg.OllamaBaseURL)
	}
	if cfg.OllamaNumCtx > 0 && os.Getenv(OllamaNumCtxEnvKey) == "" {
		_ = os.Setenv(OllamaNumCtxEnvKey, strconv.Itoa(cfg.OllamaNumCtx))
	}
	if cfg.LangsmithAPIKey != "" && os.Getenv(LangsmithApiKeyEnvKey) == "" {
		_ = os.Setenv(LangsmithApiKeyEnvKey, cfg.LangsmithAPIKey)
	}
	if cfg.LangchainProject != "" && os.Getenv(LangchainProjectEnvKey) == "" {
		_ = os.Setenv(LangchainProjectEnvKey, cfg.LangchainProject)
	}
	if cfg.LangchainTracingV2 != "" && os.Getenv(LangchainTracingEnvKey) == "" {
		_ = os.Setenv(LangchainTracingEnvKey, cfg.LangchainTracingV2)
	}

	return nil
}

// GetDatabasePath returns the standard path for the database file.
func GetDatabasePath() (string, error) {
	return "code-reducer.sqlite", nil
}

// GetOllamaContextSize returns the configured context size for Ollama, falling back to dynamic scaling.
func GetOllamaContextSize(flagVal string) int {
	if flagVal != "" {
		if n, err := strconv.Atoi(flagVal); err == nil && n > 0 {
			return n
		}
	}
	if envVal := os.Getenv(OllamaNumCtxEnvKey); envVal != "" {
		if n, err := strconv.Atoi(envVal); err == nil && n > 0 {
			return n
		}
	}

	// Dynamic scaling based on host total memory
	mem := getHostTotalMemoryBytes()
	if mem > 32*1024*1024*1024 {
		return 32768
	} else if mem > 16*1024*1024*1024 {
		return 16384
	} else if mem > 8*1024*1024*1024 {
		return 8192
	}
	return 4096
}

func getHostTotalMemoryBytes() uint64 {
	file, err := os.Open("/proc/meminfo")
	if err != nil {
		return 0
	}
	defer file.Close()
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := scanner.Text()
		if strings.HasPrefix(line, "MemTotal:") {
			fields := strings.Fields(line)
			if len(fields) >= 2 {
				val, _ := strconv.ParseUint(fields[1], 10, 64)
				return val * 1024 // meminfo is in kB
			}
		}
	}
	return 0
}
