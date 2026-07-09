# `internal/config` — Configuration Resolution Pipeline

## Responsibility & Data Flow

This module implements a multi-source configuration resolution pipeline for Code Reducer, determining every operational parameter through a fixed precedence chain: **CLI flags → environment variables → YAML config → system defaults**. All settings are resolved into a single `*Config` value consumed by the rest of the application. The file enforces restricted filesystem permissions (`0600`) on writes and performs no network I/O.

---

## Struct Definitions

### `ExtractionStep`

Encodes a single analysis phase fed to an LLM during code extraction. Contains only metadata:

| Field | Type |
|---|---|
| `Name` | `string` |
| `Prompt` | `string` |

Used exclusively within the `ExtractionSteps` slice in `Config`.

### `Config`

The root configuration struct representing the fully resolved state. Fields:

| Field | Type | Description |
|---|---|---|
| `ModelID` | `string` | Ollama model identifier (highest priority) |
| `OllamaBaseURL` | `string` | Base URL for Ollama inference backend |
| `OllamaNumCtx` | `int` | Context window size; supports integer-from-env parsing and flag-based overrides with bounds checking (`> 0`) |
| `DocsDir` | `string` | Path to documentation directory; defaults to `"wiki"` if unset |
| `ExtractionSteps` | `[]ExtractionStep` | Ordered analysis pipeline phases |
| `Ignore` | `[]string` | Directory paths excluded from analysis |
| `IgnoreExtensions` | `[]string` | File extensions excluded from analysis |

---

## Public Functions

### `ConfigExists(cwd string) bool`

Probes whether the configuration file exists at `cwd`. Uses `os.Stat`; any non-nil error is treated as "file does not exist" and returns `false`. Swallowed errors are not surfaced.

### `LoadConfig(cwd string) (*Config, error)`

Reads `.code-reducer.yaml` from `cwd` via `os.ReadFile`. Returns the parsed configuration on success; if the file is absent or malformed, returns an error directly (unwrapped). If YAML is present but empty, returns an empty struct with nil error. File read errors are passed through unchanged.

### `SaveConfig(cwd string, cfg *Config) error`

Serializes a `*Config` to disk as YAML via `os.WriteFile` with mode `0600`. Marshaling and writing each wrap their respective errors with descriptive prefixes (`"failed to marshal yaml"`, `"failed to write config file"`). Returns nil on success.

### `MergeAndDeduplicate[T comparable](a []T, b []T) []T`

Concatenates two slices of the same type and removes duplicates while preserving merge order. Used for merging ignore paths/extension lists where defaults are merged before user values to prevent stale default entries from being silently dropped.

### `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config`

Orchestrates full configuration resolution. Order of operations:

1. Calls `LoadConfig(repoRoot)` — errors swallowed; empty `*Config{}` substituted
2. Merges `Ignore` and `IgnoreExtensions` with default sets + user values, deduplicating in merge order (defaults-first)
3. Selects extraction steps: uses user-defined if non-empty, otherwise falls back to `DefaultExtractionSteps`
4. Resolves Ollama parameters through priority chain (default → YAML → env var → flag), with type-safe parsing for numeric fields
5. Sets docs directory from config or default `"wiki"`

Local state is created fresh per invocation; no shared mutable state across goroutines.

---

## Business Logic & Domain Concepts

### Multi-Source Configuration Resolution

Every setting follows a fixed precedence order: CLI flags override environment variables, which override YAML config, which override system defaults. Later sources only take effect when explicitly provided by the user — silent degradation occurs if any source is missing or malformed.

### Extraction Pipeline Definition

`DefaultExtractionSteps` encodes four analysis phases in fixed order: API surface extraction → business logic interpretation → state/concurrency mapping → error/side-effect identification. The pipeline order is immutable; users can only extend the list, not reorder it.

### Deduplication Strategy

Ignore paths and ignore extensions merge defaults first, then user config appended. Duplicates are removed in merge order. This prevents stale default entries from being silently dropped when a user explicitly adds new entries to their config.

### Ollama LLM Backend Configuration

