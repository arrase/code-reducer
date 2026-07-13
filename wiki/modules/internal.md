# Code-Reducer Internal Architecture Documentation

---

## Subsystem: `internal/config` — Configuration Resolution & Persistence

### Responsibility

The `config` package owns the lifecycle of a single configuration object (`*Config`) consumed by every downstream component. It provides three operations: resolve, persist, and query. All resolution is deterministic: given identical inputs, the returned `*Config` is identical. No global mutable state exists within the package; the only shared state is a package-level variable holding default extraction steps (`DefaultExtractionSteps`).

### Data Flow

```
CLI flags (modelIDFlag, numCtxFlag) ─┐
Env vars (CODE_REDUCER_MODEL_ID,     │   ┌─────────────┐
OLLAMA_BASE_URL, OLLAMA_NUM_CTX)    ─┼──►│ LoadConfig  │─► Config
YAML file (.code-reducer.yaml)      └──>│ getConfigPath│
                                        └─────────────┘

Resolved *Config ──► SaveConfig() ──► disk: .code-reducer.yaml
```

### Constants

| Constant | Value / Purpose |
|---|---|
| `CodeReducerModelIDEnvKey` | Env key `"CODE_REDUCER_MODEL_ID"` consumed by `ResolveConfig`. |
| `OllamaBaseURLEnvKey` | Env key `"OLLAMA_BASE_URL"` consumed by both `LoadConfig` and `ResolveConfig`. |
| `OllamaNumCtxEnvKey` | Env key `"OLLAMA_NUM_CTX"` consumed by `ResolveConfig`; value parsed via `strconv.Atoi`. |
| `OllamaDefaultBaseURL` | Hard-coded fallback URL `"http://localhost:11434"`. |
| `OllamaDefaultModelID` | Hard-coded fallback model identifier `"ornith:9b"`. |
| `OllamaDefaultNumCtx` | Hard-coded fallback context size `8192`. |
| `DefaultDocsDir` | Hard-coded fallback docs directory name `"wiki"`. |
| `ConfigFileName` | Config file basename `".code-reducer.yaml"`. |

### Types: `ExtractionStep` and `Config`

```go
type ExtractionStep struct {
    Name   string  // yaml:"name"
    Prompt string  // yaml:"prompt"
}

type Config struct {
    ModelID                              string
    OllamaBaseURL                       string
    OllamaNumCtx                        int
    DocsDir                             string
    SystemPrompt                        string
    ModuleSynthesisPrompt               string
    ArchitecturePrompt                  string
    FileFactConsolidationPrompt         string
    ExtractionSteps                     []ExtractionStep
    Ignore                              []string
}
```

- `Config` is the sole configuration container. Every field maps to a distinct YAML key; all four prompt categories are independent strings with no cross-field dependencies.
- `ExtractionStep` is an opaque two-string struct used only inside the slice field of `Config`. The package-level variable `DefaultExtractionSteps` holds a static instance initialized at declaration and never mutated within this file.

### Variable: `DefaultExtractionSteps`

```go
var DefaultExtractionSteps []ExtractionStep
```

Package-level slice pre-populated with four entries covering API signatures, business logic, state and concurrency analysis, and errors/side effects. Read-only within the package boundary; any modification must occur outside this file.

### Functions: Configuration Resolution (`internal/config/resolve.go`)

#### `ResolveConfig(repoRoot string, modelIDFlag string, numCtxFlag string) (*Config, error)`

Merges CLI flags, environment variables, YAML config, and built-in defaults into a single resolved `*Config`. Returns nil error on success; returns both a non-nil Config and an error only when the underlying file read fails with a condition that is not "file does not exist".

**Parameters:**

| Parameter | Purpose |
|---|---|
| `repoRoot` | Repository root path. Used to derive the config file path for the disk-read step. |
| `modelIDFlag` | Explicit model ID from CLI; overrides environment and YAML only if non-empty. |
| `numCtxFlag` | Explicit context size from CLI; parsed as int with a minimum-of-1 validation gate before taking effect. |

**Resolution Priority Chains:** Every field follows an independent priority chain:

- **Model ID** — built-in default → YAML `model_id` → env `CODE_REDUCER_MODEL_ID` → CLI flag (skipped if empty).
- **Ollama Base URL** — built-in default → YAML `ollama_base_url` → env `OLLAMA_BASE_URL`. No CLI override.
- **Ollama Context Size** — built-in default → YAML `ollama_num_ctx` → env `OLLAMA_NUM_CTX` → CLI flag (parsed via `strconv.Atoi`, minimum 1). Env and CLI values that fail to parse are silently skipped; the previously-resolved fallback remains in effect.
- **DocsDir** — built-in default if unset, otherwise uses YAML-specified value. No env or CLI override path exists for this field.
- **Prompts** (system, synthesis, architecture, fact consolidation) — each has its own independent chain: built-in default → YAML override if non-empty. No CLI or env override is provided in this code.

