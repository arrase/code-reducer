# internal/config — Multi-Source Configuration Resolution Module

## Responsibility & Data Flow

The module centralizes all configuration state for the code-reducer engine: loading, persisting, resolving, and validating a `Config` struct from CLI flags, environment variables, YAML files, or hardcoded defaults in a strict four-tier priority chain. Persisted state lives at `.code-reducer.yaml` in the current working directory with restricted owner-only permissions (`0600`).

---

## Data Structures

### Config

```go
type Config struct {
    ModelID            string   // Ollama model identifier (falls back to gemma4:26b-a4b-it-qat)
    OllamaBaseURL      string   // Base URL for the Ollama API endpoint
    OllamaNumCtx       int      // Context window size
    DocsDir            string   // Path to source documentation directory
    ExtractionSteps    []ExtractionStep  // Ordered pipeline of analysis tasks
    Ignore             []string            // Directories to exclude from scanning
    IgnoreExtensions   []string            // File extensions to skip
}
```

### ExtractionStep

```go
type ExtractionStep struct {
    Name   string `yaml:"name"`
    Prompt string `yaml:"prompt"`
}
```

Four extraction steps are defined by default:

| Priority | Step | Purpose |
|---|---|---|
| 1 | API_SIGNATURES | Extract exported structs, interfaces, methods and their types |
| 2 | BUSINESS_LOGIC | Analyze domain concepts and high-level algorithmic flow |
| 3 | STATE_AND_CONCURRENCY | Identify mutable state and concurrency protections (mutexes, channels) |
| 4 | ERRORS_AND_SIDE_EFFECTS | Document external communication (I/O) and error handling patterns |

The step list is overridable in the YAML file; if empty at resolve time it falls back to defaults.

---

## Functions

### ConfigExists(cwd string) bool

Returns `true` when `.code-reducer.yaml` exists under `cwd`. Treats any `os.Stat` error as "file does not exist" — permission denial and I/O failures are silently swallowed.

### LoadConfig(cwd string) (*Config, error)

Reads the persisted config from `<cwd>/.code-reducer.yaml`. Returns a nil pointer with an unwrapped `os.ReadFile` error if the file cannot be read at all. If YAML parsing fails, returns a wrapped error (`failed to parse yaml config`). On success, returns the raw parsed struct without applying overrides — callers must chain this into `ResolveConfig` for final resolution.

### SaveConfig(cwd string, cfg *Config) error

Marshals the provided `cfg` pointer and writes it to `<cwd>/.code-reducer.yaml` with `0600` permissions via `os.WriteFile`. Marshaling errors and write errors are both wrapped with `%w`, preserving the chain for unwrapping. A nil config is not explicitly guarded; marshaling a nil will result in an empty YAML document.

### MergeAndDeduplicate(a []T, b []T) []T (where T is comparable)

Merges two slices into one deduplicated slice using a map-based seen-set pattern. System defaults are merged first, then user-provided config additions follow — this ordering ensures user values take precedence when duplicates exist across the merge boundary. No error return; any panic from non-comparable types would be a caller-side bug.

### ResolveConfig(repoRoot, modelIdFlag string, numCtxFlag string) *Config

Orchestrates the full resolution pipeline:

1. Calls `LoadConfig` to read persisted state (errors swallowed — falls back to empty struct).
2. Applies CLI flag overrides in order: `modelIdFlag` → `OllamaBaseURL`, then `numCtxFlag` → `OllamaNumCtx`.
3. Merges default ignore lists (`DefaultIgnores`, `DefaultIgnoredExtensions`) with user-provided values via `MergeAndDeduplicate`.
4. Falls back to hardcoded defaults for extraction steps if the slice is empty.
5. Returns a pointer to the final resolved config; no error return — any failure path results in an empty `&Config{}`.

---

## Module Constants & Defaults (package-level)

| Variable | Purpose | Reassignable? |
|---|---|---|
| `DefaultIgnores` | Hardcoded directories to skip (`.git`, `node_modules`, `venv`, etc.) | No — read-only after init |
| `DefaultIgnoredExtensions` | Hardcoded file extensions to skip (`.pyc`, `.class`, etc.) | No — read-only after init |
| `DefaultExtractionSteps` | Ordered list of default analysis tasks | No — read-only after init |

These are initialized once at package scope and consumed exclusively by `ResolveConfig`.