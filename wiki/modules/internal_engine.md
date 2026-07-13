# internal/engine — Documentation Synthesis Engine

## Module Responsibility & Data Flow

The `internal/engine` package implements a recursive, context-window-aware LLM-based documentation synthesis engine. It operates in two modes—**init** (full generation from scratch) and **update** (incremental refresh)—orchestrating file discovery, change detection, chunked fact extraction, hierarchical synthesis, and persistent cache management for all intermediate state.

Data flow: `Runner.Run` → acquires lock → instantiates LLM client + orchestrator → dispatches to `RunInit` or `RunUpdate` → discovers code files via `tools.DiscoverCodeFiles` → classifies changes (Added/Modified/Deleted) against persisted `MetadataCache` → builds directory tree → propagates affected flags upward → recursively synthesizes each node via `synthesizeNode` (per-chunk LLM extraction → fact consolidation → reduction) → writes module docs to disk → persists cache.

All external communication is delegated: network calls go through the injected `llmCaller.CallLLM`; filesystem I/O goes through internal tool wrappers (`tools.ReadFileSafely`, `tools.WriteFileSafely`); path resolution uses `security.SafeResolve`. No mutexes, channels, or atomic operations are used anywhere in this package.

---

## Constants & Types (constants.go)

### Constants

| Constant | Value | Purpose |
|---|---|---|
| `defaultHTTPTimeout` | 10 min | Default HTTP client request timeout |
| `maxErrorBodyBytes` | 1024 bytes | Maximum size allowed in error response bodies |
| `defaultChunkOverlap` | 800 chars | Overlap between consecutive chunks during streaming/processing |
| `minNumCtxFloor` | 512 tokens | Minimum context window floor for processing |
| `contextWindowAllocRatio` | 0.75 | Ratio used when allocating context windows (reserves 75% of available space) |
| `maxCharsMultiplier` | 3 | Multiplier applied to character counts (scales token estimates or max output limits) |
| `metadataFileName` | `.metadata.json` | Persistent metadata file name for the engine's state |
| `agentsFileName` | `AGENTS.md` | Agent definitions/configuration file |
| `defaultDirPerm` | 0755 | Default directory permission mode |

### Type: `LogEventFunc`

Function type alias with signature `func(EventType, string)`. Callback interface for logging events; no business rules defined here.

---

## LLM Client Communication (client.go)

### `Message`, `llmClient`, `CallLLM`

Unexported except `Message` struct (`Role string`, `Content string`). The package-private `llmClient` carries: model ID, base URL, context count, HTTP client pointer. All set once during construction; never modified afterward within this file. No mutexes or channels used.

### Algorithmic Flow

1. **Prepare payload**: Assemble request body by prepending `systemPrompt` as a system-role message before any user-supplied messages, serialize as JSON to Ollama `/api/chat`.
2. **Send HTTP request**: Single synchronous POST with fixed timeout; no retries (fail-fast policy).
3. **Handle response**: On 200 OK → deserialize body → extract `message.content` field. Non-2xx status code → error string containing both HTTP status and raw response payload.
4. **Return result**: Extracted text content or wrapped error describing what went wrong.

### Error Handling Patterns

| Path | Wraps? | Notes |
|---|---|---|
| Marshal failure | ✅ `%w` | Good; caller can inspect via `errors.Is` |
| HTTP request construction | ❌ raw | Unwrapped; context lost for callers |
| Network/transport error | ❌ raw | Unwrapped; timeout vs DNS vs connect indistinguishable to caller |
| Success-path body read | ❌ raw | Unwrapped (rare) |
| JSON unmarshal failure | ✅ `%w` | Good |
| Non-OK status response | ❌ custom message | Discards read error (`_`); uses undefined `maxErrorBodyBytes` constant |

No retry logic. `defaultHTTPTimeout` and `maxErrorBodyBytes` referenced but not defined in this file; if they come from another package, callers have no visibility into their values or how they are set. No context propagation check between `Do()` and `Body.Close()`.

