# Code-Reducer — Quickstart & Architecture Overview

## System Boundaries

Code-Reducer is an AI-driven code synthesis tool that scans Go repositories and produces structured Markdown documentation (wiki pages). The entire pipeline operates within the repository filesystem—no network or database interactions occur outside the Ollama HTTP transport layer used by `internal/engine`. All operations are scoped to paths resolved relative to a caller-supplied `repoRoot`.

## Module Interaction Map

```
OS env / CLI flags → cmd.ResolveConfig() → *config.Config
                    repoRoot           → security.SafeResolve() → absolute path
                    *config.Config     → engine.NewRunner(cfg)
                                        │
                                        ├─ Lock acquisition via security.AcquireLock(repoRoot)
                                        ├─ metadata cache load/save at <docsDir>/<metadataFileName>
                                        ├─ gitignore merge + code file discovery (tools.DiscoverCodeFiles)
                                        ├─ SHA256 hashing per discovered file
                                        ├─ DirNode tree construction → affected status propagation
                                        ├─ Map: extractFileFacts(ctx, LLM, chunked content) per extraction step
                                        ├─ Reduce: synthesizeNode(node *DirNode) bottom-up
                                        ├─ Standard docs generation (architecture + quickstart) via 2× LLM calls
                                        └─ metadata cache persist → completion event
```

## How Modules Interact

### Configuration Flow (`cmd` ↔ `internal/config`)

The CLI layer resolves configuration through a priority chain: CLI flags override environment variables, which override YAML config file contents, which override built-in defaults. The setup wizard bootstraps first-time configuration by collecting LLM pipeline parameters through guided stdin prompts and persists to `.code-reducer.yaml`. All operations are local to the filesystem; no network calls or database access occur in `cmd` or `internal/config`.

### Security Primitives (`internal/security`)

Two safety primitives protect the repository boundary:
- **Path sanitization** prevents directory escape during path resolution by verifying resolved targets lie strictly within the repo root.
- **Exclusive locking** coordinates concurrent access through an exclusive lockfile, preventing stale partial writes and detecting inter-process contention.

Both use atomic write-through-rename patterns so readers never observe partially-written state.

### Pipeline Orchestration (`internal/engine`)

The engine implements a Map-Reduce pipeline: source files are discovered (map), facts are extracted per-file via LLM calls using chunking with overlap and loop prevention, and module summaries are synthesized bottom-up through directory traversal (reduce). The orchestrator loads `.gitignore` patterns, merges them with user-supplied ignore lists, persists a metadata cache tracking per-file SHA256 hashes, and routes execution based on mode flag.

### Repository-Aware File Operations (`internal/tools`)

File operations resolve virtual/relative paths into absolute filesystem paths confined to `repoRoot`. Read/write/discover functions use safe resolution before any I/O occurs. Writes employ atomic rename patterns—creating parent directories, writing to a temporary file in the same directory, syncing and closing, then atomically renaming over the final target—to prevent partial-write corruption.

## Entry Points

### CLI Execution Path

```
executeCommand(mode engine.Mode) error // dispatches to engine runner with mode flag
```

The public execution path verifies Git repository presence, resolves configuration from disk or defaults, validates engine mode against project state (init requires uninitialized; update requires existing wiki— inconsistent modes fail fast), instantiates the runner, registers SIGINT/SIGTERM handlers on the background context, and invokes `Run(ctx, repoRoot, engine.Mode(mode), callback)` wrapping its return as `"documentation run failed: %w"`.

### Subcommand Registration

Both init and update follow an identical thin-wrapper pattern: define an unexported `*cobra.Command`, assign its `RunE` to a function that calls `executeCommand` with a mode constant, register under `RootCmd`. No mutable state; no synchronization. All I/O is downstream in `internal/engine`; not visible here.

## Concurrency Model

No synchronization mechanisms (`sync.Mutex`, `sync.RWMutex`, atomics, channels) are present in any file within `internal/engine`. All mutable state (cache maps, orchestrator struct fields) is accessed in single-threaded contexts only; thread safety cannot be verified from source alone and must be assumed caller-side responsible for serialization if used across goroutines. The repository-level lock in `Runner.Run` provides coarse-grained concurrency control but operates outside the engine's own fields.

## External I/O Summary

All interactions are local (disk + stdin/stderr). No network calls, database access, or API interactions occur in `cmd`, `internal/config`, or `internal/tools`. The Ollama HTTP transport layer used by `internal/engine` is the sole exception for LLM communication.