**Error Handling:**

| Condition | Behavior |
|---|---|
| `LoadConfig(repoRoot)` returns error + not-exist | Swallowed; replaced with an empty `&Config{}` and returned alongside a nil error. |
| `LoadConfig(repoRoot)` returns any other error | Wrapped as `"failed to load configuration file: %w"` and returned. |
| Integer parsing (`strconv.Atoi`) fails | Skipped; previous fallback retained. No error propagated. |

### Functions: Configuration Persistence (`internal/config/io.go`)

#### `getConfigPath(cwd string) string`

Derives the absolute path to `.code-reducer.yaml` by combining the working directory with the fixed filename constant. Return value is a plain string; no error path exists in this signature.

#### `ConfigExists(cwd string) bool`

Returns true if the config file is present at the derived path, false otherwise. Uses stat semantics internally (`os.Stat`). No error propagation — callers cannot distinguish "file does not exist" from other OS-level errors (e.g., permission denied).

#### `LoadConfig(cwd string) (*Config, error)`

Reads raw bytes from disk at the resolved config path, then unmarshals them into a `*Config` struct via YAML parsing. Any parse failure is wrapped and returned as an error with descriptive prefix `"failed to parse yaml config: %w"`.

#### `SaveConfig(cwd string, cfg *Config) error`

Persists `*cfg` back to disk using an atomic rename pattern:

1. Marshals the in-memory config to YAML.
2. Normalizes key prefixes by inserting extra blank lines before specific top-level keys (`system_prompt`, `module_synthesis_prompt`, `architecture_prompt`, `file_fact_consolidation_prompt`, `extraction_steps`, `ignore`) — purpose of this normalization is not documented within the file boundary.
3. Creates a temporary file in the same directory via `os.CreateTemp`.
4. Writes normalized YAML to the temp file, syncs, and closes it.
5. Sets restrictive permissions on the temp file via `chmod`.
6. Renames the temp file over the original config path atomically.

Any failure at any step (read, parse, write, sync, close, chmod, rename) is wrapped with a descriptive prefix (`"failed to create temp file:"`, etc.) and returned as an error. Cleanup of the temp file runs in a defer block so removal happens even when errors occur mid-save. A redundant explicit `Close()` call follows the deferred one; closing an already-closed file returns nil, so this is non-fatal but unnecessary.

---

## Subsystem: `internal/engine` — Incremental Documentation Synthesis Engine

### Responsibility

This package orchestrates automatic generation of architecture overviews (`architecture.md`), quickstart guides (`quickstart.md`), and per-module API summaries for software repositories. It operates in two modes: **init** (full generation from scratch) and **update** (incremental regeneration triggered by file changes). The engine consumes source code through a hierarchical synthesis pipeline driven by LLM calls, persists intermediate state to disk for resumption between runs, and uses an exclusive lock file to serialize concurrent invocations.

### Data Flow

```
Runner.Run(ctx, repoRoot, mode) 
    ↓ acquires lock via security.AcquireLock(repoRoot)
orchestrator.RunInit(ctx, ...) | orchestrator.RunUpdate(ctx, ...)
    ↓ setupPipeline() loads gitignore + discovers code files + computes SHA256 hashes per file
tree.buildTree(files) → DirNode hierarchy
    ↓ synthesizeNode(root) recurses post-order: child dirs first (reusing cached summaries), then each file
client.CallLLM(...) — LLM API call for extraction step on chunked content
reduceInChunks / reduceFileFacts — recursive text synthesis with batch partitioning and overlap handling
cache.saveMetadataCache(repoRoot, docsDir, cache) — persists Files + Modules maps to disk as JSON
```

---

### `cache.go` — Persistent Metadata Cache

#### Types

- **`FileCacheEntry`** — Key-value pair mapping a file's SHA256 hash to its synthesized facts string. Used as the per-file cache entry in `MetadataCache.Files`.
- **`MetadataCache`** — Package-level cache state containing:
  - `Version int` — Cache format version; mismatched versions return an empty initialized structure rather than corrupt data.
  - `StepsHash string` — SHA256 of serialized extraction steps, used to invalidate the entire cache when configuration changes.
  - `Files map[string]FileCacheEntry` — Per-file hash→facts mapping for incremental updates.
  - `Modules map[string]string` — Per-directory synthesized summary storage.

#### Public API Surface