The tool assumes Ollama as its inference backend. Configuration is scoped: model ID (highest priority), base URL, and context window size. Context size supports both integer-from-env parsing and flag-based overrides with bounds checking (`> 0`).

### Default Ignore Set

A curated list of directories (.git, node_modules, dist, etc.) and file extensions (.png, .pdf, .zip, .lock files) is baked into the binary's defaults. The config schema supports extending this set without replacing it.

---

## High-Level Algorithmic Flow

1. **LoadConfig** reads `.code-reducer.yaml` from a working directory; missing or malformed returns error (or empty struct if YAML is absent)
2. **ResolveConfig** orchestrates full resolution: load user config (fall back to empty struct on any error), merge ignore paths and extensions with defaults + user values deduplicating, select extraction steps (user-defined if non-empty, otherwise `DefaultExtractionSteps`), resolve each Ollama parameter through priority chain (default → YAML → env var → flag) with type-safe parsing for numeric fields, set docs directory from config or default `"wiki"`
3. **SaveConfig** serializes a Config struct back to disk as YAML with restrictive permissions (`0600`)

---

## Key Design Choice

The resolution order is intentionally asymmetric: `DefaultIgnores` are merged before user config ignores, meaning defaults act as a baseline that users can extend but not strip. The same pattern applies to extensions and extraction steps (defaults-first merge). This preserves the tool's out-of-the-box behavior unless the user explicitly changes it.

---

## Mutable State

| Variable | Location | Mutability | Notes |
|---|---|---|---|
| `cfg` (local) | `ResolveConfig` | Per-call, not shared | Local to function scope; created fresh each invocation. Not shared across goroutines. |
| `resolved` (local) | `ResolveConfig` | Per-call, not shared | Local to function scope; built incrementally via field assignments. Not shared across goroutines. |

**Concurrency Mechanisms:** None. No mutexes, channels, or other synchronization primitives are used in this file.

---

## Side Effects & Error Handling Analysis

### Filesystem (Disk) Communication

| Function | Operation | Details |
|---|---|---|
| `ConfigExists` | Read-only probe via `os.Stat` | Returns `true` if file exists, `false` otherwise. Errors are swallowed — any error from `os.Stat` is treated as "file does not exist". |
| `LoadConfig` | Reads `.code-reducer.yaml` via `os.ReadFile` | Error returned directly (unwrapped). Caller receives raw OS-level error if file cannot be read. |
| `SaveConfig` | Writes to disk via `os.WriteFile` with mode `0600` | Two-step: marshals YAML first, then writes. Each step wraps its own error with a prefix (`"failed to marshal yaml"`, `"failed to write config file"`). Errors are wrapped but not panicked — returned to caller. |
| `ResolveConfig` | Indirectly reads disk via `LoadConfig(repoRoot)` | Errors from `LoadConfig` are swallowed. If the YAML file is missing or malformed, `cfg = &Config{}` is used instead. The error is lost; no warning, no sentinel — just silent fallback to empty config. |

### Error Handling Strategy

| Pattern | Where It Appears | Notes |
|---|---|---|
| **Direct pass-through** | `LoadConfig` (file read) | Raw OS error returned unchanged via `%w`. Caller sees the original error. |
| **Wrapped with prefix** | `SaveConfig` (marshal + write) | Both errors wrapped with descriptive `"failed to ..."` prefix using `%w`. Callers can unwrap. |
| **Swallowed / silent fallback** | `ResolveConfig` | `LoadConfig` error is discarded; empty `*Config{}` substituted. No sentinel, no panic — pure silent degradation. |
| **Silent bool conversion** | `ConfigExists` | Any non-nil error → `false`. File existence check that loses the reason it failed. |

### Notable Observations

- **No panics anywhere.** All errors are returned to callers (except where swallowed in `ResolveConfig`).
- **No network I/O from this file.** The Ollama URLs and model IDs stored here are configuration values; no HTTP calls originate from the package.
- **File write is restricted** — mode `0600` enforces owner-read/write only on disk.
- **YAML parse failure surfaces as a wrapped error**, but `ResolveConfig` ignores it entirely, meaning a broken config file will not prevent the program from running — it just uses zero-valued defaults.