# Architecture Overview — Code-Reducer

## System Boundary & Purpose

Code-Reducer is a CLI-driven documentation generation engine. It extracts architectural facts from source files and synthesizes them into Markdown documentation via LLM calls. The system operates on two modes: **init** (full rebuild) and **update** (incremental refresh). State between invocations persists through a JSON-backed metadata cache at `{docsDir}/.metadata.json`.

## Module Map & Responsibilities

| Package | Responsibility |
|---|---|
| `cmd/` | CLI entry point: Cobra command registration, pre-flight validation, configuration resolution, engine orchestration with signal handlers |
| `internal/config` | Configuration persistence and resolution from multiple sources (CLI flags > env vars > YAML file > hardcoded defaults) |
| `internal/engine` | Pipeline coordination: LLM interaction, per-file extraction → chunking → synthesis, persistent metadata cache, global doc generation |
| `internal/security` | Path-traversal prevention via path canonicalization, cross-process coordination via file-based locking (`.code-reducer.lock`), `.gitignore` hygiene for the lock artifact |
| `internal/tools` | Safe filesystem operations within a bounded repository root (`file_tools`) and git process orchestration (`git_tools`) |

## Data Flow

```
User invokes "code-reducer init|update"
        │
        ▼
cmd/ — Cobra CLI entry point, command registration, lifecycle management
        │
        ▼
engine.NewRunner().Run(callback)
        │
        ├──► tree.go (DirNode construction + change detection)
        ├──► synthesize.go (per-file extraction → chunking → synthesis)
        ├──► cache.go (persistent hash/facts/module summary store)
        └──► orchestrator.go (pipeline coordination, global doc generation)
```

## Inter-Module Dependencies

### `cmd/` → `internal/config`

The CLI entry point delegates configuration resolution to the config module. The root command registers persistent flags (`--model-id`, `--num-ctx`) and validates pre-flight conditions (working directory existence, git repository validity, `.code-reducer.yaml` file existence). After validation passes, the system queries the last documented commit SHA from git history and routes execution based on mode.

The setup flow in `cmd/setup.go` calls `config.LoadConfig`, which may fail silently (swallowed), then proceeds with prompts. On success, it calls `config.SaveConfig`. All config file I/O is mediated through this module.

### `cmd/` → `internal/engine`

After validation and mode routing, `engine.NewRunner` is created and invoked via `runner.Run(callback)`. The callback handles three event types (`"status"`, `"error"`, default): all print to stdout or stderr via `fmt` family — no state changes, no logging to file, no metrics emission.

### `cmd/` → `internal/security`

The runner acquires an exclusive repository lock via `security.AcquireLock(repoRoot)` before any work begins with a deferred unlock guaranteeing release. Acquisition failure wraps as `"failed to acquire repository lock: %w"` and returns immediately — short-circuits all downstream work (no LLM client creation, no pipeline execution).

### `internal/engine` → `internal/tools`

Engine functions rely on the tools package for safe filesystem operations:
- `tools.ReadFileSafely` is used by the cache module for SHA-256 computation and orchestrator writes to `{docsDir}/modules/<safe-filename-of-path>.md`.
- `tools.WriteFileSafely` handles writing `.gitignore`, lockfile, and global docs.
- `tools.VerifyGitRepo` (outbound call) validates git repository validity from the CLI pre-flight path.

### `internal/engine` → `internal/security`

The security module provides path-traversal prevention via `SafeResolve`. The engine's tree construction uses this to canonicalize paths. The lock acquisition in the runner depends on `AcquireLock` which opens `.code-reducer.lock` with O_EXCL semantics; if another process expects to observe a successful write before removal, there is no synchronization primitive governing that path.

### `internal/engine` → `internal/config` (read-back)

The engine's persistent metadata cache holds three maps: `Files` (keyed by file path → `FileCacheEntry{SHA256, Facts}`), `Modules` (keyed by directory path → synthesized summary string), and a scalar `LastDocumentedCommit`. The orchestrator reads from this cache during update runs to determine affected directories.

### `internal/config` → `external LLM endpoints`

The engine creates an `*LLMClient` via `NewLLMClient(modelID, baseURL, numCtx)` using resolved config values (model ID, Ollama base URL, context size). The HTTP client inside it carries a fixed 10-minute timeout; there is no way to swap or inject a custom one after construction.

## External Dependencies

| Dependency | Usage |
|---|---|
| `github.com/spf13/cobra` | CLI framework for command tree construction and flag parsing in `cmd/` |
| `gopkg.in/yaml.v3` | YAML configuration parsing for `.code-reducer.yaml` |
| `sabhiram/go-gitignore` | Gitignore rule compilation used by `internal/tools.ShouldIgnoreFile` |
| Ollama-style HTTP endpoints | LLM interaction through `*LLMClient` in `internal/engine` |

## Concurrency & Safety Model

No file in the engine module uses `sync.Mutex`, channels, atomic operations, or equivalent concurrency primitives. All state mutations occur on a single goroutine's call stack during recursion (`synthesizeNode`). The metadata cache is read/written without locking — race conditions exist but no protection mechanism is present.

All external I/O is filesystem-only in the security module. No network calls are observed anywhere except for LLM API interaction, which happens through the engine's `*LLMClient` with a fixed 10-minute timeout. Errors from that path are wrapped descriptively and propagated to the callback handler.

No panics occur: every function returns `(value, error)` or equivalent; no `panic`, `runtime.Goexit()`, or unhandled recover blocks are present in any of the examined files.

## State Persistence

State between invocations persists through a JSON-backed metadata cache at `{docsDir}/.metadata.json`. The orchestrator only refreshes global docs when affected directories exist OR either file is missing entirely; otherwise they remain intact without regeneration.