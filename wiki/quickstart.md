# Quickstart — Code-Reducer

## What It Does

Code-Reducer is a **local-first** AI agent that generates and maintains project wikis from source code. For each repository it can produce:

- `architecture.md` — global architecture summary
- `quickstart.md` — quick-start guide
- Per-module markdown files under `<docsDir>/modules/<path>.md`

All output is written into a local directory (`wiki/` by default). The system does not push anything to remote services; the only network interaction it makes is an HTTP call to an Ollama-compatible inference server.

## System Boundary

```
┌─────────────────────────────────────────────┐
│  code-reducer binary (single goroutine)       │
│                                               │
│  main.go ─► cmd.RootCmd.Execute()             │
│          ├► cmd/init.go   (ModeInit)          │
│          ├► cmd/update.go  (ModeUpdate)       │
│          └► cmd/setup.go   (interactive reconfig) → config.SaveConfig
├─────────────────────────────────────────────┤
│                                               │
│  internal/config     internal/engine         │
│  (schema, persistence)    (orchestrator,      │
│                            LLM pipeline)       │
│          │                                      │
│          ▼                                      │
│  ResolveConfig ─► Loader ─► Runner.Run ──►   │
│                         │                      │
│                security.AcquireLock            │
│                security.EnsureGitignoreHasLockfile │
│                                               │
├─────────────────────────────────────────────┤
│                                               │
│  internal/security    internal/tools         │
│  (path sanitization,   (safe file I/O,       │
│   locking, gitignore)  git subprocesses)     │
│                                               │
└─────────────────────────────────────────────┘
```

**Key boundary rules**

- All filesystem paths are resolved against `repoRoot` through `security.SafeResolve`. Paths that escape the repository root produce a sentinel error (`ErrPathTraversal`) and abort.
- Writes use an atomic-temp-file + rename pattern so the on-disk state is always either old or new, never partial. Permissions: config file `.code-reducer.yaml` → `0600`; gitignore → `0644`.
- The binary runs single-threaded; there are no goroutines, mutexes, or channels inside the application itself.
- Configuration resolution is layered: hardcoded defaults → YAML file (`.code-reducer.yaml`) → environment variables → CLI flags. Missing config file is silently treated as an empty `Config{}`.

## Entry Point

`main()` delegates to `cmd.RootCmd.Execute()`. On error it writes the message to `os.Stderr` and calls `os.Exit(1)`. A nil return from `Execute()` results in exit code 0 (default Go behaviour).

### Subcommands

| Command | Behaviour |
|---------|-----------|
| `init` | Runs the synthesis pipeline once, producing initial wiki artifacts. |
| `update` | Re-runs the synthesis pipeline; only directories affected by recent file changes are regenerated. |
| `setup` | Interactive re-configuration flow that ends with `config.SaveConfig`. |

## Configuration

Configuration lives at `<repoRoot>/.code-reducer.yaml`. Filename, env keys, and defaults:

```yaml
# Defaults
CODE_REDUCER_MODEL_ID="ornith:9b"
OLLAMA_BASE_URL="http://localhost:11434"
OLLAMA_NUM_CTX=8192
DOCS_DIR="wiki"
```

### Resolution order (highest → lowest)

1. Hardcoded defaults (`OllamaDefaultBaseURL`, `OllamaDefaultModelID`, `OllamaDefaultNumCtx`, `DefaultDocsDir`)
2. Values parsed from `.code-reducer.yaml` at `repoRoot`
3. Environment variables: `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX`
4. CLI flags (`--modelID`, `--numCtx`)

Rules applied during resolution:

- **Ignore list**: deduplicated in-place; first occurrence wins.
- **ExtractionSteps**: if the YAML slice is empty or nil, it falls back to `DefaultExtractionSteps`.
- **Numeric fields** (`OllamaNumCtx`): parsed with `strconv.Atoi`; non-positive values are rejected and the prior tier's value is retained.
- **String prompts**: an empty YAML value falls back to its corresponding hardcoded default via `resolveString`.

### I/O functions in `internal/config`

