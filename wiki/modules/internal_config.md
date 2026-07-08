# `internal/config` — Configuration Resolution Pipeline

## Module Responsibility

Centralizes all configuration sources for the Code Reducer application and resolves them into a single authoritative `Config` value through a deterministic merge pipeline. Sources are layered: system defaults (lowest priority) ← YAML file ← environment variables ← CLI flags (highest priority). All operations are idempotent where applicable; side-effecting writes enforce restricted permissions (mode `0600`).

## Constants

Environment variable keys and fallback constants that govern external override behavior:

| Constant | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Runtime key for overriding the LLM model identifier. |
| `OllamaBaseUrlEnvKey` | Runtime key for overriding the Ollama server address. |
| `OllamaNumCtxEnvKey` | Runtime key for overriding the context/token count. |
| `LangsmithApiKeyEnvKey` | Runtime key supplying a LangSmith API tracing credential. |
| `LangchainProjectEnvKey` | Runtime key setting the LangChain project name. |
| `LangchainTracingEnvKey` | Runtime key enabling/disabling LangChain v2 tracing. |
| `OllamaDefaultBaseURL` | Fallback Ollama address when no env/config override is present. |
| `OllamaDefaultNumCtx` | Default context size of 8192 tokens applied in the absence of overrides. |
| `ConfigFileName` | Filesystem name `.code-reducer.yaml` used to locate the project configuration file. |

## Types

### `Config`

Core application configuration schema aggregating: LLM model ID, Ollama server/connection settings (base URL, context size), LangSmith/LangChain tracing flags, ignored filesystem paths and extensions, and the docs directory path. Serves as the canonical resolved state consumed downstream by the analyzer pipeline.

## Default Exclusions

Package-level slices defining filesystem paths excluded from analysis:

- `DefaultIgnores` — Directory name exclusions (e.g., `.git`, `node_modules`, build artifacts).
- `DefaultIgnoredExtensions` — File extension exclusions (e.g., images, binaries, lock files).

## I/O Operations

### `ConfigExists(cwd string) bool`

Returns `true` when `.code-reducer.yaml` exists under the given working directory. Used as a gate for conditional config loading in the resolution pipeline.

### `LoadConfig(cwd string) (*Config, error)`

Reads and parses `.code-reducer.yaml` from `cwd` into a populated `Config` struct. Returns an error on parse failure or missing file; callers must check existence via `ConfigExists` first to avoid spurious errors.

### `SaveConfig(cwd string, cfg *Config) error`

Serializes the provided configuration to `.code-reducer.yaml` under `cwd`. Writes with restricted file permissions (`0600`) to prevent unintended access by other users on shared filesystems.

## Merge Utility

### `MergeAndDeduplicate[T comparable](a, b []T) []T`

Generic slice concatenation and deduplication preserving insertion order. Applied when merging default ignore lists with user-provided overrides; preserves first-seen ordering to avoid reordering side effects in downstream path matching logic.

## Resolution Pipeline

### `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config`

Orchestrates the full configuration resolution pipeline: merges CLI flags (`modelIdFlag`, `numCtxFlag`), environment variables (via the key constants above), YAML config (loaded by `LoadConfig`), and system defaults into a single resolved `Config`. Execution order enforces priority from lowest to highest: system defaults → YAML file → environment variables → CLI flags. The returned value is authoritative; downstream components read only this struct, never individual sources.