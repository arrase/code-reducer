# `internal/config` Module Architecture

## Responsibility

This module defines the static schema, behavioral contracts, and runtime parameter resolution for an LLM-driven code documentation system called "Code-Reducer". It is not execution logic—it provides configuration inputs that other modules consume to drive a four-phase file analysis pipeline and higher-level document synthesis. Three files constitute the public surface:

| File | Role |
|---|---|
| `config.go` | Declares constants, struct types (`Config`, `ExtractionStep`), and package-level defaults for extraction prompts. |
| `io.go` | Implements file I/O for YAML persistence with atomic write semantics and permission enforcement. |
| `resolve.go` | Orchestrates configuration resolution across multiple sources using a priority chain. |

No runtime orchestration lives here. The module is the contract layer consumed by downstream pipeline runners and LLM clients.

---

## Constants (`config.go`)

All constants are immutable literals; none are reassigned within this file. No mutable state exists in any function scope across the entire `internal/config` package.

### Environment Variable Keys

| Constant | Type | Value |
|---|---|---|
| `CodeReducerModelIDEnvKey` | `string` | `"CODE_REDUCER_MODEL_ID"` |
| `OllamaBaseURLEnvKey` | `string` | `"OLLAMA_BASE_URL"` |
| `OllamaNumCtxEnvKey` | `string` | `"OLLAMA_NUM_CTX"` |

### Default Values

| Constant | Type | Value | Notes |
|---|---|---|---|
| `OllamaDefaultBaseURL` | `string` | `"http://localhost:11434"` | Local Ollama API default. |
| `OllamaDefaultModelID` | `string` | `"ornith:9b"` | Default inference model. |
| `OllamaDefaultNumCtx` | `int` | `8192` | Default context window size. |
| `DefaultDocsDir` | `string` | `"wiki"` | Output directory for generated docs. |
| `ConfigFileName` | `string` | `".code-reducer.yaml"` | Project-root config filename convention. |
| `configFilePerm` | `int` | `0600` | File permission constant; unexported. |

---

## Structs (`config.go`)

### `ExtractionStep`

```go
type ExtractionStep struct {
    Name   string // yaml:"name"
    Prompt string // yaml:"prompt"
}
```

- **Name** — `string` (yaml tag: `"name"`). Identifies the extraction phase.
- **Prompt** — `string` (yaml tag: `"prompt"`). Instruction fed to the LLM for this phase.

### `Config`

