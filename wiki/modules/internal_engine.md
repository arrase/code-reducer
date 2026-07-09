# Architecture: `internal/engine` Package

## Module Responsibility

The `internal/engine` package implements an LLM-driven documentation generation pipeline for code repositories. It discovers source files under a repository root, computes integrity hashes, determines which modules are affected by changes, synthesizes summaries per module through recursive chunked reduction, and persists results to disk. Two operational modes exist: **init** (full documentation regeneration) and **update** (incremental re-documentation of only changed modules). All external communication flows through a custom Ollama-compatible LLM client; all internal state is unprotected against concurrent access.

---

## Data Structures

### `FileChange` (`tree.go`)

```go
type FileChange struct {
    Path   string // file path relative to repo root
    Status string // "Added", "Modified", or "Deleted"
}
```

Represents a single detected change in the repository. Used as input to impact analysis and tree construction.

### `DirNode` (`tree.go`)

```go
type DirNode struct {
    Path     string   // absolute directory path
    Files    []string // list of file paths within this directory
    Children map[string]*DirNode // subdirectories keyed by name
}
```

Hierarchical representation of the repository's directory layout. Constructed from a flat slice of `FileChange` entries via `buildTree`. Leaf nodes store files; intermediate nodes are created implicitly during traversal.

### `MetadataCache` (`cache.go`)

```go
type MetadataCache struct {
    LastDocumentedCommit string              // commit SHA, or empty if no documentation has been performed
    Files                map[string]FileCacheEntry  // file path → cached integrity entry
    Modules              map[string]string          // module directory path → synthesized summary
}
```

Persistent state stored at `<docsDir>/.metadata.json`. Tracks per-file SHA256 hashes paired with LLM-extracted facts, and per-module synthesis results. The `Files` and `Modules` maps are re-initialized to empty maps if unmarshaled as `null`, preventing nil-map panics on subsequent operations.

### `FileCacheEntry` (`cache.go`)

```go
type FileCacheEntry struct {
    SHA256 string // content hash for integrity verification
    Facts  string // concatenated LLM-extracted metadata
}
```

Keyed by full file path. Stores the result of reading a file's bytes and hashing them, paired with whatever facts were extracted via the multi-step extraction pipeline.

### `Event` (`orchestrator.go`)

```go
type Event struct {
    Type    string // event type identifier
    Message string // associated payload
}
```

Observer-passed callback value used by `Runner.Run` and related functions to surface status updates without blocking return paths.

---

## Data Flow: Init Mode

1. **Lock acquisition** — `runner.go` calls `security.AcquireLock(repoRoot)`; deferred unlock on exit. Failure returns wrapped error immediately.
2. **Soft setup check** — `.gitignore` update for lockfile path attempted via `security.EnsureGitignoreHasLockfile`; failure logged as warning and execution continues.
3. **Pipeline setup** (`orchestrator.go`, internal `setupPipeline`) — loads `<docsDir>/.metadata.json` via `loadMetadataCache`; if absent, returns a freshly initialized empty cache. Loads `.gitignore` patterns for code-discovery filtering; errors swallowed with log warning.
4. **Code discovery and tree construction** (`orchestrator.go`, internal `discoverCodeFiles` + `buildTree`) — scans `<repoRoot>` excluding ignored paths and git directories, groups results into a `DirNode` tree. Errors from discovery propagate as-is to the caller.
5. **Affected determination** (`tree.go`, `determineAffected` / `propagateAffected`) — for each node in the tree: marks affected if any file appears in `filteredChanges`, if no markdown module exists at `<docsDir>/modules/<safe-filename>.md`, or if path is absent from `cache.Modules`. Then propagates upward so any parent of an affected child becomes affected.
6. **LLM client construction** (`runner.go`) — instantiates `*LLMClient` with configured model ID and base URL, 10-minute HTTP timeout.
7. **Root summary synthesis** — feeds all changed files into `reduceInChunks` (internal) to produce a consolidated root summary, then generates `architecture.md` and `quickstart.md` via two successive LLM calls keyed by `"architecture"`. Each call is wrapped with the system persona prompt injected at construction time.
8. **Module synthesis** (`synthesize.go`, internal `synthesizeNode`) — for each affected directory: reads file contents, computes SHA256, checks cache hit; if miss, truncates content to ~75% of context window and runs extraction steps from `cfg.ExtractionSteps`; accumulates results into facts; stores in both `cache.Files` (by path) and `cache.Modules` (by directory).
9. **AGENTS.md maintenance** — checks for existing file; if absent, writes fresh content with marker `"AI Agent Guidelines"`; otherwise appends conditionally. Write errors silently discarded (`_ = ...`).
10. **Cache persistence** (`orchestrator.go`, internal `teardownPipeline`) — saves updated `MetadataCache` back to `<docsDir>/.metadata.json`. Errors logged as warning and swallowed.

