# Global Architecture Overview — Code-Reducer

## System Boundaries

Code-Reducer is a local-first AI agent that generates and maintains project wikis by analyzing source code against an Ollama LLM backend. The application runs in a single goroutine with no synchronization primitives; all concurrency concerns are absent from the design.

```
main.go ──► cmd.RootCmd.Execute()
    │
    ├── cmd/init.go          (engine.ModeInit)
    ├── cmd/update.go        (engine.ModeUpdate)
    └── cmd/setup.go         (interactive reconfiguration → config.SaveConfig)
    │
internal/config ─────────────► internal/engine ──► LLM client ──► synthesis artifacts
  │                                                                    │
  ▼                                                                    ▼
ResolveConfig                  Runner.Run(ctx, repoRoot, mode)        WriteFileSafely
  │                                                                    │
  ├── LoadConfig               security.AcquireLock                   │
  ├── SaveConfig                                                       ▼
  └── config.Config pointer      ──► internal/security ──► disk I/O / git subprocesses
                                                                      ▼
internal/tools ─────────────► LLM inference server (Ollama)
```

The four internal packages (`config`, `engine`, `security`, `tools`) form a shared execution path. The command layer delegates to the engine via unexported types; configuration resolution, synthesis pipeline orchestration, file I/O, Git validation, and security enforcement all flow through these internals before reaching external dependencies (Ollama HTTP endpoint, local filesystem).

## Module Interaction Map

```
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│  cmd/       │────►│ internal/    │────►│ internal/   │
│  (Cobra)    │     │ config      │     │ engine      │
└─────────────┘     └──────────────┘     └─────────────┘
       │                  │                    │
       ▼                  ▼                    ▼
┌─────────────┐     ┌──────────────┐     ┌─────────────┐
│ main.go     │     │ internal/   │     │ external    │
│ thin shell  │     │ security   │     │ Ollama      │
└─────────────┘     └──────────────┘     │ /api/chat  │
                                        └─────────────┘
```

## Entry Point (`main.go`)

`main()` delegates to `cmd.RootCmd.Execute()`. On error, the returned value is formatted via `fmt.Fprintf` and written to `os.Stderr`; the program exits with code 1. Nil returns from `Execute()` invoke the default Go exit behavior (code 0). No observable error path exists at this layer — all errors are swallowed into `Exit(1)`.

## Configuration Subsystem (`internal/config`)

Centralized configuration schema and persistence for the synthesis pipeline. Defines `Config` (runtime parameters: LLM identity, Ollama base URL/context size, docs directory, prompt templates, extraction steps, ignore lists) and `ExtractionStep` (single fact-extraction phase name + system prompt).

Three I/O functions manage `.code-reducer.yaml`:
- **`ConfigExists(cwd)`** — boolean check via single `os.Stat`.
- **`LoadConfig(cwd)`** — reads and parses YAML; returns `(nil, wrappedError)` on parse failure, raw error otherwise.
- **`SaveConfig(cwd, cfg)`** — atomic write pattern (temp file → sync → close → chmod 0600 → rename).

Resolution merges four precedence tiers: hardcoded defaults → YAML config → environment variables (`CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX`) → CLI flags. Missing files are silently replaced with an empty `Config{}` — no error surfaces for that case.

## Engine Subsystem (`internal/engine`)

Persistent, incremental documentation synthesis pipeline producing three artifact classes: global architecture summaries (`architecture.md`), quickstart guides (`quickstart.md`), and per-module API descriptions. Change detection limits regeneration to directories impacted by actual file modifications. State persists across invocations via a metadata cache stored as JSON under `docs/`; a global steps hash anchors cache validity to the current extraction pipeline configuration.

### Runner Entry Point

`Runner.Run(ctx, repoRoot, mode)` performs three operations before branching:
1. **Lockfile validation** — `security.EnsureGitignoreHasLockfile(repoRoot)`. Error logged as status event; execution proceeds regardless.
2. **Exclusive lock acquisition** — `security.AcquireLock(repoRoot)`. Deferred unlock runs on every exit path, guaranteeing release even when the pipeline fails partway through.
3. **Client instantiation** — constructs an Ollama HTTP client from config fields (no network I/O at this point).

After lock acquisition, execution branches on mode: invalid strings return `fmt.Errorf("unsupported mode: %s", mode)`. Otherwise either `orch.RunInit` or `orch.RunUpdate` is invoked directly.

