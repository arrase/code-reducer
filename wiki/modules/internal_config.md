# Configuration Module (`internal/config`)

## Responsibility

This module owns the application's runtime configuration lifecycle: loading defaults, merging user-provided overrides from multiple sources (CLI flags, environment variables, YAML file), persisting state, and injecting resolved values into the OS process environment for downstream components. It is a single source of truth for model identity (`CodeReducerModelIdEnvKey`), Ollama server connection parameters, tracing instrumentation options, and ignored-path/last-commit metadata stored in `.code-reducer.yaml`.

## Data Flow: Configuration Resolution Pipeline

The core operation is `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config`, which orchestrates a four-tier resolution strategy:

1. **System Defaults** — Hard-coded fallbacks (`OllamaDefaultBaseURL = "http://localhost:11434"`, `OllamaDefaultNumCtx = 8192`) serve as the base layer.
2. **YAML Config File** — If present (detected via `ConfigExists(cwd string) bool`), `LoadConfig(cwd string) (*Config, error)` unmarshals `.code-reducer.yaml` from `repoRoot`. Missing or unreadable files yield an empty struct rather than returning errors.
3. **Environment Variables** — Each tier-specific override key (`CodeReducerModelIdEnvKey`, `OllamaBaseUrlEnvKey`, `OllamaNumCtxEnvKey`, `LangsmithApiKeyEnvKey`, `LangchainProjectEnvKey`, `LangchainTracingEnvKey`) is read from `os.Getenv` and applied to the resolved config when non-empty.
4. **CLI Flags** — Explicit positional arguments (`modelIdFlag`, `numCtxFlag`) take precedence over environment variables, providing the highest-priority runtime override for model identity and context window size respectively.

The final resolved `*Config` value is returned with all tracing-related environment variables propagated to the OS process via `os.Setenv`, ensuring downstream packages (e.g., LangSmith/Tracing client initializers) observe consistent configuration without requiring explicit dependency on this module.

## Configuration Types and Schema

```go
type Config struct {
    // Model identity — overridden by CLI/env when present
    CodeReducerModelId string `yaml:"model_id"`

    // Ollama connection parameters
    OllamaBaseUrl  string `yaml:"base_url"`
    OllamaNumCtx   int    `yaml:"num_ctx"`

    // LangSmith tracing configuration
    LangsmithTracingEnabled bool   `yaml:"langsmith_tracing_enabled,omitempty"`
    LangsmithApiKey         string `yaml:"langsmith_api_key,omitempty"`

    // LangChain v2 tracing configuration
    LangchainTracingEnabled bool   `yaml:"langchain_tracing_enabled,omitempty"`
    LangchainProjectName    string `yaml:"langchain_project_name,omitempty"`

    // Operational metadata
    IgnoredPaths []string `yaml:"ignored_paths,omitempty"`
    DocDirectory  string   `yaml:"doc_directory,omitempty"`
    LastCommitSHA string   `yaml:"last_commit_sha,omitempty"`
}
```

## Persistence Operations

- **`ConfigExists(cwd string) bool`** — Returns true if `.code-reducer.yaml` exists in the given working directory via `os.Stat`, enabling conditional loading.
- **`LoadConfig(cwd string) (*Config, error)`** — Reads and unmarshals the YAML file into a `*Config`. On failure (missing/unreadable), returns an initialized empty struct with no error; callers must inspect the returned value for defaults rather than treating nil as "not loaded".
- **`SaveConfig(cwd string, cfg *Config) error`** — Marshals the configuration to YAML and writes it atomically with `os.FileMode(0600)` permissions. Marshaling or write failures are wrapped in a single error value; callers should handle the returned error uniformly without distinguishing cause unless necessary for observability.

## Environment Variable Keys (Process Propagation)

The following keys are injected into `os.Environ` during resolution when their corresponding values are non-empty:

| Key | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Runtime model identifier override |
| `OllamaBaseUrlEnvKey` | Ollama server endpoint override |
| `OllamaNumCtxEnvKey` | Context window token count override |
| `LangsmithApiKeyEnvKey` | LangSmith tracing API key injection |
| `LangchainProjectEnvKey` | LangChain v2 project name injection |
| `LangchainTracingEnvKey` | LangChain v2 tracing enable/disable flag |

This propagation pattern ensures that any process spawned from this application inherits the resolved configuration without requiring explicit re-configuration in downstream binaries.