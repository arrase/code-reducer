# `internal/config` Module

## Responsibility

Central configuration management for the Code Reducer pipeline. This module handles persistence of user-defined settings (`.code-reducer.yaml`), runtime overrides via environment variables, defaults, and CLI flag merging through a single resolved configuration object consumed by downstream components (model providers, tracing integrations, file-system traversal).

**Data Flow:**  
`ConfigExists(dir)` → `LoadConfig(dir)` → `ResolveConfig()` → `SaveConfig(Config)`. The resolve step merges four precedence layers: **system defaults** < **YAML config** < **environment variables** < **CLI flags**. The final resolved struct is the canonical configuration passed to every pipeline stage.

---

## Constants — Environment Variable Keys & Defaults

| Constant | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Runtime override of LLM model ID without file edit. |
| `OllamaBaseUrlEnvKey` | Custom Ollama API base URL (defaults to `http://localhost:11434`). |
| `OllamaNumCtxEnvKey` | Ollama context window size in tokens. |
| `LangsmithApiKeyEnvKey` | LangSmith tracing API key. |
| `LangchainProjectEnvKey` | LangChain project name. |
| `LangchainTracingEnvKey` | Toggle for LangChain v2 tracing. |
| `OllamaDefaultBaseURL` | Hardcoded fallback Ollama base URL (`http://localhost:11434`). |
| `OllamaDefaultNumCtx` | Hardcoded fallback context window size (8192 tokens). |
| `ConfigFileName` | Path suffix for the user config file (`.code-reducer.yaml`). |

---

## Types — Config Struct

```go
type Config struct {
    // Model configuration: provider, model ID, temperature, etc.
    Model   *ModelConfig     `yaml:"model,omitempty"`
    Tracing *TracingConfig   `yaml:"tracing,omitempty"`
    Ignore  []string         `yaml:"ignore,omitempty"`
    DocsDir string           `yaml:"docsDir,omitempty"`
}
```

`Config` is the sole struct persisted to disk. All fields are YAML-serializable; pointer semantics prevent zero-value marshaling for optional sections (model, tracing).

---

## Package-Level Defaults — Ignore Lists

| Variable | Purpose |
|---|---|
| `DefaultIgnores` | Directory names excluded from analysis by default (`.git`, `node_modules`, `dist`). |
| `DefaultIgnoredExtensions` | File extensions excluded from analysis by default (`.png`, `.zip`, `.pyc`). |

These lists are merged with user-supplied ignore entries during the resolve phase and applied to file-system traversal downstream.

---

## Functions — Config Validation & Persistence

### `ConfigExists(dir string) bool`

Returns whether a valid `.code-reducer.yaml` exists at `dir/ConfigFileName`. Does not read contents—only checks filesystem presence. Used by CLI entry points to prompt users for initial configuration on first run.

**Data Flow:**  
`dir` → OS.Stat(`dir + "/" + ConfigFileName`) → `true/false`.

---

### `LoadConfig(dir string) (Config, error)`

Reads raw YAML bytes from `dir/ConfigFileName`, unmarshals into a `Config` struct. Returns an error if the file is missing or malformed.

**Data Flow:**  
`dir + "/" + ConfigFileName` → OS.ReadFile → yaml.Unmarshal → `Config`. If unmarshal fails, returns zero-value `Config` and non-nil error.

---

### `SaveConfig(cfg Config) error`

Serializes a `Config` struct to YAML and writes it to the working directory's `.code-reducer.yaml` with **0600** permissions for security (read/write owner only).

**Data Flow:**  
`cfg` → yaml.Marshal → OS.OpenFile(`dir + "/" + ConfigFileName`, 0600) → OS.Write.

---

### `ResolveConfig() (Config, error)`

Merges four precedence layers into a single fully-resolved `Config`: **system defaults** (`OllamaDefault*` constants) < **YAML config** (from `LoadConfig`) < **environment variables** (`*_EnvKey` constants) < **CLI flags**. Returns the canonical configuration for pipeline consumption.

**Data Flow:**  
1. Load YAML from working directory via `LoadConfig`.
2. Read each `_*EnvKey` environment variable; apply overrides where set.
3. Apply CLI flag values (passed as function parameters).
4. Fill missing fields with defaults (`OllamaDefaultBaseURL`, `OllamaDefaultNumCtx`).
5. Merge `DefaultIgnores` and `DefaultIgnoredExtensions` into ignore lists.

The resolved struct is the authoritative configuration consumed by model providers, tracing backends, and file-system traversal logic downstream in the pipeline.