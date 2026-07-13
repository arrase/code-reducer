# Module: internal/config — Configuration Layer for Code Reducer Synthesis Pipeline

## Responsibility

This module manages the lifecycle of project-level configuration for a multi-phase LLM analysis pipeline that generates technical documentation from source code. It defines three distinct concerns: (1) compile-time type and constant declarations, (2) file-system operations on `.code-reducer.yaml` with atomic write guarantees, and (3) multi-source value resolution across defaults, YAML, environment variables, and CLI flags.

## Data Flow

The module's data flow is sequential: `config.go` declares the schema (`Config`, `ExtractionStep`) and compile-time constants; `io.go` provides read/write primitives against a fixed path derived from `cwd`; `resolve.go` composes these into a resolved `*Config` by merging across four priority tiers. No state persists between calls within this module—each resolution produces a fresh `*Config` allocation whose fields are populated through the merge chain and returned to callers for consumption elsewhere in the application.

---

## Configuration Types and Defaults (`config.go`)

### Exported Structs

**`ExtractionStep`** — Carries per-phase identifiers:
- `Name string` — Phase identifier (e.g., `"API_SIGNATURES"`, `"BUSINESS_LOGIC"`).
- `Prompt string` — The prompt text handed to the LLM for that phase.

**`Config`** — Aggregate configuration container referenced by all exported functions:
- `ModelID string`
- `OllamaBaseURL string`
- `OllamaNumCtx int`
- `DocsDir string`
- `SystemPrompt string`, `ModuleSynthesisPrompt string`, `ArchitecturePrompt string`, `FileFactConsolidationPrompt string` — First-class prompt templates, all configurable via YAML.
- `ExtractionSteps []ExtractionStep`
- `Ignore []string`

### Exported Constants

Environment variable keys (`CodeReducerModelIDEnvKey`, `OllamaBaseURLEnvKey`, `OllamaNumCtxEnvKey`), default values for Ollama instance parameters (`OllamaDefaultBaseURL = "http://localhost:11434"`, `OllamaDefaultModelID = "ornith:9b"`, `OllamaDefaultNumCtx = 8192`), file-system defaults (`DefaultDocsDir = "wiki"`, `ConfigFileName = ".code-reducer.yaml"`), and multiline prompt templates are all package-level constants.

### Exported Variable

**`DefaultExtractionSteps`** — Pre-populated slice containing four named phases: `API_SIGNATURES`, `BUSINESS_LOGIC`, `STATE_AND_CONCURRENCY`, `ERRORS_AND_SIDE_EFFECTS`. Initialized at declaration only; no post-initialization mutations occur in this file.

### Non-Exported

Internal helper variable `configFilePerm` (value `0600`) is referenced by `io.go` but not declared here. No exported interfaces or methods exist on these types.

---

## Configuration File Lifecycle (`io.go`)

### Exported Functions

| Function | Input | Output |
|----------|-------|--------|
| `ConfigExists(cwd string)` | cwd directory path | `bool` |
| `LoadConfig(cwd string)` | cwd directory path | `*Config`, `error` |
| `SaveConfig(cwd string, cfg *Config)` | cwd, config pointer | `error` |

No exported structs or interfaces are defined in this file. All external type references (`Config`) come from the same package.

### Atomic Write Pattern for SaveConfig

The save operation does not write directly to the target path. The sequence is: marshal `cfg` into YAML bytes → apply formatting normalization (collapsing single blank lines to double blank lines before known section headers: `system_prompt`, `module_synthesis_prompt`, `architecture_prompt`, `file_fact_consolidation_prompt`, `extraction_steps`, `ignore`) → create a temp file matching `.tmp.*` in the same directory → write normalized YAML into it → sync and close → set permissions via `os.Chmod` → atomically rename temp over original. Readers never observe a partially-written config during save because the old file remains intact until the new one is fully ready to replace it.

### Load-Save Roundtrip Integrity

Loading preserves whatever formatting was originally written; normalization is applied only on write, not read. This decouples the in-memory representation from on-disk formatting expectations. `LoadConfig` returns a fresh `*Config` via unmarshaling with no shared state. Errors from `os.ReadFile` propagate directly; YAML parse failures are wrapped with a `"failed to parse yaml config"` prefix chained via `%w`; marshal failures similarly wrap a descriptive message.

### Error Propagation Strategy