---

## Runner & Entry Point (runner.go)

### `Runner` / `Run`

`Runner` wraps a config pointer and exposes a single `Run(ctx, repoRoot, mode, onEvent)` method that is the only public entry point into the pipeline. It performs four sequential operations:

1. **Gitignore lockfile maintenance** — attempts to append the lockfile path to `.gitignore`. Failure is logged via `onEvent(EventStatus, ...)` and execution continues; no error returned.
2. **Repository lock acquisition** — calls `security.AcquireLock(repoRoot)`. On failure, returns wrapped error: `"failed to acquire repository lock: %w"`. No partial work proceeds without the lock.
3. **Mode dispatch** — constructs an LLM client from config fields (`cfg.ModelID`, `cfg.OllamaBaseURL`, `cfg.OllamaNumCtx`) and instantiates an `orchestrator`. Dispatches to either `RunInit` or `RunUpdate`; any other mode returns `"unsupported mode: %s"`.
4. **Deferred lock release** — `defer lock.Unlock()` runs after the pipeline completes, regardless of success or failure.

The `Runner.cfg` field is set once in `NewRunner(cfg)` and never mutated within this file. The pointer type allows external reassignment but no such mutation occurs here.

---

## Orchestrator & Pipeline Modes (orchestrator.go)

### `EventType`, `Event`, `orchestrator`

Unexported struct with two exported constants on the `EventType` type alias: `EventStatus = "status"` and `EventError = "error"`. The unexported `orchestrator` struct carries a method receiver; its public surface is empty.

### RunInit — Cold Start Flow

```
1. setupPipeline() → discover files, compute hashes, load .gitignore (swallowed), load cache (swallowed)
2. Mark every directory as affected (full regeneration)
3. Synthesize root summary from full tree via LLM calls
4. Generate architecture.md + quickstart.md using root summary
5. Write agent guidelines file (append if marker missing, overwrite otherwise)
6. Persist cache to disk → complete
```

### RunUpdate — Incremental Refresh Flow

```
1. setupPipeline() → discover current files, compute hashes
2. If extraction steps changed → invalidate entire cache (.StepsHash mismatch resets Files and Modules maps)
3. Classify all discovered files into Added/Modified/Deleted buckets by comparing current vs cached SHA-256 hashes
4. Build directory tree from allowed (existing + new) files only
5. Purge stale module summaries: for any `cache.Modules` path not represented in the live tree, delete both the cache entry and the physical `.md` file (`_ = os.Remove(...)` — error swallowed).
6. Compute affected directories; propagate upward through parent dirs.
7. If `len(affectedDirs) == 0` → return early (no-op, docs are up to date).
8. Synthesize root summary from the affected region via LLM calls.
9. Re-generate architecture.md + quickstart.md only if `.` is in `affectedDirs`, or either file does not exist on disk, or resolution fails (treated as "not exists").
10. Persist cache → complete.
```

### Pipeline Context (`pipelineContext`)

Unexported struct carrying: LLM client pointer, repo root string, config pointer, `*MetadataCache` pointer, affected directory set map, precomputed file hashes map, and event logger callback. Constructed fresh per invocation; no shared instances exist across goroutines in this file.

---

## Cache Persistence Layer (cache.go)

### `MetadataCache`, `FileCacheEntry`

Two structs with no methods:
- **`FileCacheEntry`** — pairs a SHA256 digest string (`sha256`) with extracted facts string (`facts`). Stored in `MetadataCache.Files map[string]FileCacheEntry`.
- **`MetadataCache`** — carries version int, steps hash string, files map, and modules map (module dir → synthesized summary).

### IsInitialized(repoRoot, docsDir) bool

Returns true only if the cache file exists AND is readable in one call. Reuses the existence check from `loadMetadataCache`. No error return.

### saveMetadataCache(repoRoot, docsDir, cache) error