- **`IsInitialized(repoRoot, docsDir string) bool`** — Probes `{docsDir}/{metadataFileName}` existence; returns `false` on read failure (error swallowed).
- **`saveMetadataCache(repoRoot, docsDir string, cache *MetadataCache) error`** — Serializes the metadata cache to indented JSON at `{docsDir}/{metadataFileName}`. Sets `cache.Version = currentCacheVersion` before marshaling. JSON marshal errors propagate; write failures propagate as-is.
- **`computeSHA256(repoRoot, virtualPath string) (string, error)`** — Reads a source file via `tools.ReadFileSafely`, computes SHA256 hash in memory. Read errors propagated directly.

#### Internal Functions

- **`loadMetadataCache(repoRoot, docsDir string) (*MetadataCache, error)`** — Constructs an empty `MetadataCache` with fresh maps (`make(map[string]FileCacheEntry)`, `make(map[string]string)`). If the cache file exists and parses successfully with matching version, returns parsed state + nil. On `os.ErrNotExist`: returns default structure + **nil error** (silent recovery). Other read errors wrapped via `%w`. Version mismatch: returns clean cache + nil without signaling an error.

#### Thread Safety Assessment

No synchronization primitives present. The maps in `MetadataCache` are created fresh and returned as struct values, but callers receive map references pointing to shared backing storage. Concurrent access to `Files`/`Modules` from multiple goroutines constitutes a data race unless external locking exists outside this module boundary. On-disk writes via `saveMetadataCache` provide coarse-grained serialization through OS write ordering alone; no explicit mutex is used.

---

### `chunking.go` — Recursive Hierarchical Text Synthesis

#### Internal Functions (All Unexported)

- **`reduceWithLLM(ctx, c llmCaller, items []string, sysPrompt string, buildPrompt func(batch []string) string, logMsg func(batch []string) string, errMsg string, logEvent LogEventFunc) (string, error)`** — Feeds a batch of items to an LLM via `c.CallLLM(ctx, sysPrompt, messages, false)`. System prompt describes the synthesis task. Response markdown fences are stripped before returning. Errors from `CallLLM` wrapped as `fmt.Errorf("%s: %w", errMsg, err)` where `errMsg` is caller-supplied (e.g., `"LLM error during synthesis"`). Original error type information lost for classification purposes.

- **`reduceInChunks(ctx, c llmCaller, nodePath string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)`** — Delegates to `reduceWithLLM`. No external I/O beyond the LLM call.

- **`reduceFileFacts(ctx, c llmCaller, filePath string, stepName string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)`** — Delegates to `reduceWithLLM`. Used per extraction step during file synthesis in `synthesize.go`.

- **`reduceItems(ctx, items []string, maxChars int, reduceFn func(batch []string) (string, error)) (string, error)`** — Core recursive reduction engine:
  1. Partitions input items into batches where total character count stays within `maxChars`. Oversized single items are truncated in-place via `items[0] = string(runes[:maxChars]) + "\n...[truncated]"` (mutation without signal).
  2. If exactly one batch exists, calls the reduction function on it directly.
  3. Otherwise, recursively reduces each sub-batch independently, concatenates results, and recurses until a single result remains — forming a binary tree of reductions.

- **`chunkTextWithOverlap(text string, maxRunes int, overlapRunes int) []string`** — Splits a single text into overlapping rune-counted chunks at fixed step size. Used for preparing input sequences requiring positional overlap between adjacent pieces during file synthesis.

#### State and Side Effects

- `reduceItems` mutates `items[0]` in-place when a single oversized item is encountered. This silent truncation provides no error signal to callers; data loss goes undetected unless output string is inspected.
- All other functions are pure (string manipulation, recursion) with the sole exception of the LLM API call inside `reduceWithLLM`.

---

### `client.go` — Ollama HTTP Client

#### Types

- **`Message`** — Struct with `Role string \`json:"role"\`` and `Content string \`json:"content"\``. Used as the conversational turn schema for LLM requests.
- **`ollamaRequest`, `ollamaOptions`, `ollamaResponse`** — Unexported structs holding request/response payloads.

#### Methods on `*llmClient`

