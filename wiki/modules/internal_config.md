# internal/config — Configuration Module

## Responsibility and Data Flow

This module provides configuration management, loading, persistence, and resolution for an automated code synthesis/reduction pipeline that generates structured Markdown documentation from source files using a local LLM (Ollama). The data flow proceeds as follows: **`ResolveConfig`** produces a fully-resolved `*Config` by merging CLI flags → environment variables → YAML file contents → built-in defaults. When the resolved config is committed to disk, **`SaveConfig`** performs an atomic write-through-rename sequence so readers never observe a partially-written state. At runtime, **`LoadConfig`** reads and parses that persisted `*Config`. The module exposes three extraction-phase types (`ExtractionStep`, plus the pre-populated `DefaultExtractionSteps` slice) that drive downstream synthesis prompts during code analysis. All operations are local to the filesystem; no network or database interactions occur within this package.

---

## File: config.go — Types, Constants, Default Extraction Steps

### Type Definitions

#### `ExtractionStep`

```go
type ExtractionStep struct {
    Name   string `yaml:"name"`
    Prompt string `yaml:"prompt"`
}
```

Represents a single prompt-based analysis phase applied to source files. The slice of these is used by the synthesis pipeline; each entry maps an extraction name (e.g., `"API_SIGNATURES"`) to its system prompt text.

#### `Config`

```go
type Config struct {
    ModelID                    string   `yaml:"model_id"`
    OllamaBaseURL              string   `yaml:"ollama_base_url"`
    OllamaNumCtx               int      `yaml:"ollama_num_ctx"`
    DocsDir                    string   `yaml:"docs_dir"`
    SystemPrompt               string   `yaml:"system_prompt"`
    ModuleSynthesisPrompt      string   `yaml:"module_synthesis_prompt"`
    ArchitecturePrompt         string   `yaml:"architecture_prompt"`
    FileFactConsolidationPrompt string  `yaml:"file_fact_consolidation_prompt"`
    ExtractionSteps            []ExtractionStep `yaml:"extraction_steps"`
    Ignore                     []string `yaml:"ignore"`
}
```

Holds all configuration parameters consumed by the synthesis pipeline. Fields are initialized from a priority chain of sources (see **resolve.go**). The struct is decoded/encoded via YAML with standard unmarshaling semantics; missing fields retain Go zero values.

### String Constants

| Constant | Value | Purpose |
|---|---|---|
| `CodeReducerModelIDEnvKey` | `"CODE_REDUCER_MODEL_ID"` | Env var key for model ID override |
| `OllamaBaseURLEnvKey` | `"OLLAMA_BASE_URL"` | Env var key for Ollama base URL |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | Env var key for context size override |
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | Default Ollama endpoint |
| `OllamaDefaultModelID` | `"ornith:9b"` | Default model identifier |
| `DefaultDocsDir` | `"wiki"` | Default output directory for generated docs |
| `ConfigFileName` | `".code-reducer.yaml"` | Persisted config filename |

### Integer Constants

| Constant | Value | Purpose |
|---|---|---|
| `OllamaDefaultNumCtx` | `8192` | Default Ollama context size |

### Package-Level Variables (Unexported)

- **`configFilePerm`** — File permission mode (`0600`). Unexported; used internally by `io.go`.
- **`DefaultExtractionSteps`** — Pre-populated slice of four extraction phases:
  - Index 0 — `"API_SIGNATURES"`: Extract public API surface (Markdown list of exported elements, parameters, return types)
  - Index 1 — `"BUSINESS_LOGIC"`: Extract core purpose and domain rules; explain primary problem solved; list high-level algorithm steps
  - Index 2 — `"STATE_AND_CONCURRENCY"`: Identify mutable state and thread safety; synchronization mechanisms; if entirely stateless, output exactly `"No mutable state"`
  - Index 3 — `"ERRORS_AND_SIDE_EFFECTS"`: Analyze external I/O and error propagation; detail interactions with external systems

---

## File: io.go — Configuration Lifecycle Operations

### Public API Surface

| Signature | Description |
|---|---|
| `ConfigExists(cwd string) bool` | Checks if `.code-reducer.yaml` exists in the given directory. |
| `LoadConfig(cwd string) (*Config, error)` | Reads and parses the YAML config file from the specified directory. |
| `SaveConfig(cwd string, cfg *Config) error` | Writes (marshals to YAML) configuration back to `.code-reducer.yaml`. |

### Internal Functions (Non-Exported)

- **`getConfigPath()`** — Resolves absolute path for the config file; referenced by exported functions.
- **`tmpFile.Close()` / `os.Remove(tmpName)` defer block** — Cleanup on any exit path inside `SaveConfig`. Failure to cleanup panics in the deferred function, which does not affect `SaveConfig`'s return value.

### Business Logic Summary

Manages lifecycle operations on a YAML configuration file: reading it into memory as typed structs and writing it back atomically via temp-file + rename. All I/O targets are local filesystem paths under `cwd`. No network or database interaction exists in this file.