| Function | Returns | Notes |
|----------|---------|-------|
| `ConfigExists(cwd)` | `bool` | Single `os.Stat`; no wrapping. |
| `LoadConfig(cwd)` | `(*Config, error)` | YAML parse errors wrapped with `"failed to parse yaml config: %w"`; raw OS read errors propagate unchanged. Missing file → `(nil, nil)`. |
| `SaveConfig(cwd, cfg)` | `error` | Atomic write pattern; permission `0600`; final state is old or new file only. |
| `ResolveConfig(repoRoot, modelIDFlag, numCtxFlag)` | `(*Config, error)` | Merges the four tiers described above. No runtime errors by design except when a CLI flag parses to a non-positive integer. |

## Engine Pipeline (`internal/engine`)

`Runner.Run(ctx, repoRoot, mode, onEvent)` is the central entry point for synthesis work:

1. **Lockfile validation** — `security.EnsureGitignoreHasLockfile(repoRoot)`. Errors are logged as status events; execution continues.
2. **Exclusive lock acquisition** — `security.AcquireLock(repoRoot)`. Returns a sentinel error if another process holds the lock; wrapped with `"failed to acquire repository lock: %w"`. A deferred unlock runs on every exit path, releasing the file regardless of success/failure.
3. **LLM client instantiation** — `newLLMClient(r.cfg.ModelID, r.cfg.OllamaBaseURL, r.cfg.OllamaNumCtx)`. No network I/O at this point; the HTTP client is reused across calls with a fixed timeout and base URL set at construction time.

After lock acquisition, execution branches on `mode`: invalid strings return `"unsupported mode: %s"`; otherwise either `orch.RunInit` or `orch.RunUpdate` is invoked directly.

### Orchestrator state

The orchestrator holds three pointers across its lifecycle:

| Field | Type | Mutability inside engine |
|-------|------|--------------------------|
| `cache` | `*MetadataCache` | Mutable — reassigned in `setupPipeline`, `teardownPipeline`, `RunInit`, `RunUpdate`. Fields `.StepsHash`, `.Files`, `.Modules` are updated across calls. |
| `cfg` | `*config.Config` | Read-only here; external code may mutate the pointed-to struct at runtime. |
| `client` | `llmCaller` interface | Initialized once, never reassigned within these methods. |

No synchronization primitives protect access to these fields. Concurrent callers sharing a single context instance would race on the cache maps. All operations are sequential and single-threaded per invocation.

### Affected detection (incremental mode)

The orchestrator walks a `DirNode` tree to determine which directories need regeneration:

1. **Changed-file match** — if any file change matches under this node's subtree, mark affected.
2. **Module path non-existence on disk** — skip silently when the stat error is not `os.ErrNotExist`.
3. **Cache emptiness** — if `cache.Modules` entry for a child is empty, propagate "affected" upward.
4. **Child propagation** — any parent whose *any* child is already flagged becomes affected itself.

### Synthesis algorithm (bottom-up)

For each directory node:

1. **Cache-first lookup** — check `cache.Files[f]` against precomputed SHA-256 hash; on match, reuse cached facts without re-reading the file.
2. **Recursive bottom-up traversal** — if unaffected and descendants are unaffected, return cached summary immediately. Children are sorted alphabetically; recurse first; collect non-empty summaries into a map keyed by child name.
3. **Per-file extraction** — compute dynamic content limit from remaining context window capacity (~75% reserved for file content). Split large files with `chunkTextWithOverlap`. Pass each chunk to the LLM with step-specific prompts derived from `ExtractionSteps`. Strip markdown fences via `stripOuterMarkdownFence`. Consolidate across steps using `reduceFileFacts`. Concatenate all outputs into one string.
4. **Assembly** — combine extracted file facts (prefixed `"### File: basename"`) and child summaries (prefixed `"### Subsystem: name"`). If nothing exists, clear the cache entry and return empty.
5. **Final reduction and persistence** — pass all components to `reduceInChunks`, producing a single consolidated summary for the directory; store in `cache.Modules[node.Path]`; write to disk under `<docsDir>/modules/<safe-filename-of-path>`.

