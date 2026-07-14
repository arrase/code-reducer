# internal/engine — AI-Driven Incremental Documentation Generation Pipeline

## Module Responsibility

The `internal/engine` package implements a persistent, incremental documentation synthesis pipeline for software repositories. It produces three artifact classes—global architecture summaries, quickstart guides, and per-module API descriptions—from source code using an LLM backend, with change detection that limits regeneration to directories impacted by actual file modifications. State persists across invocations via a metadata cache stored as a JSON file under the repository's `docs/` directory; a global steps hash anchors cache validity to the current extraction pipeline configuration.

## Data Flow

```
Runner.Run(ctx, repoRoot, mode) 
    └─► acquireLock(repoRoot)                    (security.AcquireLock)
        ├─► ModeInit ──► orchestrator.GenerateStandardDocs ──► architecture.md / quickstart.md
        └─► ModeUpdate ──► loadMetadataCache      ──► cache.Files, cache.Modules, cache.StepsHash
                           │
                           ├─► computeSHA256 × N  ──► precalculatedHashes (cache miss → disk read)
                           │
                           ├─► diff detection     ──► FileChange map {Path: Status}
                           │
                           ├─► rebuild tree       ──► DirNode hierarchy from current files only
                           │
                           ├─► prune stale modules (os.Stat + os.Remove)
                           │
                           ├─► determine affected dirs (walk tree + cache empty check + disk existence)
                           │
                           └─► selective regeneration ──► synthesizeNode × N ──► WriteFileSafely
```

---

## Runner Entry Point (`runner.go`)

### `Mode`

String-typed operation modes for the pipeline. Two exported constants exist:

| Constant | Value |
|----------|-------|
| `ModeInit`  | `"init"`   |
| `ModeUpdate` | `"update"` |

### `Runner`

Struct holding a single pointer field, `cfg *config.Config`, assigned once in the constructor and never reassigned within this file. External mutability of the pointed-to struct is not observable from here alone.

```go
func NewRunner(cfg *config.Config) *Runner
func (r *Runner) Run(ctx context.Context, repoRoot string, mode Mode, onEvent func(Event)) error
```

`Run()` performs three operations before branching:

1. **Lockfile validation** — `security.EnsureGitignoreHasLockfile(repoRoot)` is invoked; any error is logged as a status event and execution proceeds regardless.
2. **Exclusive lock acquisition** — `security.AcquireLock(repoRoot)` returns an error that is wrapped with context (`failed to acquire repository lock: %w`) and returned immediately. A deferred unlock runs on every exit path, guaranteeing release even when the pipeline fails partway through.
3. **Client instantiation** — `newLLMClient(r.cfg.ModelID, r.cfg.OllamaBaseURL, r.cfg.OllamaNumCtx)` constructs an Ollama HTTP client; no network I/O occurs at this point.

After lock acquisition, execution branches on the mode value: invalid strings return `fmt.Errorf("unsupported mode: %s", mode)`. Otherwise, either `orch.RunInit` or `orch.RunUpdate` is invoked directly and its error (or nil) is returned to the caller.

---

## Orchestrator (`orchestrator.go`)

### Types

```go
type EventType string
const EventStatus  EventType = "status"
const EventError   EventType = "error"

type Event struct {
    Type   EventType `json:"type"`
    Message string    `json:"message"`
}
```

Both constants and the type are unexported. The orchestrator itself is also an internal, unexported type; only its public method signatures appear in the package surface.

### `GenerateStandardDocs`

```go
func (o *orchestrator) GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum string, cfg *config.Config, logEvent LogEventFunc) error
```

Invoked from both `RunInit` and `RunUpdate`. It issues two sequential LLM calls: one to generate `architecture.md` and another for `quickstart.md`, each wrapped as a global artifact. Errors propagate fully (wrapped with `%w`). If the top-level directory is marked affected or standard docs are missing, this function runs; otherwise it is skipped entirely on update.

---

## Pipeline Context State (`orchestrator.go`)

The orchestrator maintains mutable state across its lifecycle via pointers into shared structs:

| Field | Type | Mutability | Notes |
|-------|------|------------|-------|
| `cache` | `*MetadataCache` | **Mutable** | Reassigned in `setupPipeline`, `teardownPipeline`, `RunInit`, `RunUpdate`. Fields `.StepsHash`, `.Files`, `.Modules` are updated across calls. |
| `cfg` | `*config.Config` | Potentially mutable | Read only here (`SystemPrompt`, `ArchitecturePrompt`, `ExtractionSteps`). External code may mutate the pointed-to struct at runtime. |
| `client` | `llmCaller` interface | Read-only in this file | Initialized on construction; never reassigned within these methods. |

No synchronization primitives (mutex, atomic, channel) protect access to any of these fields. Concurrent callers sharing a single context instance would race on the cache maps. All operations are sequential and single-threaded per method invocation.

---

## Tree Construction and Affected Detection (`tree.go`)

### Types

```go
type ChangeStatus string   // "Added" | "Modified" | "Deleted" (unexported)

type FileChange struct {
    Path  string
    Status ChangeStatus
}

type DirNode struct {
    Path     string
    Files    []string
    Children map[string]*DirNode
}
```

