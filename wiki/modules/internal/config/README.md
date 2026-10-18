# internal/config

## Module Responsibility

The `internal/config` package manages the configuration lifecycle for Code-Reducer, an AI-driven code analysis and documentation generation tool. It handles three concerns: (1) persistence of a `.code-reducer.yaml` configuration file, (2) resolution of configuration values from multiple sources with defined priority ordering, and (3) application of system defaults to empty or zero-valued fields.

**Data flow:** `ResolveConfig` is the primary entry point. It loads a base configuration from disk via `loadBaseConfig` (which delegates to `LoadConfig` in `io.go`), applies system defaults via `ApplyDefaults`, then resolves individual fields (`ModelID`, `OllamaBaseURL`, `OllamaNumCtx`) by merging CLI flags, environment variables, and config file values. The `Ignore` slice is deduplicated via `deduplicate`. The resolved `Config` struct is returned to callers.

---

## Configuration Persistence (`io.go`)

### Public API

| Function | Parameters | Return Types |
|---|---|---|
| `ConfigExists` | `cwd string` | `bool` |
| `LoadConfig` | `cwd string` | `*Config`, `error` |
| `SaveConfig` | `cwd string`, `cfg *Config` | `error` |

### Internal Functions

| Function | Parameters | Return Types |
|---|---|---|
| `getConfigPath` | `cwd string` | `string` |
| `formatYAML` | `data []byte` | `string` |

### File System Interactions

- **Disk reads:** `os.Stat` in `ConfigExists`; `os.ReadFile` in `LoadConfig`.
- **Disk writes:** `tools.WriteFileAtomic` in `SaveConfig`.
- No network, database, or API interactions.

### Error Propagation

- `LoadConfig` returns `(*Config, error)`. If `os.ReadFile` fails, the raw error is returned. If YAML parsing fails, the error is wrapped with `fmt.Errorf("failed to parse yaml config: %w", err)`.
- `SaveConfig` returns `error`. If `yaml.Marshal` fails, the error is wrapped with `fmt.Errorf("failed to marshal yaml: %w", err)` before being passed to `tools.WriteFileAtomic`.
- `ConfigExists` derives a boolean from `os.Stat` error check (`err == nil`).
- `getConfigPath` and `formatYAML` have no error handling; failures result in panics.

---

## Configuration Resolution (`resolve.go`)

### Public API

| Function | Parameters | Return Type |
|---|---|---|
| `ApplyDefaults` | `cfg *Config` | (none) |
| `ResolveConfig` | `repoRoot string`, `modelIDFlag string`, `numCtxFlag string` | `(*Config, error)` |

### Internal Functions

| Function | Parameters | Return Type |
|---|---|---|
| `deduplicate` | `a []string` | `[]string` |
| `loadBaseConfig` | `repoRoot string` | `(*Config, error)` |
| `resolveModelID` | `cfgVal string`, `flagVal string` | `string` |
| `resolveBaseURL` | `cfgVal string` | `string` |
| `parsePositiveInt` | `val string`, `errMsg string` | `(int, error)` |
| `resolveNumCtx` | `cfgVal int`, `flagVal string` | `(int, error)` |

### External I/O Interactions

- **Disk I/O:** `loadBaseConfig` calls `LoadConfig(repoRoot)` from `io.go`. If the file does not exist (`os.ErrNotExist`), an empty `Config{}` is returned instead of an error. Other read errors are wrapped and returned.
- **Environment Variables:** Three environment variables are read via `os.Getenv`:
  - `CodeReducerModelIDEnvKey` in `resolveModelID`
  - `OllamaBaseURLEnvKey` in `resolveBaseURL`
  - `OllamaNumCtxEnvKey` in `resolveNumCtx`
- No network, database, or API calls.

### Error Propagation

- `ResolveConfig` returns errors from `loadBaseConfig` (which wraps non-`os.ErrNotExist` errors from `LoadConfig`) and from `resolveNumCtx` (which wraps errors from `parsePositiveInt`).
- `ApplyDefaults` and `deduplicate` swallow all errors silently.
- `resolveModelID` and `resolveBaseURL` return values without errors; callers handle the empty-string case implicitly.
- `parsePositiveInt` returns a formatted error string via `fmt.Errorf`.
- Errors are propagated via Go's `error` return convention; no panics or exceptions are used.

---

## Configuration Schema (`config.go`)

### Constants

| Constant | Value |
|---|---|
| `ConfigFileName` | `.code-reducer.yaml` |
| `ConfigFileName` | `ConfigFileName` (referenced in `io.go` and `resolve.go`) |
| `configFilePerm` | `0600` |
| `configFilePerm` | `configFilePerm` (referenced in `io.go`) |
| `OllamaDefaultBaseURL` | `http://localhost:11434` |
| `OllamaDefaultModelID` | `ornith:9b` |
| `OllamaDefaultNumCtx` | `8192` |
| `DefaultDocsDir` | `wiki` |
| `CodeReducerModelIDEnvKey` | `CODE_REDUCER_MODEL_ID` |
| `OllamaBaseURLEnvKey` | `OLLAMA_BASE_URL` |
| `OllamaNumCtxEnvKey` | `OLLAMA_NUM_CTX` |

### Default Prompts

The following multi-line string constants define system prompts for downstream synthesis steps:

- `DefaultSystemPrompt` — Code-Reducer role and rules (extract facts, no speculation, no code blocks)
- `DefaultModuleSynthesisPrompt` — Folder summary generation
- `DefaultArchitecturePrompt` — Global architecture document generation
- `DefaultQuickstartPrompt` — Developer quickstart guide generation
- `DefaultFileFactConsolidationPrompt` — Fact consolidation and deduplication

### Structs

| Type | Fields |
|---|---|
| `ExtractionStep` | `Name` string, `Prompt` string |
| `Config` | `ModelID` string, `OllamaBaseURL` string, `OllamaNumCtx` int, `DocsDir` string, `SystemPrompt` string, `ModuleSynthesisPrompt` string, `ArchitecturePrompt` string, `QuickstartPrompt` string, `FileFactConsolidationPrompt` string, `ExtractionSteps` []ExtractionStep, `Ignore` []string |

### Default Extraction Steps

`DefaultExtractionSteps` is a `[]ExtractionStep` containing: `API_SIGNATURES`, `BUSINESS_LOGIC`, `STATE_AND_CONCURRENCY`, `ERRORS_AND_SIDE_EFFECTS`.

---

## Resolution Priority Ordering

The following priority ordering is enforced by `ResolveConfig` and its helpers:

| Field | Priority Order |
|---|---|
| `ModelID` | CLI flag → `CODE_REDUCER_MODEL_ID` env var → config file value |
| `OllamaBaseURL` | `OLLAMA_BASE_URL` env var → config file value |
| `OllamaNumCtx` | CLI flag → `OLLAMA_NUM_CTX` env var → config file value |
| `DocsDir` | System default (`wiki`) when empty/zero |
| `ExtractionSteps` | System default when empty |
| All prompt fields | System default when empty |

`DocsDir` is additionally trimmed and path-cleaned during default application.