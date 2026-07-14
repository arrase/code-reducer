# internal/config — Configuration Module

## Module Responsibility

This module provides the declarative configuration schema, default values, and persistence primitives for Code-Reducer. It defines:

*   **`Config`** — Centralized struct carrying all runtime parameters consumed by the synthesis pipeline (LLM model identity, Ollama base URL/context size, document directory, prompt templates, extraction step definitions, ignore lists).
*   **`ExtractionStep`** — Struct holding a single extraction phase name and its system prompt.

The module exposes three I/O functions (`ConfigExists`, `LoadConfig`, `SaveConfig`) that manage the `.code-reducer.yaml` file using an atomic write pattern to prevent readers from observing partially-written state. A fourth function, `ResolveConfig`, merges CLI flags, environment variables, YAML config, and hardcoded defaults into a single resolved `*Config`.

## Data Flow

```
CLI flags / env vars ──► ResolveConfig (merge) ──► *Config ──► Synthesis pipeline consumer
                                                              │
YAML (.code-reducer.yaml) ◄──────────── LoadConfig ────────┘
                                                              ▲
SaveConfig ───────────────────────────────────────────────────┘
```

All configuration values flow downstream to the LLM client and synthesis stages. No mutable state exists within this module; all functions are pure or side-effect only through disk I/O.

---

## Types

### ExtractionStep

Represents a single fact-extraction phase executed against source files during the file-fact extraction stage. Each step carries a human-readable name and an LLM system prompt that guides extraction behavior.

| Field | Type | Tags |
|-------|------|------|
| `Name` | `string` | `yaml:"name"` |
| `Prompt` | `string` | `yaml:"prompt"` |

### Config

Central configuration struct. All fields are optional and may be left at zero values; defaults kick in during resolution if the field is empty or unset.

| Field | Type | Tags | Notes |
|-------|------|------|-------|
| `ModelID` | `string` | `yaml:"model_id"` | LLM model identifier (e.g., `"ornith:9b"`). |
| `OllamaBaseURL` | `string` | `yaml:"ollama_base_url"` | Base URL for the Ollama inference server. |
| `OllamaNumCtx` | `int` | `yaml:"ollama_num_ctx"` | Context size parameter forwarded to Ollama. |
| `DocsDir` | `string` | `yaml:"docs_dir"` | Directory path where generated documentation is written. |
| `SystemPrompt` | `string` | `yaml:"system_prompt"` | System prompt injected into the model at synthesis time. |
| `ModuleSynthesisPrompt` | `string` | `yaml:"module_synthesis_prompt"` | Prompt used during module-level synthesis. |
| `ArchitecturePrompt` | `string` | `yaml:"architecture_prompt"` | Prompt used during architecture-level synthesis. |
| `FileFactConsolidationPrompt` | `string` | `yaml:"file_fact_consolidation_prompt"` | Prompt used to consolidate overlapping file facts before synthesis. |
| `ExtractionSteps` | `[]ExtractionStep` | `yaml:"extraction_steps"` | Ordered list of extraction phases; empty slice triggers fallback to `DefaultExtractionSteps`. |
| `Ignore` | `[]string` | `yaml:"ignore"` | File paths or patterns to skip during analysis. Duplicates are removed by the resolver. |

---

## Constants

### Environment and CLI Keys

| Constant | Value | Type | Purpose |
|----------|-------|------|---------|
| `CodeReducerModelIDEnvKey` | `"CODE_REDUCER_MODEL_ID"` | `string` | Env var key for model ID override in resolver. |
| `OllamaBaseURLEnvKey` | `"OLLAMA_BASE_URL"` | `string` | Env var key for Ollama base URL override in resolver. |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | `string` | Env var key for context size override in resolver. |

### Defaults

| Constant | Value | Type | Purpose |
|----------|-------|------|---------|
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | `string` | Default Ollama inference server address. |
| `OllamaDefaultModelID` | `"ornith:9b"` | `string` | Default LLM model identifier. |
| `OllamaDefaultNumCtx` | `8192` | `int` | Default context size. |
| `DefaultDocsDir` | `"wiki"` | `string` | Default documentation output directory. |
| `ConfigFileName` | `".code-reducer.yaml"` | `string` | Filename used by all three I/O functions and `ResolveConfig`. |
| `DefaultSystemPrompt`, `DefaultModuleSynthesisPrompt`, `DefaultArchitecturePrompt`, `DefaultFileFactConsolidationPrompt` | *(multiline strings)* | `string` | Hardcoded fallback prompts for the four synthesis stages. |