---

## Data Flow: Update Mode

Same setup phase through step 5 (affected determination). Key divergence begins at step 6:

- If no affected directories exist, pipeline short-circuits with success message; no docs regenerated.
- If `affectedDirs["."]` is false and both architecture/quickstart files exist on disk, global documents are skipped entirely (`RunUpdate` returns early).
- Otherwise invokes `GenerateStandardDocs` to regenerate global docs (same LLM path as init), then proceeds with module synthesis for affected directories only.

---

## External Communication Matrix

| Function | Direction | Mechanism | Notes |
|---|---|---|---|
| `client.CallLLM` | Outbound network | HTTP POST to `<baseURL>/api/chat`, `stream: false` | Returns single response; non-200 status returns formatted error with truncated body (1KB limit) |
| `client.StreamLLM` | Outbound network | HTTP POST, `stream: true` | Reads line-by-line via `bufio.NewReader`; invalid JSON lines silently skipped during parsing |
| `json_parser.StripOuterMarkdownFence` | In-memory regex | No I/O | Strips triple-backtick fences with optional language specifier; returns original input if no match |
| `json_parser.CleanJSONResponse` | In-memory character scan | No I/O | Locates `{`/`[` within cleaned text, tracks nesting respecting string literals and escapes |
| `json_parser.UnmarshalJSONResponse` | In-memory unmarshal | No I/O | Wraps `json.Unmarshal` error via `%w`; includes cleaned JSON snippet in message |
| File reads (orchestrator/synthesize) | Disk read | `tools.ReadFileSafely(repoRoot, path)` | Relative to repo root; errors swallowed per-file during synthesis loop |
| File writes (orchestrator/synthesize) | Disk write | `tools.WriteFileSafely(repoRoot, path, data)` | Used for architecture.md, quickstart.md, module docs, AGENTS.md. Errors wrapped or silently discarded depending on code path |

---

## Concurrency Characteristics

No synchronization primitives are present anywhere in the package. All mutable state (`MetadataCache.Files`, `MetadataCache.Modules`, `DirNode.Children`, `affectedDirs` map) is unprotected against concurrent access. The `Runner.Run` function acquires a repository-level lock via `security.AcquireLock` before executing pipeline logic, but this lock is external to the engine package and its internal behavior is not observable here.

---

## Error Handling Summary

| Pattern | Where | Behavior |
|---|---|---|
| Wrapped + returned | LLM calls in standard doc generation; file writes for architecture/quickstart/module docs; directory creation | `fmt.Errorf("...: %w", err)` propagated up call chain |
| Returned raw | Recursive `reduceInChunks` results; pipeline execution via LLM client | Direct pass-through, no wrapping |
| Swallowed + logged warning | `.gitignore` load in setup; metadata cache load/save in teardown and update | Logged through event callback; execution continues |
| Silently discarded (`_ = err`) | AGENTS.md write path (both create and append branches) | Pipeline reports success regardless of write outcome |
| Swallowed, no signal | `GetLastDocumentedCommit` on any failure from cache load | Returns `"", false`; caller cannot distinguish between "not yet documented" and "load failed" |

No panics are raised anywhere in the visible code. Where errors could theoretically cause downstream issues (e.g., `chunkRes` used without nil-check after recursive call), no protection exists within this file's scope.