- **`newLLMClient(modelID, baseURL string, numCtx int) *llmClient`** — Constructor storing model identifier, base URL, and context size. Field assignments occur once; no subsequent mutation within the file.
- **`(c *llmClient).NumCtx() int`** — Returns stored `numCtx` value. Read-only accessor.
- **`(c *llmClient).prepareOllamaRequest(ctx, systemPrompt string, messages []Message, jsonFormat bool) (*http.Request, error)`** — Prepends the system prompt as a new entry to the messages array, then serializes model ID + conversation context + processing options into JSON. Constructs HTTP POST request targeting `{baseURL}/api/chat`. JSON marshal failures wrapped with `%w`; request construction errors propagated as-is.
- **`(c *llmClient).CallLLM(ctx, systemPrompt string, messages []Message, jsonFormat bool) (string, error)`** — Executes the HTTP POST via `c.httpClient.Do` with timeout applied to the client itself (`&http.Client{Timeout: defaultHTTPTimeout}`). On 200 status: deserializes response body fully and returns generated text. On non-OK status: reads up to `maxErrorBodyBytes` (value undefined in this file; if zero, `io.LimitReader(resp.Body, 0)` blocks indefinitely until EOF rather than applying a size limit), then returns formatted error `"ollama api error: status %d, response: %s"` where the body read failure is silently swallowed — only the HTTP status and successfully-read bytes are visible in the returned error.

#### Unresolved References

- `defaultHTTPTimeout` and `maxErrorBodyBytes` referenced but not defined in this file; resolved at link time or via another package constant. If these resolve to zero values, behavior deviates from intended timeout/limit semantics.

---

### `constants.go` — Runtime Configuration Contract

#### Function Types

- **`LogEventFunc func(EventType, string)`** — Signature for external observer hooks into system events (type + message), enabling traceability without hardcoding logging implementation.

#### Constants and Values

This file defines the numerical boundaries governing engine behavior:
- HTTP operation timeout: 10 minutes before aborting network calls.
- Error response truncation limit: responses beyond 1 KB are truncated for non-critical error paths.
- Chunked text processing: input split into overlapping segments of ~800 characters, enabling streaming across large documents without full loading.
- Context window budgeting: only 75% of total memory reserved; minimum floor enforced at ~512 tokens regardless of available space.
- Output scaling rule: maximum generated output capped at roughly 3× input length to prevent runaway generation loops.
- Persistent state layer: engine writes `.metadata.json` snapshot and reads agent behavior from `AGENTS.md`, establishing a file-based identity layer for long-running processes.
- Directory permissions: any directories the engine creates are assigned 0755 by default, controlling filesystem boundary access.

---

### `json_parser.go` — Markdown Fence Stripping Utility

#### Internal Functions

- **`stripOuterMarkdownFence(content string) string`** — Best-effort extraction of raw content from strings wrapped in backtick-delimited markdown or JSON fences (3+ backticks, optional language identifier like `markdown` or `json`). Algorithm: trim whitespace → test against regex pattern for fence syntax → if match found, extract interior between opening and closing fences; otherwise return trimmed input unchanged. Regex mismatch returns original with whitespace stripped — no error signaled.

#### Side Effects

No external I/O. Only standard library `regexp` and `strings` used for in-memory processing. If a malformed regex were supplied at compile time it would panic during package initialization, but this is not handled within the function scope.

---

### `orchestrator.go` — Incremental Documentation Generation Orchestrator

#### Types

- **`EventType`** (`type string`) — Event classification type.
- **`EventStatus`, `EventError`** (`constant EventType`) — Predefined event status/error constants.
- **`Event struct`** — `Type EventType`, `Message string`. Carries typed event data with message payload.

#### Methods on `*orchestrator`

- **`GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum string, cfg *config.Config, logEvent LogEventFunc) error`** — Generates `architecture.md` and `quickstart.md` from the synthesized root summary. Writes both files via `tools.WriteFileSafely`. Errors wrapped and propagated.
- **`RunInit(ctx, repoRoot string, cfg *config.Config, onEvent func(Event)) error`** — Full generation path:
  1. Loads `.gitignore`, user-configured ignore paths, metadata cache from disk.
  2. Discovers code files respecting ignores; errors propagated directly as returned errors.
  3. Computes SHA256 hashes per file (per-file failures logged + skipped from processing).
  4. Builds hierarchical directory tree from all discovered files.
  5. Marks every directory as "affected" for fresh generation.
  6. Traverses tree bottom-up: each node's summary becomes part of its parent's input; leaf summaries flow up through directories, then into module-level summaries, converging at repository root.
  7. Uses root-level synthesis result to generate global `architecture.md` and `quickstart.md`.
  8. Ensures `.agent-guidelines.md` exists: if missing, creates fresh; if contains "AI Agent Guidelines", appends; otherwise replaces. Read failures for this file are swallowed — code proceeds as if empty/missing. Write errors wrapped and returned.

- **`RunUpdate(ctx, repoRoot string, cfg *config.Config, onEvent func(Event)) error`** — Incremental path:
  1. Detects file changes by comparing current hashes against cached ones — classifying each as Added, Modified, or Deleted.
  2. Builds tree only from currently existing files; prunes cache entries for deleted files and removes module documentation for directories no longer present.
  3. Determines affected directories based on change set; propagates "affected" status through hierarchy.
  4. If nothing changed (zero affected directories), returns immediately with no regeneration.
  5. Otherwise re-runs hierarchical synthesis within affected modules only.