### `buildTree`

Constructs a nested `DirNode` hierarchy from a slice of file paths. Leaf entries are files; intermediate path segments become child nodes. No I/O, no side effects.

### Affected Detection Logic

The orchestrator walks the tree to mark directories as "affected" via four conditions:

1. **Changed-file match** — if any `FileChange` in the map matches a file under this node's subtree.
2. **Module path non-existence on disk** — `security.SafeResolve(repoRoot, modulePath)` followed by `os.Stat(absModulePath)`. If the stat error is not `os.ErrNotExist`, the branch is skipped silently.
3. **Cache emptiness** — if the corresponding entry in `cache.Modules` is empty.
4. **Child propagation** — a second recursive pass marks any parent affected whenever *any* child has already been flagged, regardless of direct match.

---

## LLM Client (`client.go`)

### Public Types

```go
type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}
```

`Message` is the only exported type in this file. All other declarations are unexported and therefore not part of the public API:

| Element | Type | Notes |
|---|------|-------|
| `llmCaller` | interface | Unexported; methods include `CallLLM(ctx, systemPrompt, messages, jsonFormat)` returning `(string, error)` and `NumCtx() int`. |
| `llmClient` | struct | Unexported. Fields: `modelID`, `baseURL`, `numCtx`, `httpClient *http.Client`. |
| `newLLMClient` | func | Factory for `*llmClient`; referenced but not defined in this file. |
| `(c *llmClient) NumCtx()` | method | Returns the configured context window size. |
| `ollamaRequest` / `ollamaOptions` / `ollamaResponse` | structs | Unexported request/response payloads for Ollama's `/api/chat`. |
| `(c *llmClient) prepareOllamaRequest()` | private method | Builds and serializes the chat payload. |

### External I/O

All network interactions target `{baseURL}/api/chat` via HTTP POST with JSON body (`application/json`). The client is constructed once per instance; `httpClient` is reused across calls without locking, which is safe because timeout and base URL are fixed at construction time and never reassigned within this file.

**Response handling:** 200 OK responses have their bodies read fully via `io.ReadAll`; non-OK status codes trigger a bounded read (`io.LimitReader(resp.Body, maxErrorBodyBytes)`) whose error is discarded silently—the resulting string (containing the HTTP status and truncated body) is returned to the caller instead.

**Unresolved references:** `defaultHTTPTimeout` and `maxErrorBodyBytes` are referenced in this file but not defined here; they must be package-level constants or variables declared elsewhere, otherwise compilation fails.

---

## Chunking and Reduction Pipeline (`chunking.go`)

### `reduceWithLLM`

```go
func reduceWithLLM(ctx context.Context, c llmCaller, items []string, sysPrompt string, buildPrompt func(batch []string) string, logMsg func(batch []string) string, errMsg string, logEvent LogEventFunc) (string, error)
```

Entry point for all LLM-mediated reductions. It calls `c.CallLLM(ctx, sysPrompt, messages, false)` with a system prompt prepended to user messages; errors are wrapped via `fmt.Errorf("%s: %w", errMsg, err)`. The original error is preserved through `%w` wrapping.

### `reduceInChunks`

```go
func reduceInChunks(ctx context.Context, c llmCaller, nodePath string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)
```

Invoked from the synthesis step to consolidate a directory's file facts and child summaries. Calls `reduceWithLLM` with a prompt derived from `cfg.SystemPrompt` and the current extraction steps. Errors propagate through unchanged.

### `reduceFileFacts`

```go
func reduceFileFacts(ctx context.Context, c llmCaller, filePath string, stepName string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)
```

Consolidates per-extraction-step outputs for a single file. Calls `reduceWithLLM` with a prompt built from `cfg.SystemPrompt` and the current extraction steps; errors propagate as-is.

### `reduceItems`

```go
func reduceItems(ctx context.Context, items []string, maxChars int, reduceFn func(batch []string) (string, error)) (string, error)
```

Iterative map-reduce loop: accumulates items into batches that fit within `maxChars`, flushing when the next item would exceed the limit. If a single item exceeds the cap at entry, it is split with overlap rather than truncated—information preservation over compression. After each LLM pass, total output run count vs input is computed; if output ≥95% of input (integer division), recursion terminates and results are concatenated with `\n\n`. Context cancellation (`ctx.Err()`) returns early without partial results.

### `chunkTextWithOverlap`

```go
func chunkTextWithOverlap(text string, maxRunes int, overlapRunes int) []string
```

Splits text into overlapping chunks of at most `maxRunes` runes with `overlapRunes` rune overlap between adjacent chunks (clamped if ≥ `maxRunes`). Returns the full text as a single chunk when `maxRunes <= 0` or `overlapRunes >= maxRunes`. No panics, no bounds errors.

---

## Markdown Fence Stripping (`markdown.go`)

### Internal Implementation

```go
var markdownFenceRe regexp.Regexp
func stripOuterMarkdownFence(content string) string
```

Unexported; both the variable and function begin with lowercase letters. The regex is compiled once at package scope and never reassigned—effectively read-only after compilation.

