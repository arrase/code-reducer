# `internal/config` — Package Documentation

## Responsibility and Data Flow

The package implements a **multi-layer configuration resolution** pipeline for the code analysis tool. Configuration values flow through four precedence tiers: hard-coded defaults → YAML file (`~/.config/code-reducer.yaml`) → environment variables → CLI flags. Each layer overwrites prior values when present; missing layers are transparently skipped.

Three properties are resolved independently: `ModelID`, `OllamaBaseURL`, and `OllamaNumCtx`. The package also structures the analysis pipeline into named **extraction steps** (`ExtractionStep`), each carrying a distinct prompt template. These steps can be customized per project via YAML or wholesale replaced by CLI flag.

All non-structural state (resolved config, local variables) is ephemeral—scoped to function calls and returned as values. No package-level mutable state survives beyond initialization. Concurrency primitives are absent; the package is single-threaded in its design.

## Types

### `ExtractionStep`

```go
type ExtractionStep struct {
    Name   string // step identifier (e.g., "api-signatures", "business-logic")
    Prompt string // prompt template applied to this phase during analysis
}
```

Instances are held in a package-level slice (`DefaultExtractionSteps`). The slice is immutable after declaration; callers receive copies or slices of it.

### `Config`

```go
type Config struct {
    ModelID              string            // resolved model identifier (last-wins: default → YAML → env → flag)
    OllamaBaseURL        string            // resolved base URL (default → YAML → env; CLI flag omitted)
    OllamaNumCtx         int               // resolved context size, validated positive (default → YAML → env → flag)
    DocsDir              string            // path to the documents directory under analysis
    ExtractionSteps      []ExtractionStep  // ordered list of extraction phases
    Ignore               []string          // absolute paths excluded from traversal
    IgnoreExtensions     []string          // file extensions excluded from traversal
}
```

## Constants and Defaults

### Environment Variable Keys

| Constant | Value | Purpose |
|---|---|---|
| `CodeReducerModelIdEnvKey` | `"CODE_REDUCER_MODEL_ID"` | Overrides `Config.ModelID` when set. |
| `OllamaBaseUrlEnvKey` | `"OLLAMA_BASE_URL"` | Overrides `Config.OllamaBaseURL` when set. |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | Overrides `Config.OllamaNumCtx` when set; value must parse as a positive integer. |

### Default Values

| Constant | Value | Purpose |
|---|---|---|
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | Fallback base URL when no prior layer supplies one. |
| `OllamaDefaultModelID` | `"ornith:9b"` | Default model identifier. |
| `OllamaDefaultNumCtx` | `8192` | Default context size. |

### Configuration File

| Constant | Value | Purpose |
|---|---|---|
| `ConfigFileName` | `".code-reducer.yaml"` | Filename resolved relative to the current working directory by `getConfigPath`. |

## Package-Level Immutable Defaults

Three slices are declared at package init and never mutated:

```go
var DefaultIgnores           []string // system-supplied ignore paths
var DefaultIgnoredExtensions []string // system-supplied ignored extensions
var DefaultExtractionSteps   []ExtractionStep // ordered extraction phases
```

Callers may append to these slices externally, but the package itself never reassigns them. `SaveConfig` writes the current slice state back to disk; callers must mutate before save if they need custom defaults.

## Public API Surface

### Path Resolution and Existence Check

#### `getConfigPath(cwd string) (string, error)`

Constructs the full path to `.code-reducer.yaml` by joining `cwd` with `ConfigFileName`. Returns a non-nil error only on I/O failure during construction; normal cases return `nil`.

#### `ConfigExists(cwd string) bool`

Returns whether the configuration file exists at the resolved path. Internally calls `os.Stat`; all errors (permission denied, I/O failures, etc.) are swallowed and treated as "does not exist". The function returns no error.

### Configuration Loading

#### `LoadConfig(cwd string) (*Config, error)`

Reads and parses the YAML configuration file at the resolved path. Error handling:
- File-read failure (`os.ReadFile`) → returned to caller unchanged (pass-through).
- YAML parse failure → wrapped as `"failed to parse yaml config: %w"`.

Returns a non-nil `*Config` on success; the struct is initialized with zero values before parsing, so callers receive a populated object even when the file contains only valid empty mappings.

#### `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) (*Config, error)`

Top-level entry point for full configuration resolution. Algorithm:
1. Calls `LoadConfig` to read the YAML file. If the error is a non-existent-file sentinel (`os.IsNotExist(err)`), it swallows and initializes an empty `Config{}`; all other errors propagate wrapped as `"failed to load configuration file:"`.
2. Layers defaults (hard-coded constants) over the loaded config.
3. Layers environment variable overrides using `os.Getenv` for `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, and `OLLAMA_NUM_CTX`. Non-empty values are used; empty strings fall through. Env read errors are swallowed.
4. Applies CLI flags (`modelIdFlag`, `numCtxFlag`). The flag value is parsed via `strconv.Atoi`; only `err == nil && n > 0` is accepted—invalid or non-positive values cause the prior layer's value to be retained silently.

Returns a fully resolved `*Config`. Errors are either pass-through (from `LoadConfig`) or wrapped with `"failed to load configuration file:"`. Notable: if YAML is malformed, the parse error wraps and propagates; otherwise resolution always succeeds for missing files.

### Configuration Persistence

#### `SaveConfig(cwd string, cfg *Config) error`

Serializes the config struct back to disk using `os.WriteFile` with `0600` permissions. Error handling:
- Write success → returns `nil`.
- Parse failure (marshal YAML) → wrapped as `"failed to marshal yaml: %w"`.
- File-write failure → wrapped as `"failed to write config file: %w"`.

Writes directly (no temp-file + rename); a partial write can leave a truncated file on disk. Permission normalization is implicit—existing files with different permissions will fail the write.

## Utility Functions

### `MergeAndDeduplicate[T comparable](a, b []T) []T`

Generic slice merge that deduplicates entries. Merges `b` after `a`; duplicates are removed via a map lookup and never reported as errors. Returns a new slice; the original inputs remain unmodified. The function is safe for concurrent use (no shared mutable state).