```go
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

YAML-mapped fields include model identity, API endpoint, context size, document output directory, system persona prompt, synthesis prompts (module, architecture, file consolidation), extraction steps slice, and ignore list. No field is modified within `config.go`.

---

## Default Extraction Steps (`config.go`)

### `DefaultExtractionSteps`

- **Type**: `[]ExtractionStep`
- **Value**: Pre-populated package-level variable with four entries:
  - `{Name: "API_SIGNATURES", Prompt: "Task: Extract the public surface area of the file. Output: A strict Markdown list of all exported structs, interfaces, and methods. For each, note the actual input and output types. Ignore internal logic."}`
  - `{Name: "BUSINESS_LOGIC", Prompt: "Task: Analyze the business logic and domain concepts. Output: Explain what business rules or domain concepts this file solves. Describe the high-level algorithmic flow. Ignore implementation details."}`
  - `{Name: "STATE_AND_CONCURRENCY", Prompt: "Task: Analyze state mutation and concurrency. Output: List all mutable state (global variables, changing struct fields) and what concurrency mechanisms (e.g., sync.Mutex, channels) protect them. If none, state 'No mutable state'."}`
  - `{Name: "ERRORS_AND_SIDE_EFFECTS", Prompt: "Task: Analyze side effects and error handling. Output: Detail how this code communicates with the outside world (I/O like network, disk, DB) and how it handles/returns errors (wrap, sentinel, panic)."}`

This slice is initialized once at package scope and not reassigned anywhere in `config.go`. It serves as the fallback when no YAML config specifies extraction steps.

---

## I/O Operations (`io.go`)

### Exported Functions

| Function | Signature | Notes |
|---|---|---|
| **`LoadConfig`** | `func LoadConfig(cwd string) (*Config, error)` | Reads `.code-reducer.yaml` from `cwd`; parses YAML; returns struct or error. |
| **`SaveConfig`** | `func SaveConfig(cwd string, cfg *Config) error` | Writes config via atomic rename pattern with permission enforcement. |
| **`ConfigExists`** | `func ConfigExists(cwd string) bool` | Checks filesystem state for config file presence. |

### Internal Functions (Not Part of Public Surface)

- `getConfigPath(string)` — lowercase initial, unexported; computes path via `filepath.Join`. No side effects.

---

## Configuration Resolution (`resolve.go`)

### Exported Function: `ResolveConfig`

```go
func ResolveConfig(repoRoot, modelIDFlag, numCtxFlag string) (*Config, error)
```

Standalone exported function that returns a fully-resolved `*Config`. Does not depend on `config.go` types being defined in this file—`Config` is referenced from elsewhere.

### Algorithmic Flow

#### 1. Load Raw Configuration

- Calls `LoadConfig(repoRoot)` to read YAML config file rooted at `repoRoot`.
- If the returned error satisfies `os.IsNotExist`, treat it as an empty configuration (zero-value struct). Other errors are propagated up with wrapping: `fmt.Errorf("failed to load configuration file: %w", err)`.

#### 2. Resolve Extraction Steps

- Uses configured steps if present in loaded config; otherwise falls back to package-level `DefaultExtractionSteps`.

#### 3. Resolve Configuration Values by Priority Chain

Each value is resolved independently using a source-priority chain where later sources override earlier ones:

| Value | Priority Order (highest wins) |
|---|---|
| Model ID | Default > YAML > Environment Variable (`CodeReducerModelIDEnvKey`) > CLI Flag (`modelIDFlag`) |
| Ollama Base URL | Default > YAML > Environment Variable (`OllamaBaseURLEnvKey`) *(no flag support)* |
| Ollama Context Size | Default > YAML > Environment Variable (`OllamaNumCtxEnvKey`) > CLI Flag (`numCtxFlag`) *(validated: must be positive integer; invalid input yields 0, which fails `n > 0` check and falls through to next source)* |
| DocsDir | Default > YAML |
| SystemPrompt, ModuleSynthesisPrompt, ArchitecturePrompt, FileFactConsolidationPrompt | Default > YAML *(no env or flag override)* |

#### 4. Return Resolved Config

- Returns the fully-resolved `*Config` and any error encountered during loading. Final return is `return resolved, nil`. No errors escape except the wrapped config-load failure described above.

---

## Error Handling Strategy (`io.go`)

### Explicit Errors Returned

| Function | Failure Path | Behavior |
|---|---|---|
| `getConfigPath` | Returns empty string (no error return path) | Caller must handle empty result. Pure string computation via `filepath.Join`. |
| `ConfigExists` | Returns `bool`; **swallows** the underlying `os.Stat` error—only reports success/failure, never returns the original error. | Stat error is dropped entirely. No way to distinguish "file not found" from other stat failures. |
| `LoadConfig` | Two distinct failure paths: <br>1. Raw `os.ReadFile` error → returned as-is (unwrapped).<br>2. YAML parse error → wrapped with `fmt.Errorf("failed to parse yaml config: %w", err)`. | ReadFile errors are **not** wrapped—caller sees the raw OS-level error from `io.ReadAll`/`OpenFile`. Parse errors get context added via `%w` wrapping (preserves root cause). |
| `SaveConfig` | Returns single `error`. Every failure point is wrapped with a descriptive prefix and `%w`:<br>- temp file creation<br>- write to temp file<br>- sync temp file<br>- close temp file<br>- chmod temp file<br>- rename temp → final config | All errors are **wrapped**, never returned raw. Root cause chain preserved via `%w`. No error swallowing inside this function. |

### Defer Cleanup in `SaveConfig`

`SaveConfig` defers `tmpFile.Close()` + `os.Remove(tmpName)`. If the function returns early due to an error, the temp file is still closed and removed from disk (best-effort cleanup).

---

## Side Effects Analysis (`io.go`)

### External Communication (I/O)

| Function | Disk/File I/O | Network/DB/etc | Notes |
|---|---|---|---|
| `getConfigPath` | None | None | Pure string computation via `filepath.Join`. No side effects. |
| `ConfigExists` | Reads disk (calls `os.Stat`) | None | Only reads filesystem state to check existence of `.code-reducer.yaml`. |
| `LoadConfig` | Reads disk (calls `os.ReadFile`) then parses YAML from memory | None | Two-step: raw read, then in-memory parse. No external calls between steps. |
| `SaveConfig` | Writes to disk via atomic rename pattern (`tmpFile.Write` → `Sync` → `Close` → `Rename`) | None | Creates temp file in same directory as target config. Persists after all writes + sync complete. |

**Atomicity note:** `SaveConfig` uses a write-to-temp-then-rename approach to avoid leaving partial files on disk. The rename is the final visible side effect that updates `.code-reducer.yaml`.

---

## Mutable State Analysis (All Files)

| Variable/Field | Type | Mutated? | Notes |
|---|---|---|---|
| `configFilePerm` | `int` | No — constant, immutable by definition. Held as string/integer literal and never reassigned. |
| `Config` struct fields | struct field declarations only | No — no instance is created or modified within `config.go`. |
| `DefaultExtractionSteps` | `[]ExtractionStep` (package-level) | No — initialized once at package scope with literal slice values and not reassigned anywhere in the file. |
| `resolved` (local `*Config` in resolve.go) | pointer to `Config` struct | Yes — fields reassigned inside the function body in priority order: defaults → YAML config (`cfg.*`) → environment variables (`os.Getenv`) → CLI flags. All writes are sequential within a single goroutine; no concurrent access visible here. |
| `resolved.Ignore` | `[]T` (slice) | Yes — populated via `mergeAndDeduplicate(cfg.Ignore, nil)`. Local slice, not shared with other goroutines in this file. |
| `resolved.ExtractionSteps` | `[]ExtractionStep` (inferred from usage) | Yes — set to `cfg.ExtractionSteps` or `DefaultExtractionSteps`. Local variable only; no concurrent access visible here. |

**Concurrency Mechanisms:** None detected (`sync.Mutex`, channels, `atomic.*`, goroutines, etc.). All state mutations occur within a single function call without any visible concurrency primitives across the entire package.