### Internal

| Constant | Value | Type | Notes |
|----------|-------|------|-------|
| `configFilePerm` | `0600` | *(unexported)* | File permission applied to config file and temp files during write. |

---

## Public API Surface — Types & Variables (from config.go)

### DefaultExtractionSteps

Package-level variable of type `[]ExtractionStep`. Serves as the fallback extraction pipeline when the YAML config contains an empty or nil slice. Each step carries a name and prompt for one of the four fact-extraction phases: *API signatures*, *business logic*, *mutable state & concurrency*, *external I/O & error propagation*.

---

## Public API Surface — I/O Functions (from io.go)

### ConfigExists

```go
func ConfigExists(cwd string) bool
```

Returns `true` if `.code-reducer.yaml` exists at the path formed by joining `cwd` with `ConfigFileName`. The underlying operation is a single `os.Stat` call; no error wrapping occurs. Returns only a boolean to caller.

### LoadConfig

```go
func LoadConfig(cwd string) (*Config, error)
```

Reads and parses `.code-reducer.yaml` from `cwd`. On success returns the populated `*Config`. Failure modes:

| Condition | Return Value | Notes |
|-----------|--------------|-------|
| YAML parse error | `(nil, wrappedError)` | Error is wrapped with `"failed to parse yaml config: %w"`. |
| Raw OS read error (not-not-exist) | `(nil, rawError)` | `os.ReadFile` failure propagates unchanged. |

### SaveConfig

```go
func SaveConfig(cwd string, cfg *Config) error
```

Serializes `cfg` to `.code-reducer.yaml` using an atomic write pattern: create temp file → write bytes → sync → close → chmod → rename over original path. Failure points are wrapped individually with descriptive prefixes (`"failed to <action> config file"`). The final state is either the old file or a fully-written new file — never neither, because of the rename semantics.

---

## Public API Surface — Resolution (from resolve.go)

### ResolveConfig

```go
func ResolveConfig(repoRoot string, modelIDFlag string, numCtxFlag string) (*Config, error)
```

Merges configuration across four precedence tiers:

1.  Hardcoded defaults (`OllamaDefault*`, `DefaultDocsDir`, etc.).
2.  Values parsed from `.code-reducer.yaml` at `repoRoot`.
3.  Environment variables (`CodeReducerModelIDEnvKey`, `OllamaBaseURLEnvKey`, `OllamaNumCtxEnvKey`).
4.  CLI flags (`modelIDFlag`, `numCtxFlag`).

Specific resolution rules:

*   **`Ignore` list**: Deduplicated in-place; first occurrence wins via hash-set tracking.
*   **`ExtractionSteps`**: Falls back to `DefaultExtractionSteps` when the YAML slice is empty or nil.
*   **Numeric fields** (`OllamaNumCtx`): Parsed with `strconv.Atoi`; non-positive values are rejected and the prior tier's value is retained.
*   **String prompts**: Empty YAML values fall back to the corresponding hardcoded default via `resolveString`.

### Internal Helpers (from resolve.go)

| Function | Parameters | Return Types | Description |
|----------|-----------|--------------|-------------|
| `deduplicate` | `a []string` | `[]string` | Removes duplicates from a string slice; first occurrence wins. |
| `resolveString` | `yamlVal string`, `defaultVal string` | `string` | Returns `yamlVal` if non-empty, otherwise returns `defaultVal`. Used for prompt field resolution. |

---

## Error Propagation Summary

*   **config.go**: No runtime operations; no errors can be raised or swallowed here.
*   **io.go**: All disk-read and disk-write functions return error tuples. YAML parse errors are explicitly wrapped with `"failed to parse yaml config: %w"`; raw OS-level read/write errors propagate unchanged through the returned tuple. Write-path errors each carry a unique prefix (`"failed to <action> temp config file"`).
*   **resolve.go**: `LoadConfig` failure is only surfaced when `os.IsNotExist(err)` returns false — missing files are silently replaced with an empty `Config{}` and no error reaches the caller. All other branches (env lookups, flag parsing, deduplication) produce zero errors by design.

---

## External I/O Analysis

*   **Disk**: All functions interact exclusively with `.code-reducer.yaml` under `cwd`. Writes use temp file + rename for atomicity; permissions are set to `0600`.
*   **Environment / Process**: Three env vars (`CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX`) are read by `ResolveConfig`; none are written.
*   **Network**: None. Constants like `OllamaDefaultBaseURL` are string literals; no HTTP client or network calls exist in this module.
*   **Database/APIs**: None referenced.