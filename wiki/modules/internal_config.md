# `internal/config` — Configuration Resolution and Persistence

## Module Responsibility

The `internal/config` package owns the complete lifecycle of application configuration: detection, loading, merging, resolution, and persistence. It provides a layered resolution strategy that merges system defaults → YAML file → environment variables → CLI flags into a single canonical `*Config`. All resolved values are also propagated to external tracing environment variables so downstream services observe them without separate injection logic.

## Constants

| Constant | Purpose |
|---|---|
| `ConfigFileName` | Canonical filename `.code-reducer.yaml` used for all disk read/write operations within the package. |
| `CodeReducerModelIdEnvKey` | Environment variable key that overrides the LLM model identifier at runtime. |
| `OllamaBaseUrlEnvKey` | Environment variable key that overrides the Ollama API base URL at runtime. |
| `OllamaNumCtxEnvKey` | Environment variable key that overrides the Ollama context size (window) at runtime. |
| `LangsmithApiKeyEnvKey` | Environment variable key for the LangSmith tracing API key. |
| `LangchainProjectEnvKey` | Environment variable key for the LangChain project identifier. |
| `LangchainTracingEnvKey` | Environment variable key that enables/disables LangChain v2 tracing. |
| `OllamaDefaultBaseURL` | Fallback base URL used when neither config nor environment overrides the Ollama endpoint. |
| `OllamaDefaultNumCtx` | Default context size (8192) applied when no explicit override is present in any layer. |

## Structs

### `Config`

Schema for `.code-reducer.yaml`. Fields cover: LLM model ID, Ollama settings (base URL, context size), Langsmith/Langchain tracing configuration (API key, project, enabled flag), ignored file paths, and the docs directory path. This struct is the canonical in-memory representation of all configuration sources after resolution.

## Functions

### `ConfigExists(dir string) bool`

Performs a filesystem check via `os.Stat` to determine whether `.code-reducer.yaml` exists within `dir`. Returns `true` only when the file is present; callers use this gate before attempting load operations to avoid silent failures.

### `LoadConfig(dir string) (*Config, error)`

Reads `.code-reducer.yaml` from `dir`, parses it with `yaml.Unmarshal`, and returns a populated `*Config`. Returns an error if the file does not exist (caller should verify via `ConfigExists` first) or if parsing fails. This is the raw-disk loader; no merging, validation, or default injection occurs here.

### `SaveConfig(config *Config, dir string) error`

Serializes a `*Config` to YAML and writes it to `.code-reducer.yaml` in `dir`. The file is created with Unix mode `0600`, ensuring the configuration file remains world-unreadable on disk. This function handles both initial creation and updates; callers are responsible for providing a fully populated config.

### `ResolveConfig(cliFlags, env vars) (*Config, error)`

Terminal resolution step that merges four precedence layers into a single canonical `*Config`:

1. **System defaults** — hardcoded fallbacks (`OllamaDefaultBaseURL`, `OllamaDefaultNumCtx`).
2. **YAML config** — loaded via `LoadConfig` from disk (if present).
3. **Environment variables** — read from the process environment using the defined env keys; overrides YAML values where applicable.
4. **CLI flags** — provided by the caller at startup; highest precedence.

After merging, the resolved struct is written back to external tracing environment variables so that downstream tracing infrastructure observes consistent configuration without separate injection calls. Returns the fully resolved `*Config` and any error encountered during resolution or serialization.