- **Private Functions** — `setupPipeline(repoRoot, cfg, logEvent) (string, []string, *MetadataCache)` loads gitignore + discovers files + computes hashes; errors from `LoadGitignore` swallowed after logging warning event. `teardownPipeline(repoRoot, docsDir, cache, logEvent, successMsg)` saves metadata cache — write failures logged as warnings but **not returned** (silently swallowed).

#### Error Propagation Summary

| Operation | Failure Handling |
|---|---|
| LLM call (`CallLLM`) | Wrapped + propagated as returned error |
| Write file (`WriteFileSafely`) | Wrapped + propagated in main path; swallowed in teardown |
| Read file (`ReadFileSafely` for `.agent-guidelines.md`) | Swallowed — falls through to write path |
| Load gitignore | Logged warning, swallowed |
| Discover files | Propagated as returned error |
| SHA256 hash per file | Per-file: logged + skipped from processing |
| MkdirAll | Wrapped + propagated |
| SafeResolve (path existence) | Errors treated as "not found", swallowed |
| os.Remove (cleanup) | Swallowed silently — stale files remain on disk if deletion fails |

---

### `runner.go` — Pipeline Execution Wrapper with Locking

#### Types and Constants

- **`Mode type string`** — Operation mode of the documentation pipeline.
- **`ModeInit`, `ModeUpdate`** (`const Mode`) — Init mode constant and update mode constant.
- **`Runner struct`** — Orchestrates execution of the documentation pipeline. Holds immutable reference to configuration set once via `NewRunner`.

#### Methods on `*Runner`

- **`NewRunner(cfg *config.Config) *Runner`** — Constructor storing configuration values (model ID, base URL, context size). No field reassignment within this file after construction.
- **`(r *Runner) Run(ctx, repoRoot string, mode Mode, onEvent func(Event)) error`** — Main entry point:
  1. Attempts to add lockfile entry to `.gitignore` via `security.EnsureGitignoreHasLockfile(repoRoot)`; failure swallowed — only logged via callback.
  2. Acquires exclusive repository lock via `security.AcquireLock(repoRoot)`. Failure propagated with wrapping: `"failed to acquire repository lock: %w"`.
  3. Defer unlock for after pipeline completes (success or error). No explicit error handling for unlock failures visible in this file.
  4. Instantiates LLM client and orchestrator based on stored configuration values.
  5. Dispatches either `RunInit` or `RunUpdate` depending on mode string match; unknown modes return `"unsupported mode: %s"` without wrapping.

---

### `synthesize.go` — Recursive Context-Aware Synthesis Engine

#### Internal Structures and Functions

- **`pipelineContext struct`** — Holds shared state for a synthesis run: LLM client, repository root, docs directory, configuration, extraction steps, affected directories set, precalculated hashes map. Locally allocated per method call in `RunInit`/`RunUpdate`.
- **`synthesizeNode(p *pipelineContext, node *DirNode) (string, error)`** — Recursive synthesis walking the directory tree:
  1. Post-order traversal: process children before files. For each child directory, recursively call `synthesizeNode`; if child is not affected and has a cached summary, reuse directly.
  2. Per-file processing loop: compute or retrieve SHA256 hash (from precalculated hashes map or read-and-hash). On cache hit (hash matches stored entry), return cached facts without reading the file. On cache miss, read file content; if multiple chunks needed based on dynamic context window size × 4 chars/token × allocation ratio, split with overlap. For each chunk, run LLM extraction across all configured `ExtractionSteps`, building consolidated fact per step via `reduceFileFacts`.
  3. Component assembly: build list of components — file facts (prefixed by filename) followed by child directory summaries (prefixed by subsystem name). If no components exist, cache empty string and return early.
  4. Final reduction: combine all assembled components into single consolidated summary for current directory using `reduceInChunks`. Cache result under `p.cache.Modules[node.Path]` and write to disk at `<docsDir>/modules/<safe-filename-of-path>`.

#### External Interactions

- **Disk Read**: `tools.ReadFileSafely(p.repoRoot, f)` reads individual source files when cache misses occur. Errors logged as warnings via callback but do not propagate — facts for that file remain empty string; processing continues to next file.
- **Disk Write**: `tools.WriteFileSafely(p.repoRoot, modulePath, []byte(finalSum))` writes synthesized documentation at `<repoRoot>/<cfg.DocsDir>/modules/<safe-filename>`. Failures wrapped with context: `"failed to write module documentation for %s: %w"`. Propagates up through synthesis stack.
- **Network/API**: `p.client.CallLLM(p.ctx, systemPrompt, []Message{...}, false)` makes LLM API calls per extraction step and chunk combination during file synthesis. Multiple sequential calls made per file (one per `cfg.ExtractionSteps`), multiple files processed before moving to child summary reduction.

