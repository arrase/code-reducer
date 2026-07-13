# `internal/config` — Configuration Resolution & Persistence Module

## Responsibility

The `internal/config` package owns the lifecycle of a single configuration object (`*Config`) consumed by every downstream component in the system. It provides three operations:

1. **Resolve** — collapse four distinct input sources (CLI flags, environment variables, YAML file, built-in defaults) into one canonical `Config`.
2. **Persist** — read and write a YAML config file at the project root via atomic rename.
3. **Query** — inspect whether the config file exists on disk without loading its contents.

All resolution is deterministic: given identical inputs, the returned `*Config` is identical. No global mutable state exists within the package; the only shared state is a package-level variable holding default extraction steps (`DefaultExtractionSteps`).

---

## Constants

| Constant | Value / Purpose |
|---|---|
| `CodeReducerModelIDEnvKey` | Env key `"CODE_REDUCER_MODEL_ID"` used by `ResolveConfig` to read model ID overrides from the process environment. |
| `OllamaBaseURLEnvKey` | Env key `"OLLAMA_BASE_URL"` consumed by both `LoadConfig` and `ResolveConfig`. |
| `OllamaNumCtxEnvKey` | Env key `"OLLAMA_NUM_CTX"` consumed by `ResolveConfig`; value is parsed via `strconv.Atoi`. |
| `OllamaDefaultBaseURL` | Hard-coded fallback URL `"http://localhost:11434"`. |
| `OllamaDefaultModelID` | Hard-coded fallback model identifier `"ornith:9b"`. |
| `OllamaDefaultNumCtx` | Hard-coded fallback context size `8192`. |
| `DefaultDocsDir` | Hard-coded fallback docs directory name `"wiki"`. |
| `ConfigFileName` | Config file basename `".code-reducer.yaml"`. |
| `DefaultSystemPrompt`, `DefaultModuleSynthesisPrompt`, `DefaultArchitecturePrompt`, `DefaultFileFactConsolidationPrompt` | Hard-coded prompt texts used as the zero-order fallback for every prompt category in resolution. |

---

## Types: `Config` and `ExtractionStep`

```go
type ExtractionStep struct {
    Name   string  // yaml:"name"
    Prompt string  // yaml:"prompt"
}

type Config struct {
    ModelID                              string
    OllamaBaseURL                       string
    OllamaNumCtx                        int
    DocsDir                             string
    SystemPrompt                        string
    ModuleSynthesisPrompt               string
    ArchitecturePrompt                  string
    FileFactConsolidationPrompt         string
    ExtractionSteps                     []ExtractionStep
    Ignore                              []string
}
```

- `Config` is the sole configuration container. Every field maps to a distinct YAML key; all four prompt categories are independent strings with no cross-field dependencies.
- `ExtractionStep` is an opaque two-string struct used only inside the slice field of `Config`. The package-level variable `DefaultExtractionSteps` holds a static instance initialized at declaration and never mutated within this file; it serves as the zero-order extraction pipeline when the YAML file does not specify one.

---

## Types: `ExtractionStep` (definition)

- **Name** — human-readable label for the step, serialized under `"name"` in YAML.
- **Prompt** — prompt text injected into the LLM during that extraction phase, serialized under `"prompt"`.

---

## Variables: `DefaultExtractionSteps`

```go
var DefaultExtractionSteps []ExtractionStep
```

Package-level slice pre-populated with four entries covering API signatures, business logic, state and concurrency analysis, and errors/side effects. The variable is read-only within the package boundary; any modification must occur outside this file.

---

## Functions: Configuration Resolution (`internal/config/resolve.go`)

### `ResolveConfig(repoRoot string, modelIDFlag string, numCtxFlag string) (*Config, error)`

Merges CLI flags, environment variables, YAML config, and built-in defaults into a single resolved `*Config`. Returns a nil error on success; returns both a non-nil Config and an error only when the underlying file read fails with a condition that is not "file does not exist".

#### Parameters

| Parameter | Purpose |
|---|---|
| `repoRoot` | Repository root path. Used to derive the config file path for the disk-read step. |
| `modelIDFlag` | Explicit model ID from CLI; overrides environment and YAML only if non-empty. |
| `numCtxFlag` | Explicit context size from CLI; parsed as int with a minimum-of-1 validation gate before taking effect. |