Serializes to `{docsDir}/{metadataFileName}` with indented JSON (`json.MarshalIndent(cache, "", "  ")`). Marshaling errors propagate directly; write errors pass through `tools.WriteFileSafely`. The version field is pinned to the current value on every save.

### Version Compatibility Guard

On load: if `cache.Version != currentCacheVersion` (currently 1), returns a fresh zero-valued `MetadataCache{}` with nil error. No warning, no panic—caller sees success with an empty cache. This prevents stale metadata from corrupting future runs without explicit migration handling.

### Fault-Tolerant Initialization

If the file does not exist (`os.ErrNotExist` sentinel matched via `errors.Is`), returns a populated zero-valued cache with nil error. If it exists but cannot be read or unmarshaled, returns wrapped error `"failed to read metadata cache: %w"`. Callers can distinguish "not yet initialized" from "corrupt data."

---

## Chunking & Reduction Engine (chunking.go)

### reduceWithLLM, reduceInChunks, reduceFileFacts

All unexported; package-private. Implements a **context-aware recursive reduction** for LLM-based synthesis:

1. **Split**: Input list divided into batches whose combined character count fits within `contextSize × maxCharsMultiplier`.
2. **Reduce each batch**: Each batch sent to LLM via domain-specific prompt → one summary string per batch. If only one batch exists, returned directly; otherwise summaries concatenated and fed back into step 1 recursively until a single result remains.
3. **Graceful truncation**: Items exceeding `maxChars` are truncated with `"...\n...[truncated]"`. If every item exceeds the per-item budget (`allowedPerItem`), all items uniformly truncated to avoid infinite loops.
4. **Context cancellation**: Checked at entry points and before each recursive reduction round; cancellation errors propagate as-is.

### chunkTextWithOverlap (unused in this file)

Text-splitting utility with overlap between adjacent chunks. Appears available for reuse elsewhere but not referenced by any function here.

---

## Synthesis Core (synthesize.go)

All types and functions unexported (`pipelineContext`, `synthesizeNode`). Package-private.

### Recursive Directory Synthesis

A directory's summary is derived by recursively synthesizing children (subdirectories) and files together. Final result stored in cache under `cache.Modules[node.Path]` and written to disk at `docs/modules/<safe-filename>`.

### Caching Strategy with Hash-Based Invalidation

- **Module-level**: Stores synthesized summary per directory path. Reused when affected status indicates no changes (short-circuit return).
- **File-level**: Stores SHA256 hash + extracted facts in `cache.Files[f]`. Cache hit determined by comparing precomputed or read file hash against cached entry; only re-reads on miss.

### Multi-Step Extraction Pipeline Per File Chunk

1. System prompt augmented with current extraction step's prompt.
2. Single LLM call extracts facts from chunk → one fact per step.
3. All steps produce respective facts for the same file → consolidated via `reduceFileFacts`.

### Synthesis Order & Assembly

1. File facts (sorted child name order).
2. Child subdirectory summaries (after all files processed).
3. Combined list reduced to final summary via `reduceInChunks` at directory level.

### Affected-Dir Pruning

If a directory path has no affected status AND a cached module summary exists → short-circuit return cached value without reprocessing children or files.

---

## Tree & Change Detection (tree.go)

### Exported Types: `FileChange`, `DirNode`

- **`FileChange`** — `Path string`, `Status string` ("Added", "Modified", "Deleted").
- **`DirNode`** — `Path string`, `Files []string`, `Children map[string]*DirNode`.

### Three-Phase Algorithm

1. **Build tree**: Given raw file paths, split each on `/`, create intermediate `DirNode` entries for subdirectories, attach files to leaf nodes via `buildTree`.
2. **Determine affected directories from changes**: For each input change, mark directly changed file as affected (if exists in current set). When a file is Deleted, mark parent directory as affected. Walk recursively and check additional conditions: if node path matches `docs/modules/<safe-name>` but no such doc exists on disk → affected; if node has empty cached metadata entry → affected.
3. **Propagate status upward**: Recursive walk of `DirNode` tree propagates any `affected` flag from children to parents so a parent is considered affected if any child is.