- **Raw return**: When `os.ReadFile` fails in `LoadConfig`, the OS error is returned unwrapped.
- **Wrap with context (`fmt.Errorf(... %w)`)**: Parse and write failures across all save-path operations (temp create, write, sync, close, chmod, rename). All wrapped calls preserve chain via `%w`.
- **Error-as-boolean sentinel**: `ConfigExists` uses an `err == nil` check instead of returning the error. No wrapping; raw error is reused as a boolean condition.
- **Silent cleanup in defer**: A deferred function calls `tmpFile.Close()` and `os.Remove(tmpName)`. Errors from these cleanup calls are swallowed (not returned). On success, `Close` runs twice on the temp file—Go allows this safely—and the defer's `Remove` returns an error that is silently swallowed because the original config now holds the renamed data.

### Disk Operations Summary

| Function | Disk Operations |
|---|---|
| `getConfigPath` (internal) | None; pure computation via `filepath.Join`. |
| `ConfigExists` | One read-only check via `os.Stat`. Error sentinel reused as boolean condition. |
| `LoadConfig` | Two disk reads: `os.ReadFile` then `yaml.Unmarshal`. Missing-file errors propagate raw; parse failures are wrapped. |
| `SaveConfig` | Multiple writes: `os.CreateTemp`, `tmpFile.Write`, `tmpFile.Sync`, `tmpFile.Close`, `os.Chmod`, `os.Rename`. Each step returns its own error; all but the final rename are wrapped. |

No network I/O anywhere in this module. No database operations referenced.

---

## Multi-Source Configuration Resolution (`resolve.go`)

### Exported Functions

| Function | Input Types | Output Types |
|----------|-------------|--------------|
| `ResolveConfig(repoRoot, modelIDFlag, numCtxFlag string)` | `(string, string, string)` | `(*config.Config, error)` |

Internal helper `mergeAndDeduplicate` is lowercase and not part of the public surface. Only usage (not definition) of package-level types (`Config`, `OllamaDefaultModelID`, `CodeReducerModelIDEnvKey`, `DefaultExtractionSteps`) is visible here.

### Resolution Priority System

Each configurable field follows its own independent priority chain, lowest to highest: **defaults → YAML config file → environment variables → CLI flags**. The merge-and-deduplicate logic operates on local function scopes with no cross-goroutine visibility and no shared mutable state.

### Field-Specific Priority Chains

| Field | Priority Chain |
|-------|---------------|
| `ModelID` | Default > YAML > Env Var > CLI Flag |
| `OllamaBaseURL` | Default > YAML > Env Var |
| `OllamaNumCtx` (context size) | Default > YAML > Env Var > CLI Flag |
| `DocsDir` | Default > YAML |
| System prompts (`SystemPrompt`, `ModuleSynthesisPrompt`, `ArchitecturePrompt`, `FileFactConsolidationPrompt`) | All start with default, then override with YAML if non-empty |

### Validation Rules

- **Numeric fields**: CLI flag and env var values are parsed as integers; a value must be greater than zero to take effect. Invalid or non-positive values fall through silently.
- **String fields**: Empty string is treated as "not set" — the lower-priority default remains active.
- **Config file absence** (`config.yml`): Not an error. If missing, an empty `Config{}` struct is created and defaults are applied. Other parse errors are surfaced.

### Algorithmic Flow for ResolveConfig

1. Load YAML config — if it exists and parses successfully; otherwise start with an empty struct (missing-file case).
2. Resolve extraction steps — use YAML value if present, else fall back to `DefaultExtractionSteps`.
3. Deduplicate ignore list from the loaded config using a map-based seen-set while preserving first-seen order.
4. For each field, apply its specific priority chain: start with built-in default constant → override with YAML (if non-empty) → override with environment variable (if set and valid for numeric types) → override with CLI flag (highest priority; numeric types require successful integer parse and positive value).
5. Return the fully resolved `Config` struct, or an error if the YAML file fails to parse (excluding missing-file case).

### Error Handling in ResolveConfig

- **Swallowed path**: When `os.IsNotExist(err)` is true during config loading, defaults apply with no error returned.
- **Propagated path**: Any other error (parse failure, permission denied) is wrapped with a prefix and returned as `(*Config, nil, error)` — the config pointer is `nil`.
- **Silent branches**: String comparisons (`!= ""`, `> 0`) short-circuit without errors. `strconv.Atoi` failures are caught inline; env/flag values simply not used. Map lookups for deduplication have no error path.

### Always-On Return

The function always returns a non-nil tuple `(*Config, error)` — either a populated config with `nil` error, or a `nil` config with an error string. No panics anywhere in this file.

---

## Concurrency and Mutability Summary

Across all three files: no `sync.Mutex`, channels, atomics, or other synchronization primitives appear. No package-level variables are modified after initialization. All functions operate on caller-provided arguments and return results. The module is designed for single-call composition with no shared mutable state requiring protection.