#### Thread Safety Assessment

No explicit locks/mutexes visible in this file. Context (`p.ctx`) checked at boundaries for cancellation detection but does not prevent concurrent map writes to the cache. If multiple goroutines invoke `synthesizeNode` with different `pipelineContext` instances sharing the same underlying `*MetadataCache`, both `cache.Files` and `cache.Modules` maps will be concurrently written without locking — a data race unless external synchronization exists outside this module boundary.

---

### `tree.go` — Directory Tree Building and Affected Detection

#### Types

- **`FileChange struct`** — `Path string` (file path), `Status string` (change status: `"Added"`, `"Modified"`, `"Deleted"`).
- **`DirNode struct`** — `Path string` (directory path), `Files []string` (list of file paths in this directory), `Children map[string]*DirNode` (child directory nodes keyed by name).

#### Internal Functions

- **`buildTree(repoRoot, files []string) *DirNode`** — Walks each path's directory components to create nested `DirNode` objects with files listed under parent directories. Mutated during tree construction only; single-threaded build path.
- **`determineAffected(p *pipelineContext, node *DirNode) bool`** — For each node: checks if any file in its `Files` list matches a changed file (marks as affected); resolves expected module path via safe filename conversion and secure resolution; if resolved path does not exist on disk, marks as affected (indicating missing module); checks external metadata cache (`cache.Modules`); if no cached value recorded for this directory's path, marks as affected.
- **`propagateAffected(p *pipelineContext) map[string]bool`** — Walks tree again; any node whose subtree contains an affected descendant gets itself marked as affected. Parent directories inherit children's impact.

#### External I/O and Error Propagation

- `determineAffected`: calls `os.Stat(absModulePath)` after resolving a module path via `security.SafeResolve`. Only the "not exist" case is handled; if `SafeResolve` returns an error or `Stat` returns a non-not-exist error (e.g., permission denied, I/O fault), it is not checked and never propagated to callers.
- All other functions (`propagateAffected`, `buildTree`) are pure in-memory operations using only string manipulation via standard library imports — no actual read/write calls.

---

### `utils.go` — Internal Utility Helpers

#### Internal Functions (Both Unexported)

- **`toSafeMarkdownFilename(path string) string`** — Converts a module path into a filesystem-safe markdown filename by replacing `/` with `_`, defaulting to `root.md` when the result is empty or just `.`. Single-step transformation using only in-memory string manipulation (`strings.ReplaceAll`). Silently returns computed string regardless of input — no error signaling.
- **`makeLogEvent(onEvent func(Event)) LogEventFunc`** — Produces a `LogEventFunc` that forwards an `EventType` and message string to an optional callback, with nil-safety for the callback. Returns a ready-to-use closure without error handling.

#### Side Effects

No external side effects. Both functions perform only in-memory operations: path separator replacement, default value fallback, and closure wrapping. No disk, network, database, or API interactions occur.

---

## Subsystem: `internal/security` — Security Boundaries & Inter-Process Exclusivity

### Responsibility

Enforces security boundaries and inter-process exclusivity during Code-Reducer operations. The module prevents directory-traversal attacks by constraining resolved paths within the repository root, ensures shared resources are not accessed concurrently via file locking, and integrates with version control to keep lock state out of tracked content. No mutable package-level state exists; all concurrency control is instance-scoped.

### Data Flow

The two sentinel errors defined in `errors.go` (`ErrPathTraversal`, `ErrLockHeld`) serve as the terminal failure signals for path-validation and lock-contention scenarios. Callers receive these values through standard Go error-return patterns and propagate them via `if err != nil` checks; no panic, crash, or custom wrapping occurs within this package. The actual I/O operations that trigger these errors reside in other files not shown here.

### Type: `SimpleLock`

```go
type SimpleLock struct {
    lockPath string
    file     *os.File
    mu       sync.Mutex
    closed   bool
}
```

**State characteristics:**

- **`lockPath string`** — read-only after construction; never reassigned within this package. Assigned exclusively by the constructor.
- **`file *os.File`** — modified inside `Unlock()` (`l.file = nil`) and assigned once in `AcquireLock()`.
- **`mu sync.Mutex`** — value-type mutex embedded on each instance. Acquired and released only within `Unlock()` via defer.
- **`closed bool`** — modified inside `Unlock()` (`l.closed = true`).

#### `Unlock() error`

