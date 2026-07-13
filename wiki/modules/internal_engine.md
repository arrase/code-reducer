# internal/engine — Architecture Documentation

## Module Responsibility & Data Flow

The `internal/engine` package implements an AI-driven project documentation generation and maintenance system. It orchestrates a Map-Reduce-style pipeline that discovers source code files, extracts facts via LLM calls, synthesizes hierarchical module summaries bottom-up through directory traversal, and produces standardized documentation (`architecture.md`, `quickstart.md`) for human onboarding.

**Pipeline lifecycle:**
1. `Runner.Run()` acquires a repository-level lock, instantiates an `orchestrator` bound to an Ollama HTTP client, then dispatches either the init or update path based on the supplied `Mode`.
2. The orchestrator loads `.gitignore` patterns and merges them with user-supplied ignore lists; it persists a `MetadataCache` (JSON file at `<docsDir>/<metadataFileName>`) that tracks per-file SHA256 hashes, extracted facts, module paths, and a steps fingerprint (`StepsHash`).
3. Code discovery walks the repo root respecting combined ignores; each discovered file is hashed via `computeSHA256`. A hierarchical directory tree (`*tree.DirNode`) is constructed from file paths.
4. The Map phase determines affected directories: in init mode every directory is marked affected; in update mode, current hashes are compared against cached entries to classify changes as Added/Modified/Deleted and delete orphaned module files whose parent directories no longer exist.
5. The Reduce phase propagates "affected" status upward through the tree so ancestors of changed descendants are also flagged for regeneration. If nothing is affected, synthesis short-circuits.
6. Hierarchical synthesis traverses bottom-up: `extractFileFacts` reads file bytes (or cache), chunks content with overlap, calls LLM per chunk per extraction step, strips markdown fences, and reduces results. `synthesizeNode` combines child summaries into a parent summary and writes the module's `.md` artifact to disk under `<docsDir>/modules/<safe-filename>.md`.
7. Standard docs (`architecture.md`, `quickstart.md`) are generated from the root synthesis summary via two additional LLM calls; they replace existing content if present, or skip entirely when no top-level directory is affected.
8. Agent guidelines at the repo root are maintained by appending to an existing file (if present) with separator-aware merging to avoid duplicate blank lines.
9. The pipeline terminates by persisting the metadata cache and logging completion status.

---

## Entry Point & Pipeline Control

### Runner

```go
type Mode = string // "init" | "update"
type Runner struct {
    cfg *config.Config
}

func NewRunner(cfg *config.Config) *Runner
func (r *Runner) Run(ctx context.Context, repoRoot string, mode Mode, onEvent func(Event)) error
```

`Run()` acquires a file-based lock via `security.AcquireLock(repoRoot)`; errors from this call are wrapped and returned immediately. On success it instantiates an `orchestrator`, then routes execution: init → `orch.RunInit`; update → `orch.RunUpdate`. The lock is released in the deferred path regardless of outcome.

---

## Orchestrator — Map-Reduce Pipeline Core

### Types & Events

```go
type EventType = string // "Progress" | "Error"
type Event struct {
    Type   EventType
    Message string
}
```

`EventStatus` and `EventError` are constants of type `EventType`. The event callback (`onEvent func(Event)`) is invoked on each pipeline milestone.

### Methods

```go
func (o *orchestrator) RunInit(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error
func (o *orchestrator) RunUpdate(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error
func (o *orchestrator) GenerateStandardDocs(ctx context.Context, repoRoot string, docsDir string, rootSum string, cfg *config.Config, logEvent LogEventFunc) error
```

`RunInit` and `RunUpdate` share a common setup phase: load `.gitignore`, merge ignores, load/save metadata cache, compute `StepsHash`. The update path additionally compares cached file hashes against current state to derive per-file change status.

`GenerateStandardDocs` issues two LLM calls (architecture + quickstart) using the system prompt concatenated with architecture prompts; both are wrapped in error handlers that return prefixed failure messages.

---

## LLM Client Layer — HTTP Transport Abstraction

### Type & Message

```go
type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}
```

`Message` is package-private; it participates in request payloads but never appears on the public API surface.

### Unexported Functions

