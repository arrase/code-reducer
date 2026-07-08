# internal/config — Configuration Management

## Responsibility

The `internal/config` module owns the entire configuration lifecycle for Code-Reducer: schema definition, filesystem persistence with restricted permissions, and multi-layer precedence resolution. It produces a single resolved `Config` instance consumed by the pipeline runner.

---

## Config Schema (`config.Config`)

```go
type Config struct {
    // Model ID — identifies the LLM backend (e.g., "codellama/CodeLlama-13b-Instruct")
    ModelID string
    // Ollama settings — base URL, API key, timeout, concurrency
    OllamaSettings OllamaSettings
    // Langchain tracing enabled flag
    LangchainTracing bool
    // Langsmith tracing enabled flag
    LangsmithTracing bool
    // Paths excluded from processing (glob patterns)
    IgnoredPaths []string
    // Documentation output directory
    DocsDir string
}
```

`Config` is the canonical schema for `.code-reducer.yaml`. All downstream consumers treat it as immutable once resolved.

---

## Filesystem Operations

### ConfigExists

**Purpose:** Pre-flight check before attempting to read configuration. Avoids unnecessary I/O by returning `false, nil` when absent and a non-empty error on filesystem-level failures (e.g., permission denied).

**Behavior:**
```go
func ConfigExists(path string) (bool, error) { ... }
```

Returns the boolean presence of `.code-reducer.yaml` at `path`. Used by CLI entrypoints to gate help messages and configuration prompts.

---

### LoadConfig

**Purpose:** Read raw YAML bytes from disk and unmarshal into a populated `Config`. Returns an empty `Config{}` with no error when the file is absent (caller must check `ConfigExists` first). On parse failure, returns the zero value with a non-nil error.

**Data flow:**
```
disk (.code-reducer.yaml) → io.ReadFile → yaml.Unmarshal → *config.Config
```

Unmarshaling uses reflection-safe YAML parsing that tolerates field order differences between schema versions; unknown fields are silently dropped to prevent breaking upgrades.

---

### SaveConfig

**Purpose:** Marshal a `Config` into YAML and persist it with mode `0600`. The restrictive permission prevents other local users from reading sensitive configuration (model IDs, API keys embedded in Ollama settings).

**Behavior:**
```go
func SaveConfig(cfg *config.Config, path string) error { ... }
```

Atomic write semantics: the file is created at mode 0644, then `chmod`'d to 0600. This avoids a race window where the intermediate world-readable state could be observed by concurrent processes. If the parent directory does not exist, the function returns a clear error rather than silently failing.

---

## Precedence Resolution (`ResolveConfig`)

**Purpose:** Produce a single resolved `Config` from four sources layered in strict precedence order: **system defaults → YAML file → environment variables → CLI flags**. The final value is used by every downstream component without any caller needing to know which layer supplied it.

### Precedence contract

| Layer | Override scope |
| --- | --- |
| System defaults | `Config` zero values (used when no file exists) |
| YAML file | Full struct override where fields are non-empty |
| Environment variables | Per-field env mapping (`CODE_REDUCER_MODEL_ID`, etc.) |
| CLI flags | Final override; wins over everything above |

### Implementation notes

- Each layer is a pure function returning `(partial *Config, error)`. The resolver composes them left-to-right.
- Partial configs are merged field-by-field with the higher-precedence layer winning on any non-zero value. This prevents partial overrides from corrupting defaults (e.g., an empty string in YAML does not reset a previously-set CLI flag).
- The resolver returns a copy of the resolved struct to prevent accidental mutation by callers.

### Failure modes

If the YAML parse fails during `LoadConfig`, the resolver treats it as "no config file present" and falls through to env/CLI layers only. This allows users to ship partial configurations without being blocked by malformed files.