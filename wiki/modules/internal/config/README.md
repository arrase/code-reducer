# internal/config — Configuration Module for Code-Reducer

## Responsibility and Data Flow

The `internal/config` package owns all runtime configuration for the Code-Reducer static analysis pipeline. It is responsible for three operational concerns: (1) defining a typed configuration schema that captures LLM prompts, extraction step definitions, ignore lists, and Ollama client parameters; (2) performing persistent file I/O with atomic write semantics so that partially-written state never replaces valid config on disk; and (3) resolving the final `*Config` value by merging four sources in a defined priority order — YAML file → environment variables → CLI flags → hardcoded defaults.

The data flow follows this pipeline: **YAML persistence** (`SaveConfig` writes to a temp path, syncs, chmods, then atomically renames over the target) ↔ **File read** (`LoadConfig` reads and unmarshals) ↔ **Resolution** (`ResolveConfig` applies env/flag overrides on top of whatever `LoadConfig` returned). Downstream consumers (pipeline runner, LLM client) receive a single resolved `*Config`.

---

## Configuration Schema

### Types

```go
type ExtractionStep struct {
    Name   string // yaml:"name"
    Prompt string // yaml:"prompt"
}

type Config struct {
    ModelID                     string           // yaml:"model_id"
    OllamaBaseURL               string           // yaml:"ollama_base_url"
    OllamaNumCtx                int              // yaml:"ollama_num_ctx"
    DocsDir                     string           // yaml:"docs_dir"
    SystemPrompt                string           // yaml:"system_prompt"
    ModuleSynthesisPrompt       string           // yaml:"module_synthesis_prompt"
    ArchitecturePrompt          string           // yaml:"architecture_prompt"
    FileFactConsolidationPrompt string           // yaml:"file_fact_consolidation_prompt"
    ExtractionSteps             []ExtractionStep // yaml:"extraction_steps"
    Ignore                      []string         // yaml:"ignore"
}
```

`Config` is the single struct passed between `SaveConfig`, `LoadConfig`, and `ResolveConfig`. All three functions operate on it by value or pointer — `SaveConfig` and `LoadConfig` accept only a directory path, while `ResolveConfig` takes directory + two flag arguments. No methods are defined on `Config`; all mutation happens in external packages via struct initialization.

### Constants

```
CodeReducerModelIDEnvKey  = "CODE_REDUCER_MODEL_ID"        // env key for model ID override
OllamaBaseURLEnvKey       = "OLLAMA_BASE_URL"              // env key for Ollama URL override
OllamaNumCtxEnvKey        = "OLLAMA_NUM_CTX"               // env key for context size override
OllamaDefaultBaseURL      = "http://localhost:11434"       // default local Ollama endpoint
OllamaDefaultModelID      = "ornith:9b"                    // default LLM model ID
OllamaDefaultNumCtx       = 8192                          // default context window size
DefaultDocsDir            = "wiki"                        // default documentation folder name
ConfigFileName            = ".code-reducer.yaml"          // persistent config filename
```

### Default Extraction Steps

`DefaultExtractionSteps` is a package-level `var` of type `[]ExtractionStep`, pre-populated with four entries:

| Index | Name | Purpose |
|---|---|---|
| 0 | `API_SIGNATURES` | Extracts public types, functions, methods and their signatures without explaining internal logic. |
| 1 | `BUSINESS_LOGIC` | Explains the primary domain problem solved by the code and lists high-level algorithm steps, ignoring implementation syntax. |
| 2 | `STATE_AND_CONCURRENCY` | Identifies mutable global/state variables and synchronization mechanisms; outputs `"No mutable state"` if entirely stateless. |
| 3 | `ERRORS_AND_SIDE_EFFECTS` | Details interactions with external systems (network, disk, databases) and how errors propagate or are swallowed. |

The slice is declared without mutex protection — no concurrency primitives exist in this file, and the variable is not modified after initialization.

---

## File I/O Operations (`io.go`)

### ConfigExists

```go
func ConfigExists(cwd string) bool
```

Performs `os.Stat()` on the resolved config path (computed by `getConfigPath`); returns `true` only when `.code-reducer.yaml` exists at that location. All OS-level failures — permission denied, inaccessible paths, I/O errors — are swallowed and reported as `false`. The caller has no way to distinguish absence from error.

### LoadConfig

```go
func LoadConfig(cwd string) (*Config, error)
```

Reads the config file into memory via `os.ReadFile`, unmarshals it with `yaml.Unmarshal` (from `gopkg.in/yaml.v3`), and populates a fresh `*Config`. On any failure — missing file, parse error, or I/O fault — returns `(nil, err)`. Parse errors are wrapped with the prefix `"failed to parse yaml config:"`.

### SaveConfig

```go
func SaveConfig(cwd string, cfg *Config) error
```

Implements atomic write semantics:

1. Marshal `cfg` via `yaml.Marshal`.
2. Apply formatting normalization — insert double newlines before specific prompt keys (`system_prompt`, `module_synthesis_prompt`, etc.) for consistent display.
3. Create a temp file in the same directory via `os.CreateTemp`.
4. Write content, sync to disk, close the descriptor, set permissions (`fileMode`), then rename the temp over the target path via `os.Rename`.

Each step's error is wrapped with a descriptive prefix (`"failed to create temp file:"`, `"failed to write config to temp file:"`, etc.). The deferred function closes the temp file and removes it; no panic recovery exists — any panic during execution or cleanup propagates unhandled.

### Non-Exported Helpers

| Function | Purpose |
|---|---|
| `getConfigPath(cwd string) string` | Builds absolute filesystem path from current working directory + configured filename. |
| `formatYAML(data []byte) string` | Applies double-newline normalization to prompt keys for consistent output formatting. |

Both are package-private; only referenced within the `config` package's internal implementation.

---

## Multi-Source Resolution (`resolve.go`)

### ResolveConfig

```go
func ResolveConfig(repoRoot string, modelIDFlag string, numCtxFlag string) (*Config, error)
```

Produces a single fully-resolved `*Config` by merging four sources in priority order: CLI flags > environment variables > YAML config file > hardcoded defaults. The returned struct is freshly allocated on each invocation — no shared instance exists within this function's scope.

**Algorithm:**

1. **Load YAML config.** Call `LoadConfig(repoRoot)`. If it returns `(nil, os.ErrNotExist)` — accept absence as valid and substitute an empty `Config{}`. Any other error (parse failure, I/O fault) is wrapped with `"failed to load configuration file:"` and returned.

2. **Resolve extraction steps.** If the loaded YAML omits `ExtractionSteps`, substitute the built-in default set (`DefaultExtractionSteps`). Otherwise use the YAML-provided list verbatim.

3. **Deduplicate ignore list.** Strip duplicate entries from the YAML's `Ignore` field.

4. **Per-field resolution (priority chain).** For each configurable field:
   - Start with the hardcoded system default.
   - Override if the YAML config provides a non-empty value.
   - Override further if an environment variable is set and valid (`os.Getenv` for `CodeReducerModelIDEnvKey`, `OllamaBaseURLEnvKey`, `OllamaNumCtxEnvKey`).
   - Override finally by the CLI flag argument passed to this function.

5. **Validate numeric inputs.** For `OllamaNumCtx`: reject values that fail `strconv.Atoi` parsing or are ≤ 0, returning an error with the offending key name and raw value embedded in the message (`"invalid value for %s: %s"`). If validation fails, return `(nil, err)` — no partial config is emitted.

6. **Return resolved config.** On successful resolution, emit a populated `*Config` struct with all fields merged; on any failure path, return `(nil, error)`.

**Error model:** All errors use Go's standard wrapping convention (`%w`) where applicable, preserving traceability for callers using `errors.Is`. No network I/O occurs within this file. No disk writes occur — only the external `LoadConfig` reads from disk.

---

## Error Model Summary

| Condition | Source Function | Behavior |
|---|---|---|
| File does not exist | `LoadConfig` → `ResolveConfig` | Accepted as valid; substitutes empty config. |
| YAML parse failure | `LoadConfig` → `ResolveConfig` | Wrapped with `"failed to parse yaml config:"`. |
| Config file I/O error | `LoadConfig` → `ResolveConfig` | Wrapped with `"failed to load configuration file:"`. |
| Invalid env/flag value for context size | `ResolveConfig` | Returns `(nil, err)` with key name and raw value in message. |
| Temp file creation failure | `SaveConfig` | Wrapped error returned; deferred cleanup runs anyway. |
| Config write failure | `SaveConfig` → caller | Wrapped error returned; no partial state visible to caller. |
| OS stat failure (existence check) | `ConfigExists` | Swallowed — returns `false`. Distinguishing absence from error is impossible. |

---

## State and Concurrency Analysis

No mutable state exists across the module's public surface:

- `DefaultExtractionSteps` is a package-level variable but is not modified after initialization; no synchronization mechanism protects it, though this is only observable within `config.go`.
- All function-local variables (`seen`, `result`, `cfg`, `resolved`) are scoped to their respective functions.
- No locks, mutexes, atomic types, async/await patterns, or channel-based coordination are used anywhere in the package.

---

## Unverifiable Elements from Test File

The test file (`config_test.go`) exercises `LoadConfig`, `SaveConfig`, and `ResolveConfig` against temporary directories created via `t.TempDir()`. It writes invalid YAML directly to disk and calls `LoadConfig` on it, confirming that parse errors propagate. Return types beyond nil/non-nil checks are not observable from test assertions alone — whether functions populate struct fields in addition to returning values is unverifiable without production source inspection.