### Error propagation (engine)

| Source | Pattern |
|--------|---------|
| LLM calls (`CallLLM`) | Wrapped with `%w`, descriptive message includes step/file context; propagated. |
| File writes (`WriteFileSafely`) | Wrapped with `%w`; propagated. |
| Hash computation per file (`computeSHA256`) | Logged as status warning; loop continues, file skipped from cache. Swallowed in both `RunInit` and `RunUpdate`. |
| Cache load/save | Logged as status event; function completes regardless of write success/failure. |
| `.gitignore` loading | Not an I/O failure signal when missing → `(nil, nil)`. Scanner errors returned as-is. |
| Module file cleanup (`os.Remove`) | Error ignored via `_ = os.Remove(...)`. No verification. |
| Directory creation (`os.MkdirAll`) | Wrapped with `%w`; propagated. |

### Chunking and reduction helpers

- **`reduceWithLLM`** — entry point for all LLM-mediated reductions; calls `c.CallLLM(ctx, sysPrompt, messages, false)` with a system prompt prepended to user messages. Errors wrapped via `fmt.Errorf("%s: %w", errMsg, err)`.
- **`reduceInChunks`** — consolidates a directory's file facts and child summaries using `cfg.SystemPrompt` and current extraction steps.
- **`reduceFileFacts`** — consolidates per-extraction-step outputs for a single file.
- **`reduceItems`** — iterative map-reduce loop: accumulates items into batches that fit within `maxChars`, flushing when the next item would exceed the limit. If a single item exceeds the cap at entry, it is split with overlap rather than truncated (information preservation over compression). After each LLM pass, total output run count vs input is computed; if output ≥95% of input (integer division), recursion terminates and results are concatenated with `\n\n`. Context cancellation returns early without partial results.
- **`chunkTextWithOverlap`** — splits text into overlapping chunks of at most `maxRunes` runes with `overlapRunes` rune overlap between adjacent chunks. Clamped if ≥ `maxRunes`. Returns the full text as a single chunk when `maxRunes <= 0` or `overlapRunes >= maxRunes`.

### Markdown fence stripping (`internal/engine/markdown.go`)

```go
var markdownFenceRe regexp.Regexp
func stripOuterMarkdownFence(content string) string
```

Unexported. The regex is compiled once at package scope and never reassigned — effectively read-only after compilation. Algorithm: trim whitespace → match triple-backtick fence (with optional `markdown`/`json` language tag) → return captured interior after trimming surrounding whitespace. No error return; if the pattern doesn't match, original input is returned unchanged.

## Security Subsystem (`internal/security`)

Two isolated concerns: path sanitization and file-based mutual exclusion. Sentinel errors from `errors.go`:

| Variable | Condition | Mechanism |
|---|-----------|-----------|
| `ErrPathTraversal` — `"security violation: path traversal detected"` | Resolved path falls outside repository root boundary | Standard Go error return value; not wrapped; no panic. Detected via `errors.Is()` / `errors.As()`. |
| `ErrLockHeld` — `"lock is already held by another process"` | Another code-reducer instance holds the lock file | Standard Go error return value; not wrapped; no panic. Detected via `errors.Is()` / `errors.As()`. |

### `SafeResolve(repoRoot, inputPath)`

Cleans an input path and ensures it lies strictly inside `repoRoot`:

1. Accept a `repoRoot` absolute reference and a relative or absolute `inputPath`.
2. Resolve symlinks on existing ancestor directory components so symlink-based traversal is blocked.
3. Walk up from the target until hitting an existing physical ancestor (skipping non-existent intermediate components).
4. Reconstruct the full resolved path from that ancestor plus the skipped suffixes.
5. Verify the final resolved path lies strictly inside `repoRoot`; reject if it does not.

Errors from `filepath.Abs`, `EvalSymlinks(absRoot)`, and `EvalSymlinks(current)` are wrapped with context and returned directly. The `os.Lstat` loop terminates on first success or when reaching the filesystem root (`parent == current`). Non-not-exist errors during stat are wrapped and returned immediately. If `filepath.Rel` returns an error OR the relative path starts with `".."`, a wrapped `ErrPathTraversal` is returned.