Closes the underlying file descriptor, removes the lockfile atomically (ignoring errors if the file no longer exists), and sets internal state to closed. Safe to call multiple times or from different goroutines without deadlocking. Returns a non-nil error only when both close and remove fail; otherwise returns nil. No panic path observed.

#### `SafeResolve(repoRoot string, inputPath string) (string, error)`

Resolves an arbitrary input path against the repository root boundary. Walks upward via `os.Lstat` until hitting the first physically-existing directory or reaching the root (`parent == current`). Symlinks are evaluated on existing ancestor components to defeat symlink-based traversal attacks. Non-existent suffix components are appended after the walk completes. If the relative path starts with `..`, it is rejected as a security violation and returns `ErrPathTraversal`. Any stat returning something other than ENOENT during the upward walk is returned immediately without masking — this could surface unexpected errors (e.g., EACCES) as fatal rather than being handled.

#### `AcquireLock() (*SimpleLock, error)`

Resolves a lockfile path within the repository root using `SafeResolve`; atomically creates the file via O_WRONLY|O_CREATE|O_EXCL to prevent concurrent acquisition from another process; writes the current PID into the newly-created file. If lock path cannot be resolved → returns nil plus the original error from SafeResolve. If O_EXCL open fails with EEXIST → wraps `ErrLockHeld` and includes lockPath in message. If write to lockfile fails → closes file, removes lockfile, then returns wrapped error (prevents stale locks). No panic or crash path observed.

#### `EnsureGitignoreHasLockfile(lockPath string) error`

Reads `.gitignore`; checks whether it already contains an entry for the lockfile; if not, appends a commented entry under a temp file written atomically via rename. Uses `Sync()` before rename for durability. Failure modes: unreadable `.gitignore` → propagates upstream error; read failure (non-ENOENT) → wrapped; temp-file creation/write/sync/chmod/rename failures → each wrapped with descriptive messages. Partial writes are not persisted if sync fails (atomic-ish via same-directory pattern).

### Error Propagation Summary

| Function | Failure Mode | Caller Impact |
|---|---|---|
| `SafeResolve` | Absolute path error, symlink resolution failure, stat-on-nonexistent-component error (only if not ENOENT), or path-traversal detection via `ErrPathTraversal`. | Any caller of `AcquireLock` or `EnsureGitignoreHasLockfile` receives the wrapped error directly. |
| `AcquireLock` | Lock path cannot be resolved → returns nil + original error from SafeResolve. O_EXCL open fails with EEXIST → wraps `ErrLockHeld`. Write failure → closes file, removes lockfile, then returns wrapped error. | Caller receives non-nil error; no lock is held. No panic or crash path observed. |
| `SimpleLock.Unlock()` | Close of underlying file fails → captured as err. Removal failure (other than ENOENT) only considered if close succeeded and removal also failed — otherwise swallowed (`if err == nil { err = removeErr }`). | Returns non-nil error only on double-failure; otherwise returns nil. No panic path. |
| `EnsureGitignoreHasLockfile` | `.gitignore` cannot be resolved → propagates upstream. Read fails (non-ENOENT) → wraps error message. Temp file creation fails → wrapped. Write to temp file fails → wrapped. Sync fails → wrapped and returns early; subsequent close/rename skipped. Chmod fails → wrapped. Rename fails → wrapped. | Caller receives a descriptive error for each failure point. Sync-before-rename pattern means partial writes are not persisted if sync fails (atomic-ish). |

### Notable Patterns / Risks

- **`SafeResolve` loop**: The `for` loop calling `os.Lstat` walks upward until it finds an existing ancestor or reaches the root (`parent == current`). If a non-existent-component stat returns something other than ENOENT, it is returned immediately — this could mask unexpected errors (e.g., EACCES on a directory) as fatal rather than being handled.
- **`.gitignore` rename**: The temp-file + rename pattern ensures atomicity but requires write access to the parent directory of `.gitignore`. Failure modes are well-covered, though no retry logic exists for transient I/O errors (e.g., NFS timeout during `Sync`).
- **Lockfile cleanup on write failure**: In `AcquireLock`, if PID write fails, the lockfile is explicitly removed before returning error — this prevents stale locks from being left behind.

---

## Subsystem: `internal/tools` — Repository File Operations & Git Integration

### Responsibility

The `internal/tools` package provides safe filesystem I/O primitives and Git command execution utilities for operating on a repository root directory. It is split into two files: file handling (`file_tools.go`) and Git interaction (`git_tools.go`). The module has no mutable state and performs only local disk operations.

### Data Flow

```
caller → [ReadFileSafely | WriteFileSafely] → security.SafeResolve (path validation) → OS open/write/rename
```

```
caller → [DiscoverCodeFiles | ShouldIgnoreFile] → gitignore parsing / extension whitelist / binary detection → []string or bool
```

