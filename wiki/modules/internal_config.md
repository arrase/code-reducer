# internal/config — Configuration Management Module

## Responsibility and Data Flow

The `internal/config` package provides centralized configuration persistence for Code Reducer. It abstracts three concerns: **file-backed YAML schema**, **process-environment variable propagation**, and **runtime parameter resolution** (CLI flags → environment variables → host-derived defaults). All configuration is persisted to `.code-reducer.yaml` in the working directory with `0600` permissions; an optional SQLite database (`code-reducer.sqlite`) is referenced by name only.

Configuration loading follows a precedence chain: CLI flag overrides apply first, then environment variables are consulted per key, and finally host-derived values (e.g., total memory) scale defaults like Ollama context size. The `LoadAndApplyConfig` function ties the two storage backends together: it loads from disk and conditionally mirrors selected fields into the parent process's environment only when absent, avoiding clobbering user-specified env vars.

---

## Constants

```go
// Environment variable keys
CodeReducerModelIdEnvKey   // Model identifier for Code Reducer
OllamaBaseUrlEnvKey        // Ollama HTTP base URL
OllamaNumCtxEnvKey         // Ollama context size (token count)
LangsmithApiKeyEnvKey      // LangSmith API authentication token
LangchainProjectEnvKey     // LangChain project name
LangchainTracingEnvKey     // Enable/disable LangChain v2 tracing

// Default values
OllamaDefaultBaseURL       // Fallback HTTP endpoint when user does not set OLLAMA_BASE_URL
OllamaDefaultNumCtx        // 8192 — applied when neither flag nor env var specifies a context limit

// Filesystem identifiers
ConfigFileName             // `.code-reducer.yaml` — on-disk configuration schema name
```

---

## Configuration Schema (`Config`)

```go
type Config struct {
    // Model and inference settings
    ModelId         string   `yaml:"model_id"`
    OllamaBaseUrl   string   `yaml:"ollama_base_url"`
    OllamaNumCtx    int      `yaml:"ollama_num_ctx"`
    
    // LangChain integration
    LangsmithApiKey  string `yaml:"langsmith_api_key"`
    LangchainProject string `yaml:"langchain_project_name"`
    LangchainTracing bool   `yaml:"langchain_tracing_enabled"`
    
    // Operational paths and filters
    IgnoreList      []string `yaml:"ignore_list,omitempty"`
    DocsDir         string   `yaml:"docs_directory"`
}
```

The struct is flattened from a YAML document via standard unmarshalling; zero-valued fields are preserved as defaults during load.

---

## File I/O Operations

### `ConfigExists(dir string) bool`

Performs an OS stat check on `<dir>/.code-reducer.yaml`; returns `true` if the file is present and readable. Used by callers that need to gate configuration loading or detect first-run state without error handling overhead.

### `LoadConfig(dir string) (cfg Config, err error)`

Reads `.code-reducer.yaml` from `dir`, parses via YAML unmarshaler, and returns the populated struct or an error if the file is missing/malformed. This is the primary entry point for deserialization.

### `SaveConfig(cfg Config) error`

Marshals a `Config` to YAML using standard encoder settings and writes to `<working-dir>/.code-reducer.yaml`. File permissions are explicitly set to `0600` via `os.FileMode(0600)` after write; no other process can read the file.

### `LoadAndApplyConfig() error`

Composite operation:
1. Calls `LoadConfig(os.Getwd())` to populate a `Config`.
2. Iterates over the subset of fields that have corresponding environment keys (`ModelId`, `OllamaBaseUrl`, `OllamaNumCtx`, `LangsmithApiKey`, `LangchainProject`, `LangchainTracing`).
3. For each field, reads the matching `os.Getenv(key)` value; if non-empty and the process env does not already contain it, writes the value via `os.Setenv`.

This ensures disk state is authoritative while allowing users to override individual settings through environment variables without losing them on next load.

---

## Runtime Parameter Resolution

### `GetOllamaContextSize(flags []string) (int, error)`

Determines Ollama context size by consulting the following precedence chain:

1. **CLI flag** — scans provided flags for `-num-ctx` / `--num-ctx`; if found, returns that value.
2. **Environment variable** — reads `OllamaNumCtxEnvKey`; if non-empty and valid integer, returns it.
3. **Host-derived scaling** — falls back to a dynamic calculation based on the host's total memory; scales context size proportionally so larger machines receive higher token budgets without explicit user configuration.

Returns the resolved count or an error if no source yields a usable value.

### `GetDatabasePath() (string, error)`

Returns the canonical SQLite database filename (`code-reducer.sqlite`) relative to the working directory. This is intended for callers that need only the filename; no filesystem access occurs and no error path exists.