### `AcquireLock(repoRoot)` → `*SimpleLock`

1. Call `SafeResolve` to sanitize the path; errors propagate unchanged.
2. Open a new file with O_EXCL; failure indicates another process holds the lock or a stale file exists.
3. On open failure: check `os.IsExist(err)`. If the lock file already exists, return a specific message instructing manual cleanup; otherwise wrap with path context and original error.
4. Write the current PID to the lock file for identification/verification.
5. On write failure after successful open: clean up by calling `f.Close()` then `os.Remove`, then return the write error.

`Unlock()` releases the lock by closing the file and removing it. Idempotent and thread-safe. If close succeeds but remove fails, that error is returned; if close fails but remove also fails, only the close error is surfaced.

### `EnsureGitignoreHasLockfile(repoRoot)`

Ensures `.code-reducer.lock` is present in `.gitignore`. Uses atomic temp-file + rename to update `.gitignore`. SafeResolve errors propagate directly. Non-not-exist read failures are wrapped with `"error reading .gitignore"` context. All temp file operations return wrapped errors on failure. Final `os.Rename` error is returned if it fails.

## Tools Subsystem (`internal/tools`)

Two subsystems: repository-aware file I/O/discovery and Git command abstraction. Both operate on a `repoRoot` boundary to prevent path traversal outside the codebase. No mutable state exists in either subsystem; all operations are stateless per-call.

### File tools (`file_tools.go`)

| Function | Signature |
|----------|-----------|
| `ReadFileSafely(repoRoot, virtualPath)` | `([]byte, error)` |
| `WriteFileSafely(repoRoot, virtualPath, content []byte)` | `error` |
| `LoadGitignore(repoRoot)` | `([]string, error)` |
| `ShouldIgnoreFile(relPath, gitIgnore *ignore.GitIgnore)` | `bool` |
| `DiscoverCodeFiles(repoRoot, ignores []string)` | `([]string, error)` |

Data flow:

1. Safe path resolution via `security.SafeResolve`. Errors propagate directly to the caller without wrapping.
2. TOCTOU-safe writes (`WriteFileSafely`) write to an in-directory temporary location, then atomically rename into place. Directories are created as needed with default permissions before write begins. Deferred cleanup (`tmpFile.Close`, `os.Remove`) discards errors silently — no return value reflects cleanup failures.
3. Gitignore parsing reads `.gitignore` from `repoRoot`. Non-empty, non-comment lines are collected as active patterns.

## External I/O Matrix (summary)

| Source | Path / Endpoint | Behavior on Failure |
|--------|-----------------|---------------------|
| Config subsystem — disk | `<cwd>/.code-reducer.yaml` | Writes use temp + rename; permission `0600`. Reads return `(nil, nil)` when missing. |
| Config subsystem — env | `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX` | Read only; never written by this module. |
| Engine — network | `{baseURL}/api/chat` via HTTP POST | Non-OK status codes trigger a bounded read (`io.LimitReader(resp.Body, maxErrorBodyBytes)`); the resulting string is returned to caller instead of raising an error. |
| Engine — disk | `<repoRoot>/<docsDir>/modules/<safe-filename>.md` | Atomic write via `tools.WriteFileSafely`. |
| Security — disk | `<repoRoot>/.code-reducer.lock`, `<repoRoot>/.gitignore` | Lock file uses O_EXCL for atomicity. Gitignore updated atomically. |
| Tools — disk | Any path under `repoRoot` that passes ignore checks | Atomic write via `tools.WriteFileSafely`. |

## Notes for Developers

- All named constants in `internal/engine/constants.go` are unexported; the file contains no mutable state, no external I/O, and no error propagation.
- The engine has no synchronization primitives (mutex, atomic, channel) protecting access to its fields. Concurrent callers sharing a single context instance would race on the cache maps. All operations are sequential and single-threaded per method invocation.
- `internal/security` contains only `const` values and function-local variables; no package-level or class-level properties are modified.