```
caller → RunGit(repoRoot, args...) → exec.Command("git", ...) → bytes.Buffer capture → stdout trimmed string
```

---

### File I/O: `file_tools.go`

#### Safe Read and Write

**ReadFileSafely** resolves the virtual path against `repoRoot`, performs a TOCTOU-safe open-and-stat sequence to prevent symlink races, reads the file contents via `io.ReadAll`, and returns `(content []byte, error)`. All OS errors are wrapped with a descriptive prefix.

**WriteFileSafely** ensures parent directories exist via `os.MkdirAll` (`defaultDirPerm = 0755`), creates a temporary file in the same directory as the destination using `os.CreateTemp`, writes content, syncs and closes the temp file, applies `Chmod` with `defaultFilePerm = 0644`, then atomically renames into place. Each step wraps its error; on failure, the deferred cleanup block unconditionally calls `tmpFile.Close()` and `os.Remove(tmpName)`. If `os.Rename` fails after `Chmod`, the destination path may be left in an inconsistent state with no rollback.

#### Gitignore Loading and File Classification

**LoadGitignore** reads `.gitignore` from `repoRoot` using `bufio.Scanner`. Non-comment lines are parsed via `ignore.CompileIgnoreLines`; existence errors return `(nil, nil)` (treated as success). Scanner errors at end-of-loop propagate directly.

**ShouldIgnoreFile** evaluates whether a file at relative path is ignored by applying four checks in order:
1. User-defined patterns from the loaded `GitIgnore` instance via `ignore.GitIgnore`.
2. Dot-prefixed components or `.egg-info` suffixes in the path.
3. Known text extension whitelist (go, js, ts, py, md, json, yaml, etc.).
4. Binary detection: opens the file and scans 1024 bytes for null bytes; returns `true` if any are found.

If `SafeResolve` fails during classification, returns `true`.

#### Source Discovery and Binary Detection

**DiscoverCodeFiles** recursively walks the repo root via `filepath.WalkDir`, skipping directories matching gitignore patterns or dot-prefixed names. For each file, it runs the ignore classification; surviving relative paths are collected into a result list. Walk callback errors (directory traversal failures) are logged to `os.Stderr` and skipped (`return nil`). The final `(files, err)` from `filepath.WalkDir` is propagated as-is — callers receive whatever error the walker produced at completion.

**IsBinaryFile** opens a file and reads 1024 bytes (constant `binaryDetectionBufSize = 1024`). Any `os.Open` error returns `true` (assumes binary). Reads other than `io.EOF` return `true`. Clean read with no null bytes returns `false`.

#### Constants (`file_tools.go`)

| Constant | Value | Description |
|---|---|---|
| `binaryDetectionBufSize` | 1024 | Buffer size for binary file detection |
| `defaultDirPerm` | 0755 | Default directory permission mode |
| `defaultFilePerm` | 0644 | Default file permission mode |

#### Error Propagation (`file_tools.go`)

- **Defensive wrapping**: `ReadFileSafely` and `WriteFileSafely` wrap every OS error with a descriptive prefix via `fmt.Errorf("...: %w", err)`.
- **Swallowed/logged only**: WalkDir errors in `DiscoverCodeFiles` are written to stderr and not returned. LoadGitignore treats non-existence as success. `ShouldIgnoreFile` returns `true` on resolution failure rather than propagating the error.
- **Implicit assumptions**: `IsBinaryFile` assumes binary state when open or read errors occur.

---

### Git Operations: `git_tools.go`

#### Public API Surface

**RunGit(repoRoot string, args ...string)** executes an arbitrary git command within `repoRoot`. Prepends `--no-pager` to disable pager wrapping. Captures stdout and stderr into memory buffers (`bytes.Buffer`). On success returns `(trimmed string, nil)`. On failure returns `(trimmed string, error)` where the error message includes stderr content.

**VerifyGitRepo(repoRoot string)** validates that `repoRoot` is inside a git working tree by invoking `git rev-parse --is-inside-work-tree`. If `RunGit` fails, wraps as `"not a git repository (or any of the parent directories)"`; underlying stderr from git is discarded at this layer.

#### Subprocess Interaction Model

The only external system interaction in this file is spawning `git` as a child process via `exec.Command("git", ...)` and waiting for completion with `cmd.Run()`. All other I/O (reading `.git` objects, filesystem traversal) occurs inside the git binary itself.

#### Error Propagation (`git_tools.go`)

- `cmd.Run()` error + non-zero exit → wrapped with both stderr text and exit code in a single `fmt.Errorf`.
- Success with output → `(trimmed string, nil)`.
- No panic/recover patterns — errors propagate as Go `error` interface values.