### Orchestrator

Maintains mutable state across its lifecycle via pointers into shared structs (`cache`, `cfg`, `client`). No synchronization primitives protect access to these fields; concurrent callers sharing a single context instance would race on the cache maps. All operations are sequential and single-threaded per method invocation.

`GenerateStandardDocs` issues two sequential LLM calls — one for `architecture.md`, another for `quickstart.md`. If the top-level directory is marked affected or standard docs are missing, this function runs; otherwise it is skipped entirely on update.

### Tree Construction and Affected Detection

The orchestrator walks a nested `DirNode` hierarchy (files as leaves, intermediate path segments as child nodes) to mark directories as "affected" via four conditions:
1. Changed-file match within the subtree.
2. Module path non-existence on disk.
3. Cache emptiness for the corresponding entry.
4. Child propagation — any parent is affected whenever *any* child has been flagged, regardless of direct match.

### Chunking and Reduction Pipeline

`reduceWithLLM` is the entry point for all LLM-mediated reductions. `reduceInChunks` consolidates a directory's file facts and child summaries. `reduceFileFacts` consolidates per-extraction-step outputs for a single file. `reduceItems` implements an iterative map-reduce loop that accumulates items into batches within a character limit, flushing when the next item would exceed it. Single items exceeding the cap are split with overlap rather than truncated — information preservation over compression. If output ≥95% of input after each LLM pass, recursion terminates and results concatenate with `\n\n`.

`chunkTextWithOverlap` splits text into overlapping chunks (clamped if overlap ≥ max run length). Returns the full text as a single chunk when `maxRunes <= 0` or `overlapRunes >= maxRunes`. No panics, no bounds errors.

### Markdown Fence Stripping

Unexported; regex compiled once at package scope. Trims whitespace → matches triple-backtick fence (with optional language tag) → returns captured interior after trimming surrounding whitespace. If the pattern doesn't match, original input is returned unchanged.

## Synthesis Engine (`synthesize.go`)

Per `synthesizeNode`:
1. **Cache-first lookup** — checks precomputed SHA-256 hash; on match, reuses cached facts without re-reading.
2. **Recursive bottom-up traversal** — if unaffected and descendants are unaffected, returns cached summary immediately. Sorts child names alphabetically; recurses into children first; collects non-empty summaries into a map keyed by child name.
3. **Per-file extraction** — computes dynamic content limit from remaining context window capacity (~75% for file content). Splits large files with `chunkTextWithOverlap`. Passes each chunk to LLM with step-specific prompt derived from `ExtractionSteps`. Strips markdown fences via `stripOuterMarkdownFence`. Consolidates across steps using `reduceFileFacts`. Concatenates into a single file fact string.
4. **Assembly** — combines extracted file facts (prefixed `"### File: basename"`) and child summaries (prefixed `"### Subsystem: name"`). If nothing exists, clears the cache entry and returns empty.
5. **Final reduction and persistence** — passes all components to chunked reduction producing a single consolidated summary for the directory; stores in `cache.Modules[node.Path]`; writes to disk under `<docsDir>/modules/<safe-filename-of-path>`.

## Security Subsystem (`internal/security`)

Two isolated concerns: (1) safe resolution of user-supplied filesystem paths to prevent traversal escapes, and (2) file-based mutual exclusion ensuring only one instance holds a lock at any time. A third utility integrates the lockfile into version control so stale state is ignored by `git`.

### Sentinel Errors

Both are raw `*errors.errorString` values constructed with `errors.New`. Detected via `errors.Is()` or `errors.As()`:
- **`ErrPathTraversal`** — returned when a resolved path falls outside the repository root boundary.
- **`ErrLockHeld`** — returned when another code-reducer instance holds the file lock.

### Path Sanitization (`SafeResolve`)

Cleans an input path and ensures it lies strictly inside the repository root. Resolves symlinks on existing ancestor parts to block symlink-based traversal. Walks up from the target until hitting an existing physical ancestor (skipping non-existent intermediate components). Reconstructs the full resolved path from that ancestor plus skipped suffixes. Verifies final path lies strictly inside the repository root; rejects if not.

### Process Locking (`AcquireLock`)

