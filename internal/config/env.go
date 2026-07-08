package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
)

const (
	CodeReducerModelIdEnvKey  = "CODE_REDUCER_MODEL_ID"
	OllamaBaseUrlEnvKey    = "OLLAMA_BASE_URL"
	OllamaNumCtxEnvKey     = "OLLAMA_NUM_CTX"
	LangsmithApiKeyEnvKey  = "LANGSMITH_API_KEY"
	LangchainProjectEnvKey = "LANGCHAIN_PROJECT"
	LangchainTracingEnvKey = "LANGCHAIN_TRACING_V2"

	OllamaDefaultBaseURL = "http://localhost:11434"
	OllamaDefaultNumCtx  = 8192
)

var ManagedEnvKeys = []string{
	CodeReducerModelIdEnvKey,
	OllamaBaseUrlEnvKey,
	OllamaNumCtxEnvKey,
	LangsmithApiKeyEnvKey,
	LangchainProjectEnvKey,
	LangchainTracingEnvKey,
}

// GetConfigPaths returns standard config directory and configuration file path.
func GetConfigPaths() (string, string) {
	return ".", ".env"
}

// GetDatabasePath returns the standard path for the database file.
func GetDatabasePath() (string, error) {
	return "code-reducer.sqlite", nil
}

// LoadEnv reads standard config file and sets env variables in process if they are unset.
func LoadEnv() (map[string]string, error) {
	_, envPath := GetConfigPaths()
	env, err := ReadEnvFile(envPath)
	if err != nil {
		if os.IsNotExist(err) {
			return make(map[string]string), nil
		}
		return nil, err
	}

	for k, v := range env {
		// Only set if not already set in parent environment
		if os.Getenv(k) == "" {
			_ = os.Setenv(k, v)
		}
	}

	return env, nil
}

// SaveEnv writes configuration variables to standard config file.
func SaveEnv(updates map[string]string) error {
	configDir, envPath := GetConfigPaths()

	if configDir != "." {
		// Ensure directory exists with 0700 permissions
		if err := os.MkdirAll(configDir, 0700); err != nil {
			return fmt.Errorf("failed to create config dir: %w", err)
		}
		_ = os.Chmod(configDir, 0700)
	}

	currentEnv, err := ReadEnvFile(envPath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read existing env file: %w", err)
	}
	if currentEnv == nil {
		currentEnv = make(map[string]string)
	}

	// Apply updates
	for k, v := range updates {
		currentEnv[k] = v
		// Also update the current process env
		_ = os.Setenv(k, v)
	}

	// Format env file content
	var lines []string
	// First write managed keys in order
	written := make(map[string]bool)
	for _, key := range ManagedEnvKeys {
		if val, ok := currentEnv[key]; ok {
			lines = append(lines, fmt.Sprintf("%s=%s", key, formatEnvValue(val)))
			written[key] = true
		}
	}
	// Write any other keys
	for k, v := range currentEnv {
		if !written[k] {
			lines = append(lines, fmt.Sprintf("%s=%s", k, formatEnvValue(v)))
		}
	}

	content := strings.Join(lines, "\n") + "\n"
	err = os.WriteFile(envPath, []byte(content), 0600)
	if err != nil {
		return fmt.Errorf("failed to write env file: %w", err)
	}
	_ = os.Chmod(envPath, 0600)

	return nil
}

// ReadEnvFile parses a basic dotenv file.
func ReadEnvFile(path string) (map[string]string, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	env := make(map[string]string)
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		parts := strings.SplitN(line, "=", 2)
		if len(parts) != 2 {
			continue
		}

		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])

		// Validate key
		if key == "" {
			continue
		}

		env[key] = parseEnvValue(val)
	}

	return env, scanner.Err()
}

func parseEnvValue(val string) string {
	if strings.HasPrefix(val, "\"") && strings.HasSuffix(val, "\"") {
		v := val[1 : len(val)-1]
		v = strings.ReplaceAll(v, "\\n", "\n")
		v = strings.ReplaceAll(v, "\\\"", "\"")
		v = strings.ReplaceAll(v, "\\\\", "\\")
		return v
	}
	return val
}

func formatEnvValue(val string) string {
	v := strings.ReplaceAll(val, "\\", "\\\\")
	v = strings.ReplaceAll(v, "\"", "\\\"")
	v = strings.ReplaceAll(v, "\n", "\\n")
	return fmt.Sprintf("\"%s\"", v)
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
	if mem > 32 * 1024 * 1024 * 1024 {
		return 32768
	} else if mem > 16 * 1024 * 1024 * 1024 {
		return 16384
	} else if mem > 8 * 1024 * 1024 * 1024 {
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
