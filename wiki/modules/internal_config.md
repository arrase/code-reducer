# `internal/config/env.go` — Configuration Resolution Module

## Responsibility & Data Flow

This module provides the configuration resolution system for the code analysis tool. It manages loading, saving, and resolving a structured configuration (`Config`) from multiple sources with explicit priority ordering: CLI flags > environment variables > YAML file values > hardcoded defaults. Empty string/zero values in upstream sources do not override downstream defaults; only non-empty values take precedence (with the exception of model ID, where an explicit zero-length value is treated as "not set" to allow fallback through the chain).

Configuration files are persisted at `<cwd>/.code-reducer.yaml` with Unix permission mode `0600`. The resolution pipeline follows a fixed sequence: existence check → load → merge ignore lists (deduplicated) → merge extension ignores (deduplicated) → apply default extraction steps if user-defined steps absent → resolve each field independently through the priority chain.

---

## Configuration Model

### `Config`

```go
type Config struct {
    ModelID          string           // yaml:"model_id"
    OllamaBaseURL    string           // yaml:"ollama_base_url"
    OllamaNumCtx     int              // yaml:"ollama_num_ctx"
    DocsDir          string           // yaml:"docs_dir"
    ExtractionSteps  []ExtractionStep // yaml:"extraction_steps"
    Ignore           []string         // yaml:"ignore"
    IgnoreExtensions []string         // yaml:"ignore_extensions"
}
```

### `ExtractionStep`

```go
type ExtractionStep struct {
    Name   string // yaml:"name"
    Prompt string // yaml:"prompt"
}
```

No methods are defined on either type. Both types implement the YAML unmarshalling contract via the `yaml` tag annotations, allowing deserialization from `.code-reducer.yaml`.

---

## Public API Functions

### `ConfigExists(cwd string) bool`

Checks whether `.code-reducer.yaml` exists at `<cwd>/.code-reducer.yaml`. Returns `true` if a regular file is present; returns `false` otherwise. Any error from the underlying filesystem call (not found, permission denied, etc.) is swallowed — the function never returns an error type to its caller.

**I/O:** Read-only (`os.Stat`). Swallows all errors. No wrapping.

### `LoadConfig(cwd string) (*Config, error)`

Reads raw bytes from `<cwd>/.code-reducer.yaml`, then unmarshals them into a `*Config`. Returns the parsed config on success; returns an error (or nil config) on failure. Two distinct error behaviors apply:

- **File read failure** (`os.ReadFile`): The underlying error is returned verbatim, unwrapped, to the caller. No context string prepended.
- **YAML parse failure**: Wrapped with prefix `failed to parse yaml config:` using `%w` verb for error chain preservation.

### `SaveConfig(cwd string, cfg *Config) error`

Marshals a `*Config` into YAML and writes it to `<cwd>/.code-reducer.yaml` with permission mode `0600`. Returns an error on failure. Two distinct wrapping behaviors:

- **Marshal failure**: Wrapped with prefix `failed to marshal yaml:` using `%w` verb.
- **Write failure**: Wrapped with prefix `failed to write config file:` using `%w` verb.

No panic, no silent swallow — all errors propagate wrapped.

### `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config`

Resolves a fully populated `*Config` from multiple sources in priority order: CLI flags > environment variables > YAML file value > hardcoded default. Delegates initial file I/O to `LoadConfig`. If that call fails, the error is swallowed and an empty `*Config{}` is returned instead of propagating — the caller receives a non-nil config with zero-valued fields and no error to inspect.

Post-loading resolution steps (merge ignores, merge extensions, default extraction steps, model ID/base URL/context size overrides) execute without any further error checks or wrapping. These operate on the already-resolved struct.

### `MergeAndDeduplicate[T comparable](a, b []T) []T`

Generic utility that concatenates two slices of a comparable type and removes duplicates while preserving order. Used for combining user-provided ignore/extension lists with system defaults so entries appear only once.

---

## Constants & Defaults

### Package-Level Variables

| Variable | Type | Description |
|---|---|---|
| `DefaultIgnores` | `[]string` | Default directory ignore list |
| `DefaultIgnoredExtensions` | `[]string` | Default file extension ignore list |
| `DefaultExtractionSteps` | `[]ExtractionStep` | Default extraction pipeline for LLM prompts (four analysis stages) |

### Constants

```go
const CodeReducerModelIdEnvKey   = "CODE_REDUCER_MODEL_ID"
const OllamaBaseUrlEnvKey        = "OLLAMA_BASE_URL"
const OllamaNumCtxEnvKey         = "OLLAMA_NUM_CTX"
const OllamaDefaultBaseURL       = "http://localhost:11434"
const OllamaDefaultNumCtx        = 8192
const ConfigFileName              = ".code-reducer.yaml"
```

---

## Error Handling Strategy Summary

| Behavior | Where It Happens | Notes |
|---|---|---|
| **Swallow (silent)** | `ConfigExists` stat errors; `ResolveConfig` load failure → empty config | No wrapping, no sentinel value. Caller receives bool or non-nil config with zero-valued fields. |
| **Propagate raw** | `LoadConfig` file-not-found / permission-denied (`os.ReadFile`) | Unmodified error returned to caller. |
| **Wrap with prefix + `%w`** | All YAML parse, marshal, and write errors in `LoadConfig` and `SaveConfig` | Error wrapping preserved via `%w`. |
| **No panic** | Anywhere | No panics occur within this module's public surface. |
| **Sentinel values** | None | No custom error types or sentinel interfaces used. |

### I/O Communication Table

| Function | Read/Write | Path | Notes |
|---|---|---|---|
| `getConfigPath` | Pure (no filesystem access) | — | Unexported helper; returns string only. |
| `ConfigExists` | **Read** (`os.Stat`) | `<cwd>/.code-reducer.yaml` | Swallows all stat errors → returns `false`. |
| `LoadConfig` | **Read** (`os.ReadFile`) | `<cwd>/.code-reducer.yaml` | Unmarshals YAML into `*Config`. Two distinct error behaviors. |
| `SaveConfig` | **Write** (`os.WriteFile`, 0600) | `<cwd>/.code-reducer.yaml` | Writes marshalled YAML with Unix permission mode `0600`. |
| `ResolveConfig` | **Read** (via `LoadConfig`) | `<repoRoot>/.code-reducer.yaml` | Delegates file I/O entirely. No additional direct filesystem access. |

---

## Key Resolution Rules

- Config files stored with restricted permissions (`0600`).
- Empty string values in YAML/CLI/env do not override defaults; only non-empty values take precedence, except for model ID where explicit zero-length is treated as "not set" to allow fallback through the priority chain.
- Context size validation requires positive integers only (enforced downstream of this module).
- A predefined default extraction pipeline provides four analysis stages with specific prompts, ensuring consistent output structure across runs.