#### Resolution Priority Chains

Every field follows an independent priority chain. The order is the same across all chains except where documented otherwise:

- **Model ID** — built-in default → YAML `model_id` → env `CODE_REDUCER_MODEL_ID` → CLI flag (skipped if empty).
- **Ollama Base URL** — built-in default → YAML `ollama_base_url` → env `OLLAMA_BASE_URL`. No CLI override.
- **Ollama Context Size** — built-in default → YAML `ollama_num_ctx` → env `OLLAMA_NUM_CTX` → CLI flag (parsed via `strconv.Atoi`, minimum 1). Env and CLI values that fail to parse are silently skipped; the previously-resolved fallback remains in effect.
- **DocsDir** — built-in default if unset, otherwise uses YAML-specified value. No env or CLI override path exists for this field.
- **Prompts** (system, synthesis, architecture, fact consolidation) — each has its own independent chain: built-in default → YAML override if non-empty. No CLI or env override is provided in this code.

#### Error Handling

| Condition | Behavior |
|---|---|
| `LoadConfig(repoRoot)` returns error + not-exist | Swallowed; replaced with an empty `&Config{}` and returned alongside a nil error. |
| `LoadConfig(repoRoot)` returns any other error | Wrapped as `"failed to load configuration file: %w"` and returned. |
| Integer parsing (`strconv.Atoi`) fails | Skipped; previous fallback retained. No error propagated. |

---

## Functions: Configuration Persistence (`internal/config/io.go`)

### `getConfigPath(cwd string) string`

Derives the absolute path to `.code-reducer.yaml` by combining the working directory with the fixed filename constant. Return value is a plain string; no error path exists in this signature.

### `ConfigExists(cwd string) bool`

Returns true if the config file is present at the derived path, false otherwise. Uses stat semantics internally (`os.Stat`). No error propagation — callers cannot distinguish "file does not exist" from other OS-level errors (e.g., permission denied).

### `LoadConfig(cwd string) (*Config, error)`

Reads raw bytes from disk at the resolved config path, then unmarshals them into a `*Config` struct via YAML parsing. Any parse failure is wrapped and returned as an error with descriptive prefix `"failed to parse yaml config: %w"`.

### `SaveConfig(cwd string, cfg *Config) error`

Persists `*cfg` back to disk using an atomic rename pattern:

1. Marshals the in-memory config to YAML.
2. Normalizes key prefixes by inserting extra blank lines before specific top-level keys (`system_prompt`, `module_synthesis_prompt`, `architecture_prompt`, `file_fact_consolidation_prompt`, `extraction_steps`, `ignore`) — purpose of this normalization is not documented within the file boundary.
3. Creates a temporary file in the same directory via `os.CreateTemp`.
4. Writes normalized YAML to the temp file, syncs, and closes it.
5. Sets restrictive permissions on the temp file via `chmod`.
6. Renames the temp file over the original config path atomically.

Any failure at any step (read, parse, write, sync, close, chmod, rename) is wrapped with a descriptive prefix (`"failed to create temp file:"`, etc.) and returned as an error. Cleanup of the temp file runs in a defer block so removal happens even when errors occur mid-save. A redundant explicit `Close()` call follows the deferred one; closing an already-closed file returns nil, so this is non-fatal but unnecessary.

---

## Data Flow Summary

```
CLI flags (modelIDFlag, numCtxFlag) ─┐
                                      ├──> ResolveConfig()
Env vars (CODE_REDUCER_MODEL_ID,     │   ┌─────────────┐
OLLAMA_BASE_URL, OLLAMA_NUM_CTX)    ─┼──►│ LoadConfig  │─► Config
YAML file (.code-reducer.yaml)      └──>│ getConfigPath│
                                        └─────────────┘

Resolved *Config ──► SaveConfig() ──► disk: .code-reducer.yaml
```

The `*Config` returned by `ResolveConfig` is the single source of truth for every downstream component. All other functions in the package either read from or write to this same object — there is no alternative configuration path within the module boundary.