### Step-by-Step Algorithm

1. **Resolve path** — Build absolute config-file path from the current working directory using fixed filename constant (`ConfigFileName`).
2. **Check existence** — `os.Stat(getConfigPath(cwd))`; return whether the file is present on disk.
3. **Load (read)** — `os.ReadFile` raw bytes; unmarshal into `*Config` via YAML decoder; propagate parse errors wrapped with context.
4. **Save (write)**:
   - `yaml.Marshal(cfg)` to in-memory bytes.
   - Apply string replacements inserting blank lines before specific top-level keys (`system_prompt`, `module_synthesis_prompt`, `architecture_prompt`, `file_fact_consolidation_prompt`, `extraction_steps`, `ignore`) for consistent formatting.
   - Create a temporary file under the same directory via `os.CreateTemp`.
   - Write formatted YAML bytes to temp file, sync, close.
   - Apply permissions via `os.Chmod(tmpName, configFilePerm)`.
   - Atomically rename temp file over target path (write-through-rename pattern); readers never see a partially-written config file.

### Error Propagation Summary

| Function | External I/O | Error Propagation |
|---|---|---|
| `getConfigPath` | None | N/A — returns `string` |
| `ConfigExists` | `os.Stat` | Swallowed; returns `false` on any error |
| `LoadConfig` | `os.ReadFile`, YAML parse | Returned as `error`; raw OS error or wrapped YAML parse error |
| `SaveConfig` | `CreateTemp`, Write, Sync, Close, Chmod, Rename | Returned as `error`; each step wraps with descriptive prefix (`failed to create temp file: %w`, etc.) |

---

## File: resolve.go — Configuration Resolution and Merging

### Public API Surface

| Signature | Description |
|---|---|
| `ResolveConfig(repoRoot, modelIDFlag, numCtxFlag string) (*Config, error)` | Merges CLI overrides, environment variables, YAML config, and system defaults into a fully resolved Config struct. |

#### Parameters

- **`repoRoot`** — Repository root path (string).
- **`modelIDFlag`** — CLI flag for model ID override (string).
- **`numCtxFlag`** — CLI flag for Ollama context size override (string).

### Business Logic Summary

Solves the configuration resolution problem by producing a single, fully-resolved `*Config` struct from four sources in strict priority order: **CLI flags > environment variables > YAML config file > built-in defaults**. All field resolutions follow this identical chain. Only one real error path reaches the caller — when loading the YAML fails for reasons other than "file not found."

### High-Level Algorithm Steps

1. **Load base config** — Read YAML configuration from disk via `LoadConfig(repoRoot)`; if absent, start with an empty default state.
2. **Pre-process ignore list** — Deduplicate any `Ignore` entries loaded from the YAML file.
3. **Set extraction steps** — Use user-provided extraction steps if present, otherwise fall back to built-in defaults (`DefaultExtractionSteps`).
4. **Resolve each field independently** — All fields follow the same priority chain: start with hardcoded default constant → override if YAML config supplied non-empty value → override if environment variable is set and valid → override if CLI flag was provided.
5. **Return** merged `*Config` struct; return error only if YAML load fails for reasons other than file absence (detected via `os.IsNotExist`).

### External I/O Analysis

| Source | Type | Interaction |
|---|---|---|
| `LoadConfig(repoRoot)` | Disk (filesystem) | Reads configuration file. Error checked via `os.IsNotExist(err)` — absent error yields empty config; otherwise wrapped and propagated. |
| `os.Getenv(...)` × 3 | OS environment | Reads process environment variables for model ID, base URL, context size overrides. Empty string treated as "not set." No errors returned by this call on standard platforms. |
| `strconv.Atoi(...)` × 2 | Pure runtime | Parses numeric strings from env vars and CLI flags. Errors (empty input, non-numeric) are swallowed — code only proceeds if `err == nil && n > 0`. |

### Error Propagation Summary

| Point | Mechanism | Behavior |
|---|---|---|
| YAML load error | Exception (error return) | Wrapped and propagated. If error is NOT `os.IsNotExist`, returns `(*Config, fmt.Errorf("failed to load configuration file: %w", err))`. Caller receives non-nil error. |
| YAML "not exist" | Swallowed | Treated as expected — replaced with empty `&Config{}`. No propagation. |
| `os.Getenv` reads × 3 | Implicit success | No errors returned; empty string == not set. Not propagated. |
| `strconv.Atoi` × 2 | Inline swallow | Only proceeds if both `err == nil && n > 0`. Otherwise defaults retained. No error return. |
| String comparisons (`yamlVal != ""`, etc.) | Implicit success | Treated as non-fatal; no propagation. |

---

## Unexported Implementation Details

- **`configFilePerm`** — File permission mode `0600`, used by `SaveConfig`. Not part of the public API surface.
- All default prompts for `Config` fields are defined as package-level constants and embedded in struct tags.