All functions are unexported (lowercase). The struct `llmClient` holds `modelID`, `baseURL`, `numCtx`, and an immutable `*http.Client{Timeout: defaultHTTPTimeout}` set once in construction. Synchronization relies on the HTTP client's internal locking; no external mutex is observed.

**Data flow:**
1. System prompt prepended as first message with role `"system"`.
2. User/assistant messages appended in order.
3. Payload marshaled to JSON, POSTed to `baseURL/api/chat` (streaming disabled).
4. On 200: body read via `io.ReadAll`, unmarshaled into Ollama response schema, generated content extracted and returned.
5. Non-200: error body truncated at `maxErrorBodyBytes` bytes; status code and truncated payload returned as a single formatted string.

**Errors:** Marshal failure → `"failed to marshal request: %w"`; transport/network errors propagate raw; OK-body read failures are swallowed into `return "", err`; JSON parse errors wrapped with `"failed to parse response: %w"`. No retries exist.

---

## Cache Layer — Metadata Persistence

### Types

```go
type FileCacheEntry struct {
    SHA256 string `json:"sha256"`
    Facts  string `json:"facts"`
}

type MetadataCache struct {
    Version   int                       `json:"version"`
    StepsHash string                    `json:"steps_hash"`
    Files     map[string]FileCacheEntry `json:"files"`
    Modules   map[string]string         `json:"modules"`
}
```

`MetadataCache.Files` maps file paths to their SHA256 hash and extracted facts. `MetadataCache.Modules` maps module directory paths to synthesized summaries (empty string indicates no cached data). Both maps are initialized by `newEmptyCache()`/`loadMetadataCache()`. No synchronization is present; concurrent access from external callers is not guarded within this file.

### Functions

```go
func loadMetadataCache(repoRoot, docsDir string) (*MetadataCache, error)
func saveMetadataCache(repoRoot, docsDir string, cache *MetadataCache) error
func IsInitialized(repoRoot, docsDir string) bool
func computeSHA256(repoRoot, virtualPath string) (string, error)
```

**Error handling:** `loadMetadataCache` returns empty cache + nil error when the metadata file is missing (`os.ErrNotExist`) or version mismatched; other read errors are wrapped. JSON parse errors are wrapped and returned. `saveMetadataCache` marshals with indent; no nil check on receiver exists, so a nil argument would panic at marshal time. `computeSHA256` reads file content and produces hex-encoded SHA-256 digest; read failures propagate directly. `IsInitialized` is a boolean probe that silently converts any error (including permission issues) to false.

---

## Chunking & Reduction — Context Window Management

### Functions

```go
func reduceWithLLM(ctx context.Context, c llmCaller, items []string, sysPrompt string, buildPrompt func(batch []string) string, logMsg func(batch []string) string, errMsg string, logEvent LogEventFunc) (string, error)
func reduceInChunks(ctx context.Context, c llmCaller, nodePath string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)
func reduceFileFacts(ctx context.Context, c llmCaller, filePath string, stepName string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)
func reduceItems(ctx context.Context, items []string, maxChars int, reduceFn func(batch []string) (string, error)) (string, error)
func chunkTextWithOverlap(text string, maxRunes int, overlapRunes int) []string
```

**Algorithm:**
1. Pre-expansion: any item exceeding `maxChars` is split via `chunkTextWithOverlap` using default overlap of 800 runes (`defaultChunkOverlap`). Smaller items pass through unchanged.
2. Batching: greedy grouping until each batch fits within `maxChars`. Items alone exceeding the limit start their own batch; remaining partial groups form a final batch.
3. Reduction pass: each batch is sent to LLM via `c.CallLLM`; results collected into an intermediate list.
4. Loop prevention: sum rune counts of intermediates; if output ≥ 95% of input runes, concatenate with newlines and stop recursing (silent termination). Otherwise recurse on intermediates.
5. Base case: single-item batches skip reduction entirely and return content directly (`reduceFileFacts`).

**Errors:** LLM API errors are wrapped with the provided `errMsg` prefix. Context cancellation at entry points returns immediately with no further processing. Recursion errors propagate unchanged. No panic handlers exist in this file.

---

## Synthesis Engine — Recursive Directory Processing

### Internal Types & Functions

```go
type pipelineContext struct { /* internal state */ }
func extractFileFacts(...) (string, error)
func synthesizeNode(node *DirNode, ...) error
```

