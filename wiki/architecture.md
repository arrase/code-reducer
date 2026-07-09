# Global Architecture Overview

## System Boundaries

Code-Reducer is a **synchronous, file-system-bound documentation synthesis engine** for Go codebases. It operates entirely within a single process with no shared mutable state across goroutines. All external communication flows through an Ollama-compatible LLM client; all internal coordination uses OS-level primitives (lockfiles). The system has zero network I/O except for the LLM inference calls themselves, and zero database interactions.

### Process Model

The application boots as a single binary with no background services, daemonization, or persistent processes. Execution is driven by an event-loop pattern: CLI flags are parsed once, configuration is resolved in advance, then the pipeline runs to completion before exit. All state is local to each invocation; there is no process registry, no IPC, and no cluster coordination.

### External Communication Matrix

| Direction | Mechanism | Notes |
|---|---|---|
| Outbound network (LLM) | HTTP POST to `<baseURL>/api/chat` (`stream: false`) or `stream: true` via `bufio.NewReader` | 10-minute timeout; non-200 status returns formatted error with truncated body |
| Disk reads | `tools.ReadFileSafely(repoRoot, path)` | Relative to repo root; errors swallowed per-file during synthesis loop |
| Disk writes | `tools.WriteFileSafely(repoRoot, path, data)` | Used for architecture.md, quickstart.md, module docs, AGENTS.md. Errors wrapped or silently discarded depending on code path |
| In-memory regex/character scan | `json_parser.StripOuterMarkdownFence`, `CleanJSONResponse` | No I/O; strips markdown fences and cleans JSON responses from LLM output |

---

## Module Interaction Flow

### Phase 1: Bootstrap and Configuration

```
CLI invocation → flag parsing → mode selection (init/update) 
→ environment validation (git repo root, config file existence) 
→ merged configuration resolution (CLI flags → env vars → YAML config → system defaults)
→ signal handler installation for graceful shutdown
→ pipeline execution in event-loop mode → completion or failure exit
```

The entry point (`main.go`) contains no business logic. It delegates entirely to `cmd.RootCmd.Execute()`. If the command returns an error, the string representation is printed and the process exits with code 1.

### Phase 2: Configuration Resolution Pipeline

Configuration is resolved in a fixed precedence chain before any engine logic runs:

```
ResolveConfig(repoRoot, modelIdFlag, numCtxFlag) 
→ LoadConfig (reads .code-reducer.yaml) 
→ merge ignore paths/extensions with defaults + user values (defaults-first deduplication)
→ select extraction steps from user config or fall back to DefaultExtractionSteps
→ resolve Ollama parameters through priority chain (default → YAML → env var → flag)
→ set docs directory from config or default "wiki"
```

The `*Config` struct is the single source of truth consumed by downstream packages. All operational parameters (model ID, base URL, context window, extraction steps, ignore lists) resolve through this pipeline. Local state is created fresh per invocation; no shared mutable state exists in this package.

### Phase 3: Path Sandboxing and Mutual Exclusion

Before any file operations occur, the system acquires a process-level lock via `security.AcquireLock(repoRoot)`. This uses `O_EXCL` at the kernel level to guarantee atomic mutual exclusion—if another process holds an open writable descriptor to `.code-reducer.lock`, acquisition fails immediately. All subsequent filesystem writes are scoped to paths resolved against the repository root, with traversal attacks rejected by `SafeResolve`.

### Phase 4: Pipeline Execution

The core engine operates through a recursive chunked reduction model:

1. **Code discovery** — Scans repo root excluding ignored paths and git directories, groups results into a hierarchical `DirNode` tree
2. **Affected determination** — For each node in the tree, marks affected if any file appears in filtered changes or no markdown module exists at `<docsDir>/modules/<safe-filename>.md`, then propagates upward so parents of affected children become affected
3. **Module synthesis per directory** — Reads file contents, computes SHA256 hashes, checks cache hits; on miss, truncates content to ~75% of context window and runs extraction steps from `cfg.ExtractionSteps`; accumulates results into facts stored in both `cache.Files` (by path) and `cache.Modules` (by directory)
4. **Root summary synthesis** — Feeds all changed files into `reduceInChunks` to produce consolidated root summary, then generates architecture.md and quickstart.md via two successive LLM calls keyed by "architecture"
5. **Cache persistence** — Saves updated MetadataCache back to `<docsDir>/.metadata.json`

### Phase 5: Two Operational Modes

**Init mode:** Full documentation regeneration from scratch through all steps including global docs (architecture, quickstart) and per-module synthesis.

**Update mode:** Same setup phase through affected determination. If no affected directories exist, the pipeline short-circuits with success. If architecture/quickstart files already exist on disk and `affectedDirs["."]` is false, global documents are skipped entirely. Otherwise invokes `GenerateStandardDocs` to regenerate global docs (same LLM path as init), then proceeds with module synthesis for affected directories only.

---

## Concurrency Characteristics

No synchronization primitives (`sync.Mutex`, channels) exist anywhere in the codebase. All mutable state within individual packages is unprotected against concurrent access, but the system never runs multiple goroutines concurrently—each invocation is single-threaded from CLI entry through pipeline completion and exit. The process-level lockfile provides inter-process mutual exclusion only; intra-process coordination uses no shared state.

---

## Error Handling Patterns

| Pattern | Where | Behavior |
|---|---|---|
| Direct pass-through | `LoadConfig` (file read), recursive `reduceInChunks` results, pipeline execution via LLM client | Returned to caller as-is |
| Wrapped + returned | LLM calls in standard doc generation; file writes for architecture/quickstart/module docs; directory creation | Propagated up call chain with `%w` |
| Swallowed + logged warning | `.gitignore` load in setup; metadata cache load/save in teardown and update | Logged through event callback; execution continues |
| Silently discarded (`_ = err`) | AGENTS.md write path (both create and append branches) | Pipeline reports success regardless of write outcome |
| Sentinel-style fixed messages | `VerifyGitRepo` (git executable not found, not a git repo), `ShouldIgnoreFile` on internal failure | Replaces underlying error entirely or returns true/false without unwrapping |

No panics are raised anywhere in the visible code. Where errors could theoretically cause downstream issues (e.g., `chunkRes` used without nil-check after recursive call), no protection exists within this file's scope—this is an architectural choice, not a bug.