**Algorithm:** trim whitespace → match triple-backtick fence (with optional `markdown`/`json` language tag) → return captured interior after trimming surrounding whitespace. No error return; if the pattern doesn't match, original input is returned unchanged.

---

## Constants (`constants.go`)

All nine named constants in this file are unexported:

| Constant | Purpose |
|----------|---------|
| `defaultHTTPTimeout` | 10-minute ceiling for remote API calls (set at package init) |
| `maxErrorBodyBytes` | 1 KB cap on error response payloads captured from failed requests |
| `defaultChunkOverlap` | 800-token overlap between consecutive text chunks during streaming/processing |
| `minNumCtxFloor` | 512 minimum threshold for context length calculations |
| `contextWindowAllocRatio` | 0.75 reserved proportion of available space for primary content |
| `maxCharsMultiplier` | 3× multiplier applied to base character limit |
| `metadataFileName`, `agentsFileName`, `defaultDirPerm` | Filesystem conventions for state persistence |

No mutable state, no external I/O, no error propagation. The single function-type alias (`LogEventFunc`) is declared but not used within this file.

---

## Synthesis Engine (`synthesize.go`)

Unexported types and functions only; all identifiers begin with lowercase letters.

### External I/O Matrix

| Source | Path | Behavior on Failure |
|--------|------|---------------------|
| `tools.ReadFileSafely(p.repoRoot, f)` — first call | `<repoRoot>/<f>` | Logged as status warning; returns `"", nil`. Swallowed by caller. |
| `tools.ReadFileSafely` — second call (inside `if facts == ""`) | Same | Same swallow pattern. |
| `p.client.CallLLM(p.ctx, systemPrompt, []Message{...}, false)` | Network endpoint via `llmCaller` | Wrapped with `%w`, includes step name and file path in message. Propagated to caller as `"LLM error extracting %s for %s: %w"`. |
| `tools.WriteFileSafely(p.repoRoot, modulePath, []byte(finalSum))` | `<repoRoot>/<docsDir>/modules/<safe-filename-of-node-path>.md` | Wrapped with `%w`, includes node path. Propagated to caller as `"failed to write module documentation for %s: %w"`. |

### Synthesis Algorithm (per `synthesizeNode`)

1. **Cache-first lookup** — check `cache.Files[f]` against precomputed SHA-256 hash; on match, reuse cached facts without re-reading the file.
2. **Recursive bottom-up traversal** — for each directory node: if unaffected and descendants are unaffected, return cached summary immediately. Sort child names alphabetically; recurse into children first; collect non-empty summaries into a map keyed by child name.
3. **Per-file extraction** — compute dynamic content limit from remaining context window capacity (allocates ~75% for file content). Split large files with `chunkTextWithOverlap`. Pass each chunk to LLM with step-specific prompt derived from `ExtractionSteps`. Strip markdown fences from responses via `stripOuterMarkdownFence`. Consolidate all outputs per extraction step using `reduceFileFacts`. Concatenate across steps into a single file fact string.
4. **Assembly** — combine extracted file facts (prefixed `"### File: basename"`) and child summaries (prefixed `"### Subsystem: name"`) into one `components` slice. If nothing exists, clear the cache entry and return empty.
5. **Final reduction and persistence** — pass all components to `chunked reduction` (`reduceInChunks`) producing a single consolidated summary for the directory; store in `cache.Modules[node.Path]`; write to disk under `<docsDir>/modules/<safe-filename-of-path>`.

---

## Utility Helpers (`utils.go`)

### `toSafeMarkdownFilename`

```go
func toSafeMarkdownFilename(modulePath string) string
```

Pure string manipulation; no file operations. Returns `"README.md"` when the path is empty or `"."`; otherwise joins with `"README.md"`. Used by tree traversal and synthesis code for module-level doc paths.

### `makeLogEvent`

```go
func makeLogEvent(onEvent func(Event)) LogEventFunc
```

Adapter factory returning a function that forwards to its callback only when non-nil (nil-safe pattern). No panics, no error returns.

---

## Error Propagation Summary

| Category | Handled Pattern | Swallowed Pattern |
|----------|-----------------|-------------------|
| LLM calls (`CallLLM`) | Wrapped with `%w`, descriptive message includes step/file context; propagated to caller. | — |
| File writes (`WriteFileSafely`) | Wrapped with `%w`; propagated to caller. | — |
| Hash computation per file (`computeSHA256`) | Error logged as status warning; loop continues, file skipped from cache. | Swallowed in both `RunInit` and `RunUpdate`. |
| Cache load/save (`loadMetadataCache`, `saveMetadataCache`) | — | Logged as status event; function completes regardless of write success/failure. |
| `.gitignore` loading | — | Logged as warning; execution proceeds. |
| Module file cleanup (`os.Remove`) | — | Error ignored via `_ = os.Remove(...)`. No verification. |
| Directory creation (`os.MkdirAll`) | Wrapped with `%w`; propagated to caller. | — |
| Non-OK HTTP responses | Bounded body read; error string returned (I/O failure during bounded read silently dropped). | Partially swallowed. |