All types/functions are unexported. `pipelineContext` holds references to the LLM client (`llmCaller`), repo root, config, affected directories map, precalculated hashes map, and event callback.

**Algorithm:**
1. Per-file truncation: dynamic content limit based on total context window (75% for file content, 25% reserved for prompts/output).
2. Cache-first read: check `precalculatedHashes` and `cache.Files[f]`; skip disk read when both match.
3. On cache miss: split content into overlapping chunks within truncation budget; run each chunk through multi-step extraction pipeline defined by config prompts; strip markdown fences from LLM output; consolidate via reduction function.
4. Directory synthesis combines extracted file facts with child directory summaries into a component list, then reduces to produce parent module summary.
5. Module artifacts written to `<repoRoot>/<docsDir>/modules/<safe-filename>.md` after successful synthesis.

**Errors:** LLM call failure during extraction → wrapped error returned immediately up the call chain. File read failures in `extractFileFacts` initial path return `"", nil` with a warning event; the caller cannot distinguish this from "successfully extracted empty facts". Module write failures are wrapped and returned. Context cancellation at top of loop returns `err` directly.

---

## Tree Building — Directory Hierarchy & Affected Detection

### Types & Constants

```go
type ChangeStatus = string // "Added" | "Modified" | "Deleted"
const StatusAdded = "Added"
const StatusModified = "Modified"
const StatusDeleted = "Deleted"

type FileChange struct {
    Path   string
    Status ChangeStatus
}

type DirNode struct {
    Path      string
    Files     []string
    Children  map[string]*DirNode
}
```

### Functions

```go
func buildTree(files []string) *DirNode
func determineAffected(node *DirNode, ...) bool
func propagateAffected(tree *DirNode, affectedDirs map[string]bool)
```

**Algorithm:**
1. `buildTree` splits file paths by `/`, constructing nested `*DirNode` entries; root-level files sit directly on the root node.
2. `determineAffected`: for each node, compute a safe markdown filename from path and check existence via `os.Stat`. If not present → mark affected (only `os.IsNotExist(err)` checked; other errors silently swallowed). Cross-reference with `cache.Modules`: empty string entry marks affected. For deleted-file parents, parent directory is marked affected.
3. `propagateAffected` recurses upward: a parent becomes affected if any child is affected.

**Error handling:** Only `determineAffected` performs I/O; it never returns errors to callers. All other error conditions are swallowed internally.

---

## Utilities — Supporting Operations

### Functions

```go
func stripOuterMarkdownFence(input string) string
func toSafeMarkdownFilename(modulePath string) string
func makeLogEvent(onEvent func(Event), eventType EventType, message string) LogEventFunc
```

**`stripOuterMarkdownFence`:** Trims whitespace; matches 3+ backtick fences with optional `markdown`/`json` language identifier; returns inner content if matched. Unrecognized formatting passes through unchanged. Failures from regex matching are silently swallowed.

**`toSafeMarkdownFilename`:** Replaces `/` with `_`, collapses empty/single-segment to `"root"`, appends `.md`. No error check on output (e.g., filesystem length limits not validated).

**`makeLogEvent`:** Returns a closure that logs events; nil callback is handled gracefully by returning an inert function.

### Constants

All identifiers are unexported: `defaultHTTPTimeout`, `maxErrorBodyBytes`, `defaultChunkOverlap`, `minNumCtxFloor`, `contextWindowAllocRatio`, `maxCharsMultiplier`, `metadataFileName`, `agentsFileName`, `defaultDirPerm`. These define operational boundaries (10-minute HTTP timeout, 1024-byte error truncation, 800-character chunk overlap, 512-context minimum floor, 75% pre-allocation ratio) and filesystem conventions.

---

## Concurrency & Thread Safety Summary

No synchronization mechanisms (`sync.Mutex`, `sync.RWMutex`, atomics, channels) are present in any file within this package. All mutable state (cache maps, orchestrator struct fields) is accessed in single-threaded contexts only; thread safety cannot be verified from source alone and must be assumed caller-side responsible for serialization if used across goroutines. The repository-level lock in `Runner.Run` provides coarse-grained concurrency control but operates outside the engine's own fields.