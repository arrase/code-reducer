package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/arrase/code-reducer/internal/tools"
)

// getConfigPath returns the absolute path to the configuration file.
func getConfigPath(cwd string) string {
	return filepath.Join(cwd, ConfigFileName)
}

// ConfigExists checks if .code-reducer.yaml exists in the specified directory.
func ConfigExists(cwd string) bool {
	_, err := os.Stat(getConfigPath(cwd))
	return err == nil
}

// LoadConfig reads and parses .code-reducer.yaml from the specified directory.
func LoadConfig(cwd string) (*Config, error) {
	data, err := os.ReadFile(getConfigPath(cwd))
	if err != nil {
		return nil, err
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("failed to parse yaml config: %w", err)
	}
	return &cfg, nil
}

func formatYAML(data []byte) string {
	yamlStr := string(data)
	for _, key := range []string{
		"system_prompt:",
		"module_synthesis_prompt:",
		"architecture_prompt:",
		"quickstart_prompt:",
		"file_fact_consolidation_prompt:",
		"extraction_steps:",
		"ignore:",
	} {
		yamlStr = strings.ReplaceAll(yamlStr, "\n"+key, "\n\n"+key)
	}
	return yamlStr
}

// SaveConfig writes the configuration to .code-reducer.yaml in the specified directory.
func SaveConfig(cwd string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %w", err)
	}

	yamlStr := formatYAML(data)
	return tools.WriteFileAtomic(getConfigPath(cwd), []byte(yamlStr), configFilePerm)
}