---

## JSON Parser Utility (json_parser.go)

### stripOuterMarkdownFence(input) string

Single-pass parser that strips markdown or JSON code fences from strings:
1. Trims whitespace from input.
2. Attempts regex match against pattern capturing triple backticks with 3+ chars, optionally followed by `markdown` or `json`, then all content between opening and closing fences (group 1).
3. If matched → returns trimmed captured inner content.
4. If no match → returns original trimmed input unchanged.

No error return parameter. `regexp.MustCompile` panics on invalid pattern at package init—unhandled within this file, propagates to any caller importing the package.

---

## Utility Functions for Filename Generation and Event Logging (utils.go)

### Responsibility

This file provides two unexported utility functions supporting the engine's documentation generation pipeline and optional event logging instrumentation. Neither function communicates with external systems, modifies shared state, or returns errors. All operations are pure in-memory transformations.

---

### toSafeMarkdownFilename — Module Path to Markdown Filename Conversion

**Signature:** `func toSafeMarkdownFilename(path string) string` (inferred from usage; exact signature not exported)

#### Purpose

Transforms a module path into a safe markdown documentation filename, enforcing the engine's implicit one-to-one mapping contract between modules and their `.md` documentation files. The root module is special-cased to default to `root.md`.

#### Data Flow

1. Input: a raw string representing a module path (e.g., `"internal/engine"`).
2. Character substitution: every `/` character in the input is replaced with `_`.
3. Extension appended: `.md` is concatenated to produce the final filename.
4. Output: a single `string` containing the resulting markdown filename.

#### Behavior Notes

- No filesystem, network, database, or other external I/O occurs during execution.
- The function returns only a result string; no error type is returned. Invalid input characters (anything other than `/`) are passed through unmodified — callers receive no escape hatch for detecting malformed paths.
- Pure: no mutable state, no package-level globals modified, no struct fields touched.

---

### makeLogEvent — Optional Event Callback Adapter

**Signature:** `func makeLogEvent(onEvent LogEventFunc) func(EventType, message string)` (inferred from usage; `EventType`, `LogEventFunc` referenced but not defined in this file)

#### Purpose

Wires an optional callback into a logging pipeline. When invoked with an event type and message pair, it either forwards the call to the registered handler or silently discards it. This makes the logging subsystem purely additive — no required consumer exists for the engine's log events; instrumentation is opt-in only.

#### Data Flow

1. Input: a user-provided `onEvent` callback of type `LogEventFunc`.
2. Closure construction: returns a new function accepting `(EventType, message)` pairs.
3. Invocation behavior: if `onEvent == nil`, the returned closure does nothing when called — no panic, no error, no log output. If `onEvent` is non-nil, the call is forwarded to it with the provided type and message arguments.

#### Behavior Notes

- No I/O anywhere in this flow.
- Pure: no mutable state, no package-level globals modified.
- Silent failure path: when `onEvent == nil`, events are swallowed without notice or error wrapping.
- The returned function is always valid (non-nil), so callers can invoke it safely regardless of whether a handler was registered.

---

### Referenced Types (Defined Elsewhere)

The following types are referenced in this file but not defined within it:

| Type | Role |
|---|---|
| `EventType` | Categorizes log events into discrete types; carried as the first argument to event handlers and consumed by the returned closure from `makeLogEvent`. |
| `LogEventFunc` | Function signature for user-provided event handlers. Expected to accept `(EventType, message string)` or equivalent parameters. |
| `Event` (struct) | Domain model with at least two fields: `Type` (`EventType`) and `Message` (`string`). Events are tracked as discrete, typed records carrying descriptive payloads. |

---

### Concurrency & State Summary

- **Mutable state:** None. Both functions operate entirely on their inputs and return values without touching package-level variables or struct fields.
- **Concurrency mechanisms:** None. No mutexes, channels, atomic operations, or synchronization primitives are used in this file.