package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
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

// SaveConfig writes the configuration to .code-reducer.yaml in the specified directory.
func SaveConfig(cwd string, cfg *Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("failed to marshal yaml: %w", err)
	}

	yamlStr := string(data)
	replacements := []string{
		"\nsystem_prompt:", "\n\nsystem_prompt:",
		"\nmodule_synthesis_prompt:", "\n\nmodule_synthesis_prompt:",
		"\narchitecture_prompt:", "\n\narchitecture_prompt:",
		"\nfile_fact_consolidation_prompt:", "\n\nfile_fact_consolidation_prompt:",
		"\nextraction_steps:", "\n\nextraction_steps:",
		"\nignore:", "\n\nignore:",
	}
	for i := 0; i < len(replacements); i += 2 {
		yamlStr = strings.ReplaceAll(yamlStr, replacements[i], replacements[i+1])
	}

	tmpFile, err := os.CreateTemp(cwd, ConfigFileName+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpName)
	}()

	if _, err := tmpFile.Write([]byte(yamlStr)); err != nil {
		return fmt.Errorf("failed to write config to temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, configFilePerm); err != nil {
		return fmt.Errorf("failed to chmod temp file: %w", err)
	}

	if err := os.Rename(tmpName, getConfigPath(cwd)); err != nil {
		return fmt.Errorf("failed to rename temp config file: %w", err)
	}
	return nil
}