Acquires `.code-reducer.lock` using O_EXCL for atomicity. On open failure, checks `os.IsExist(err)`. If the lock file already exists, returns a specific message instructing manual cleanup; otherwise wraps with path context and original error. Writes current PID to the lock file for identification/verification.

### Gitignore Integration (`EnsureGitignoreHasLockfile`)

Ensures `.code-reducer.lock` is present in `.gitignore`. Uses atomic write pattern (temp file → sync → chmod 0644 → rename). Non-not-exist read failures are wrapped with `"error reading .gitignore"` context.

## Tools Subsystem (`internal/tools`)

Two subsystems: repository-aware file I/O and discovery layer, plus Git command abstraction. Both operate on a `repoRoot` boundary to prevent path traversal outside the codebase. No mutable state exists; all operations are stateless per-call.

### File I/O

- **`ReadFileSafely(repoRoot, virtualPath)`** — resolves against `repoRoot` via security layer; propagates errors without wrapping.
- **`WriteFileSafely(repoRoot, virtualPath, content)`** — writes to in-directory temporary location, then atomically renames into place. Directories created as needed with default permissions before write begins. Deferred cleanup (`tmpFile.Close`, `os.Remove`) discards errors silently.
- **`LoadGitignore(repoRoot)`** — reads `.gitignore`; non-empty, non-comment lines collected as active patterns.
- **`ShouldIgnoreFile(relPath, gitIgnore)`** — determines if a path should be skipped based on gitignore patterns plus heuristic rules (hidden files starting with dot, paths ending with `.egg-info`).
- **`DiscoverCodeFiles(repoRoot, ignores)`** — walks repository root recursively; directories starting with dot or ending with `.egg-info` are skipped. Files passing ignore checks have relative paths normalized to forward slash and collected as results.

## External I/O Matrix

| Source | Path | Behavior on Failure |
|--------|------|---------------------|
| `config.LoadConfig` | `<cwd>/.code-reducer.yaml` | Wrapped with `"failed to parse yaml config: %w"`; raw read errors propagate unchanged |
| `config.SaveConfig` | `<cwd>/.code-reducer.yaml` | Individual write-path errors wrapped with unique prefix (`"failed to <action> temp config file"`) |
| `engine.LLM calls` | `{baseURL}/api/chat` | Wrapped via `%w`; descriptive message includes step/file context |
| `engine.Hash computation` | `<repoRoot>/<file>` | Logged as status warning; loop continues, file skipped from cache |
| `security.SafeResolve` | user-supplied path | Propagated directly without wrapping |
| `security.AcquireLock` | `<repoRoot>/.code-reducer.lock` | Sentinel errors returned; write failures wrapped with context |
| `tools.ReadFileSafely` | `<repoRoot>/<f>` | First call: logged as status warning, returns `"", nil`. Second call inside `if facts == ""`: same swallow pattern. |
| `tools.WriteFileSafely` | `<repoRoot>/<docsDir>/modules/<safe-filename>.md` | Wrapped with `%w`, includes node path in message |

## Constants Summary

| Constant | Value/Purpose | Notes |
|----------|---------------|-------|
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | Default Ollama inference server address |
| `OllamaDefaultModelID` | `"ornith:9b"` | Default LLM model identifier |
| `OllamaDefaultNumCtx` | `8192` | Default context size |
| `DefaultDocsDir` | `"wiki"` | Default documentation output directory |
| `ConfigFileName` | `".code-reducer.yaml"` | Config filename for all three I/O functions and resolution |
| `defaultHTTPTimeout` | 10-minute ceiling | Set at package init; referenced in engine client file |
| `maxErrorBodyBytes` | 1 KB cap | Cap on error response payloads from failed requests |
| `defaultChunkOverlap` | 800-token overlap | Between consecutive text chunks during processing |
| `minNumCtxFloor` | 512 | Minimum threshold for context length calculations |
| `contextWindowAllocRatio` | 0.75 | Reserved proportion of available space for primary content |

## Unresolved References

- **`defaultHTTPTimeout`** and **`maxErrorBodyBytes`** are referenced in the engine client file but not defined within it; they must be package-level constants declared elsewhere, otherwise compilation fails.
- **`llmCaller` interface** is unexported with methods `CallLLM(ctx, systemPrompt, messages, jsonFormat)` returning `(string, error)` and `NumCtx() int`.