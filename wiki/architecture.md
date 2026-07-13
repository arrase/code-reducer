# Global Architecture Overview

## System Boundaries & Data Flow

Code Reducer is structured as four independent subsystems with no shared mutable state: **config**, **engine**, **security**, and **tools**. Each module receives caller-provided arguments, operates on local state only, and returns results—no synchronization primitives cross boundaries.

**Data flow**: `internal/config` resolves a fully-populated configuration → `internal/engine` consumes that config to drive an LLM pipeline → `internal/security` enforces path containment for all filesystem operations the engine performs → `internal/tools` provides file I/O and git discovery primitives used by both security and engine.

## Entry Point

The binary entry point (`main.go`) is a thin wrapper: read error from Execute() → print to stderr → exit with code 1. The `cmd` module defines the root cobra command, registers subcommands (init, update, setup), resolves configuration from merged sources, routes engine events to stdout/stderr, handles OS signals for graceful shutdown, and delegates all substantive work to an unshown executeCommand entry point.

**Signal handling**: A handler captures INT and TERM via signal.NotifyContext. A deferred call ensures context cancellation when a signal arrives. The engine runner's returned error is wrapped as `"documentation run failed: …"`.

## Configuration Lifecycle

`config.go` declares three exported types: `ExtractionStep`, `Config`, and compile-time constants for environment variable keys, Ollama defaults (`OllamaDefaultBaseURL = "http://localhost:11434"`, `OllamaDefaultModelID = "ornith:9b"`), file-system defaults (`DefaultDocsDir = "wiki"`, `ConfigFileName = ".code-reducer.yaml"`), and multiline prompt templates.

`io.go` provides three exported functions: `ConfigExists(cwd string) bool`, `LoadConfig(cwd string) (*Config, error)`, and `SaveConfig(cwd string, cfg *Config) error`.

**Atomic Write Pattern for SaveConfig**: The save operation does not write directly to the target path. Sequence: marshal → apply formatting normalization (collapsing single blank lines before known section headers: `system_prompt`, `module_synthesis_prompt`, `architecture_prompt`, `file_fact_consolidation_prompt`, `extraction_steps`, `ignore`) → create temp file matching `.tmp.*` in same directory → write normalized YAML → sync and close → set permissions via `os.Chmod` → atomically rename temp over original. A deferred function calls `tmpFile.Close()` and `os.Remove(tmpName)`. On success, `Close` runs twice on the temp file—Go allows this safely—and the defer's `Remove` returns an error that is silently swallowed because the original config now holds the renamed data.

**Load-Save Roundtrip Integrity**: Loading preserves whatever formatting was originally written; normalization is applied only on write, not read. This decouples the in-memory representation from on-disk formatting expectations. `LoadConfig` returns a fresh `*Config` via unmarshaling with no shared state. Errors from `os.ReadFile` propagate directly; YAML parse failures are wrapped with `"failed to parse yaml config"` prefix chained via `%w`; marshal failures similarly wrap a descriptive message.

**Multi-Source Configuration Resolution**: `resolve.go` exposes a single exported function: `ResolveConfig(repoRoot, modelIDFlag, numCtxFlag string) (*config.Config, error)`. Internal helper `mergeAndDeduplicate` is lowercase and not part of the public surface. Only usage (not definition) of package-level types (`Config`, `OllamaDefaultModelID`, `CodeReducerModelIDEnvKey`, `DefaultExtractionSteps`) is visible here.

**Resolution Priority System**: Each configurable field follows its own independent priority chain, lowest to highest: **defaults → YAML config file → environment variables → CLI flags**. The merge-and-deduplicate logic operates on local function scopes with no cross-goroutine visibility and no shared mutable state.

**Field-Specific Priority Chains**: `ModelID` applies Default > YAML > Env Var > CLI Flag. `OllamaBaseURL` applies Default > YAML > Env Var. `OllamaNumCtx` (context size) applies Default > YAML > Env Var > CLI Flag. `DocsDir` applies Default > YAML. System prompts (`SystemPrompt`, `ModuleSynthesisPrompt`, `ArchitecturePrompt`, `FileFactConsolidationPrompt`) all start with default, then override with YAML if non-empty.

**Validation Rules**: Numeric fields: CLI flag and env var values are parsed as integers; a value must be greater than zero to take effect. Invalid or non-positive values fall through silently. String fields: empty string is treated as "not set" — the lower-priority default remains active. Config file absence (config.yml): not an error; if missing, an empty `Config{}` struct is created and defaults are applied. Other parse errors are surfaced.

**Algorithmic Flow for ResolveConfig**: (1) Load YAML config — if it exists and parses successfully; otherwise start with an empty struct (missing-file case). (2) Resolve extraction steps — use YAML value if present, else fall back to `DefaultExtractionSteps`. (3) Deduplicate ignore list from the loaded config using a map-based seen-set while preserving first-seen order. (4) For each field, apply its specific priority chain: start with built-in default constant → override with YAML (if non-empty) → override with environment variable (if set and valid for numeric types) → override with CLI flag (highest priority; numeric types require successful integer parse and positive value). (5) Return the fully resolved `Config` struct, or an error if the YAML file fails to parse (excluding missing-file case).

**Error Handling in ResolveConfig**: When `os.IsNotExist(err)` is true during config loading, defaults apply with no error returned. Any other error (parse failure, permission denied) is wrapped with a prefix and returned as `(*Config, nil, error)` — the config pointer is `nil`. Silent branches: string comparisons (`!= ""`, `> 0`) short-circuit without errors. `strconv.Atoi` failures are caught inline; env/flag values simply not used. Map lookups for deduplication have no error path.

**Always-On Return**: The function always returns a non-nil tuple `(*Config, error)` — either a populated config with `nil` error, or a `nil` config with an error string. No panics anywhere in this file.

## Engine Pipeline Architecture

### Constants & Types

`constants.go` declares compile-time constants: `defaultHTTPTimeout` (10 min), `maxErrorBodyBytes` (1024 bytes), `defaultChunkOverlap` (800 chars), `minNumCtxFloor` (512 tokens), `contextWindowAllocRatio` (0.75), `maxCharsMultiplier` (3), `metadataFileName` (`.metadata.json`), `agentsFileName` (`AGENTS.md`), `defaultDirPerm` (0755). Type alias `LogEventFunc` carries signature `func(EventType, string)` with no business rules defined here.

### LLM Client Communication

Unexported except `Message` struct (`Role string`, `Content string`). The package-private `llmClient` carries: model ID, base URL, context count, HTTP client pointer. All set once during construction; never modified afterward within this file. No mutexes or channels used.

**Algorithmic Flow**: (1) Prepare payload: assemble request body by prepending `systemPrompt` as a system-role message before any user-supplied messages, serialize as JSON to Ollama `/api/chat`. (2) Send HTTP request: single synchronous POST with fixed timeout; no retries (fail-fast policy). (3) Handle response: on 200 OK → deserialize body → extract `message.content` field. Non-2xx status code → error string containing both HTTP status and raw response payload. (4) Return result: extracted text content or wrapped error describing what went wrong.

**Error Handling Patterns**: Marshal failure wraps via `%w` for caller inspection via `errors.Is`. HTTP request construction returns unwrapped; context lost for callers. Network/transport error returns unwrapped; timeout vs DNS vs connect indistinguishable to caller. Success-path body read returns unwrapped (rare). JSON unmarshal failure wraps via `%w`. Non-OK status response uses custom message discarding read error (`_`); uses undefined `maxErrorBodyBytes` constant. No retry logic.

### Runner & Entry Point

`Runner` wraps a config pointer and exposes a single `Run(ctx, repoRoot, mode, onEvent)` method that is the only public entry point into the pipeline. It performs four sequential operations: (1) Gitignore lockfile maintenance — attempts to append the lockfile path to `.gitignore`. Failure is logged via `onEvent(EventStatus, ...)` and execution continues; no error returned. (2) Repository lock acquisition — calls `security.AcquireLock(repoRoot)`. On failure, returns wrapped error: `"failed to acquire repository lock: %w"`. No partial work proceeds without the lock. (3) Mode dispatch — constructs an LLM client from config fields (`cfg.ModelID`, `cfg.OllamaBaseURL`, `cfg.OllamaNumCtx`) and instantiates an `orchestrator`. Dispatches to either `RunInit` or `RunUpdate`; any other mode returns `"unsupported mode: %s"`. (4) Deferred lock release — `defer lock.Unlock()` runs after the pipeline completes, regardless of success or failure.

The `Runner.cfg` field is set once in `NewRunner(cfg)` and never mutated within this file. The pointer type allows external reassignment but no such mutation occurs here.

### Orchestrator & Pipeline Modes

Unexported struct with two exported constants on the `EventType` type alias: `EventStatus = "status"` and `EventError = "error"`. The unexported `orchestrator` struct carries a method receiver; its public surface is empty.

**RunInit — Cold Start Flow**: (1) setupPipeline() → discover files, compute hashes, load .gitignore (swallowed), load cache (swallowed). (2) Mark every directory as affected (full regeneration). (3) Synthesize root summary from full tree via LLM calls. (4) Generate architecture.md + quickstart.md using root summary. (5) Write agent guidelines file (append if marker missing, overwrite otherwise). (6) Persist cache to disk → complete.

**RunUpdate — Incremental Refresh Flow**: (1) setupPipeline() → discover current files, compute hashes. (2) If extraction steps changed → invalidate entire cache (.StepsHash mismatch resets Files and Modules maps). (3) Classify all discovered files into Added/Modified/Deleted buckets by comparing current vs cached SHA-256 hashes. (4) Build directory tree from allowed (existing + new) files only. (5) Purge stale module summaries: for any `cache.Modules` path not represented in the live tree, delete both the cache entry and the physical `.md` file (`_ = os.Remove(...)` — error swallowed). (6) Compute affected directories; propagate upward through parent dirs. (7) If `len(affectedDirs) == 0` → return early (no-op, docs are up to date). (8) Synthesize root summary from the affected region via LLM calls. (9) Re-generate architecture.md + quickstart.md only if `.` is in `affectedDirs`, or either file does not exist on disk, or resolution fails (treated as "not exists"). (10) Persist cache → complete.

**Pipeline Context**: Unexported struct carrying: LLM client pointer, repo root string, config pointer, `*MetadataCache` pointer, affected directory set map, precomputed file hashes map, and event logger callback. Constructed fresh per invocation; no shared instances exist across goroutines in this file.

### Cache Persistence Layer

Two structs with no methods: **`FileCacheEntry`** — pairs a SHA256 digest string (`sha256`) with extracted facts string (`facts`). Stored in `MetadataCache.Files map[string]FileCacheEntry`. **`MetadataCache`** — carries version int, steps hash string, files map, and modules map (module dir → synthesized summary).

**IsInitialized(repoRoot, docsDir) bool**: Returns true only if the cache file exists AND is readable in one call. Reuses the existence check from `loadMetadataCache`. No error return.

**saveMetadataCache(repoRoot, docsDir, cache) error**: Serializes to `{docsDir}/{metadataFileName}` with indented JSON (`json.MarshalIndent(cache, "", "  ")`). Marshaling errors propagate directly; write errors pass through `tools.WriteFileSafely`. The version field is pinned to the current value on every save.

**Version Compatibility Guard**: On load: if `cache.Version != currentCacheVersion` (currently 1), returns a fresh zero-valued `MetadataCache{}` with nil error. No warning, no panic—caller sees success with an empty cache. This prevents stale metadata from corrupting future runs without explicit migration handling.

**Fault-Tolerant Initialization**: If the file does not exist (`os.ErrNotExist` sentinel matched via `errors.Is`), returns a populated zero-valued cache with nil error. If it exists but cannot be read or unmarshaled, returns wrapped error `"failed to read metadata cache: %w"`. Callers can distinguish "not yet initialized" from "corrupt data."

### Chunking & Reduction Engine

All unexported; package-private. Implements a **context-aware recursive reduction** for LLM-based synthesis: (1) Split: Input list divided into batches whose combined character count fits within `contextSize × maxCharsMultiplier`. (2) Reduce each batch: Each batch sent to LLM via domain-specific prompt → one summary string per batch. If only one batch exists, returned directly; otherwise summaries concatenated and fed back into step 1 recursively until a single result remains. (3) Graceful truncation: Items exceeding `maxChars` are truncated with `"...\n...[truncated]"`. If every item exceeds the per-item budget (`allowedPerItem`), all items uniformly truncated to avoid infinite loops. (4) Context cancellation: Checked at entry points and before each recursive reduction round; cancellation errors propagate as-is.

**chunkTextWithOverlap**: Text-splitting utility with overlap between adjacent chunks. Appears available for reuse elsewhere but not referenced by any function here.

### Synthesis Core

All types and functions unexported (`pipelineContext`, `synthesizeNode`). Package-private.

**Recursive Directory Synthesis**: A directory's summary is derived by recursively synthesizing children (subdirectories) and files together. Final result stored in cache under `cache.Modules[node.Path]` and written to disk at `docs/modules/<safe-filename>`.

**Caching Strategy with Hash-Based Invalidation**: Module-level: Stores synthesized summary per directory path. Reused when affected status indicates no changes (short-circuit return). File-level: Stores SHA256 hash + extracted facts in `cache.Files[f]`. Cache hit determined by comparing precomputed or read file hash against cached entry; only re-reads on miss.

**Multi-Step Extraction Pipeline Per File Chunk**: (1) System prompt augmented with current extraction step's prompt. (2) Single LLM call extracts facts from chunk → one fact per step. (3) All steps produce respective facts for the same file → consolidated via `reduceFileFacts`.

**Synthesis Order & Assembly**: (1) File facts (sorted child name order). (2) Child subdirectory summaries (after all files processed). (3) Combined list reduced to final summary via `reduceInChunks` at directory level.

**Affected-Dir Pruning**: If a directory path has no affected status AND a cached module summary exists → short-circuit return cached value without reprocessing children or files.

### Tree & Change Detection

Exported types: **`FileChange`** — `Path string`, `Status string` ("Added", "Modified", "Deleted"). **`DirNode`** — `Path string`, `Files []string`, `Children map[string]*DirNode`.

**Three-Phase Algorithm**: (1) Build tree: Given raw file paths, split each on `/`, create intermediate `DirNode` entries for subdirectories, attach files to leaf nodes via `buildTree`. (2) Determine affected directories from changes: For each input change, mark directly changed file as affected (if exists in current set). When a file is Deleted, mark parent directory as affected. Walk recursively and check additional conditions: if node path matches `docs/modules/<safe-name>` but no such doc exists on disk → affected; if node has empty cached metadata entry → affected. (3) Propagate status upward: Recursive walk of `DirNode` tree propagates any `affected` flag from children to parents so a parent is considered affected if any child is.

### JSON Parser Utility

**stripOuterMarkdownFence(input) string**: Single-pass parser that strips markdown or JSON code fences from strings: (1) Trims whitespace from input. (2) Attempts regex match against pattern capturing triple backticks with 3+ chars, optionally followed by `markdown` or `json`, then all content between opening and closing fences (group 1). (3) If matched → returns trimmed captured inner content. (4) If no match → returns original trimmed input unchanged. No error return parameter. `regexp.MustCompile` panics on invalid pattern at package init—unhandled within this file, propagates to any caller importing the package.

### Utility Functions for Filename Generation and Event Logging

This file provides two unexported utility functions supporting the engine's documentation generation pipeline and optional event logging instrumentation. Neither function communicates with external systems, modifies shared state, or returns errors. All operations are pure in-memory transformations.

**toSafeMarkdownFilename — Module Path to Markdown Filename Conversion**: Transforms a module path into a safe markdown documentation filename, enforcing the engine's implicit one-to-one mapping contract between modules and their `.md` documentation files. The root module is special-cased to default to `root.md`. Data flow: (1) Input: a raw string representing a module path (e.g., `"internal/engine"`). (2) Character substitution: every `/` character in the input is replaced with `_`. (3) Extension appended: `.md` is concatenated to produce the final filename. (4) Output: a single `string` containing the resulting markdown filename. No filesystem, network, database, or other external I/O occurs during execution. The function returns only a result string; no error type is returned. Invalid input characters (anything other than `/`) are passed through unmodified — callers receive no escape hatch for detecting malformed paths. Pure: no mutable state, no package-level globals modified, no struct fields touched.

**makeLogEvent — Optional Event Callback Adapter**: Wires an optional callback into a logging pipeline. When invoked with an event type and message pair, it either forwards the call to the registered handler or silently discards it. This makes the logging subsystem purely additive — no required consumer exists for the engine's log events; instrumentation is opt-in only. Data flow: (1) Input: a user-provided `onEvent` callback of type `LogEventFunc`. (2) Closure construction: returns a new function accepting `(EventType, message)` pairs. (3) Invocation behavior: if `onEvent == nil`, the returned closure does nothing when called — no panic, no error, no log output. If `onEvent` is non-nil, the call is forwarded to it with the provided type and message arguments. No I/O anywhere in this flow. Pure: no mutable state, no package-level globals modified. Silent failure path: when `onEvent == nil`, events are swallowed without notice or error wrapping. The returned function is always valid (non-nil), so callers can invoke it safely regardless of whether a handler was registered.

**Referenced Types**: The following types are referenced in this file but not defined within it: `EventType` — categorizes log events into discrete types; carried as the first argument to event handlers and consumed by the returned closure from `makeLogEvent`. `LogEventFunc` — function signature for user-provided event handlers. Expected to accept `(EventType, message string)` or equivalent parameters. `Event` (struct) — domain model with at least two fields: `Type` (`EventType`) and `Message` (`string`). Events are tracked as discrete, typed records carrying descriptive payloads.

**Concurrency & State Summary**: Mutable state: None. Both functions operate entirely on their inputs and return values without touching package-level variables or struct fields. Concurrency mechanisms: None. No mutexes, channels, atomic operations, or synchronization primitives are used in this file.

## Security Invariants

### Module Responsibility

This module enforces two non-negotiable invariants for any code-reducer session: (1) the working directory must remain strictly within the repository root boundary, and (2) only one process may hold an exclusive lock on the shared resource at any given moment. It also maintains a `.gitignore` entry that excludes the lockfile from version control while ensuring it is automatically tracked if absent.

The module communicates with the outside world indirectly: `security.go` performs filesystem I/O and returns sentinel error values to callers; those sentinels are defined in `errors.go`. No panic recovery, no error wrapping beyond `%w`, no silent swallowing of non-trivial failures occurs within these files.

### Sentinel Error Contract

Package-level variables: **`ErrPathTraversal`** (`error`) — returned when a resolved path escapes outside the repository root boundary. **`ErrLockHeld`** (`error`) — returned when another code-reducer process holds an exclusive file lock on the shared resource. Both variables are initialized once via `errors.New` and remain read-only throughout their scope. No mutable state, no concurrency primitives, no panic handling exists in this file. Callers detect violations by matching against these sentinels using `errors.Is(err, ErrPathTraversal)` or equivalent patterns; because the errors are unwrapped as-is (no wrapping), identity is preserved directly through the error chain.

**Domain Concepts**: Path Traversal Violation (`ErrPathTraversal`) — signals when a resolved path escapes outside the repository root boundary. This is a classic path traversal / directory escape guardrail. Lock Conflict (`ErrLockHeld`) — signals when another process already holds an exclusive file lock, indicating contention on a shared resource.

### Lock Acquisition and Release

**Struct Definition**: `SimpleLock` carries: `lockPath string` (absolute path to the lockfile on disk), `file *os.File` (open file descriptor for the lockfile, or nil if not acquired), `mu sync.Mutex` (protects internal struct fields during `Unlock()`), `closed bool` (marks whether the lock has been released and the file descriptor closed).

**`*SimpleLock.Unlock()`**: Closes the lock file descriptor and removes the lockfile in a thread-safe manner. **Idempotency contract**: Calling `Unlock()` on an already-closed lock returns `nil`. This is intentional, not accidental. Error handling inside the method: If `l.closed == true` → return `nil`. Close the file descriptor (`os.File.Close`) — error captured in local variable; if close fails, set `file = nil` so a subsequent close becomes a no-op. Remove the lockfile via `os.Remove`. If both close and remove fail: remove error overwrites prior close error only when close returned without error. If close failed first, any remove error is ignored. This means the final returned error reflects whichever operation last produced an error, not necessarily the most severe one — an intentional design choice worth flagging for callers. If `os.IsNotExist(err)` on remove → silently accepted as normal idempotent cleanup.

### Path Resolution

**Signature**: `func SafeResolve(repoRoot, inputPath string) (string, error)`. **Responsibility**: Resolve any input path strictly inside the repository root. The function performs a multi-step containment check to defeat symlink-based escape vectors and physical directory traversal.

**Algorithmic Steps**: (1) Call `filepath.Abs` on `inputPath`. (2) Evaluate symlinks on the existing ancestor via `EvalSymlinks` — prevents symlink-based escape from parent directories that exist but are symbolic links. (3) Walk up from the target until a physically-existing directory is found, then re-evaluate symlinks on that ancestor via `EvalSymlinks` again. (4) Rejoin with the remaining suffix components after the physical ancestor boundary. (5) Verify final result is under `resolvedRoot` via relative path check (`filepath.Rel`).

**Error Handling**: Any error from `Abs`, first `EvalSymlinks`, second `EvalSymlinks`, or `Lstat` (non-exist) wraps with descriptive message using `fmt.Errorf(..., err)` — underlying cause recovered via `%w`. Unrecognized error from `Lstat` (`!os.IsNotExist`) returns immediately as wrapped error. Path escapes repo root (`..` prefix or non-nil rel) returns sentinel-style error wrapping `ErrPathTraversal` with the original input path.

### Lock Acquisition

**Signature**: `func AcquireLock(repoRoot string) (*SimpleLock, error)`. **Responsibility**: Establish an exclusive process-level lock on a shared resource. Uses `O_EXCL` flag for atomic creation of the lockfile — if the lockfile already exists, acquisition fails immediately without silent success.

**Lockfile Lifecycle**: Resolve path calls `SafeResolve(repoRoot)` to obtain absolute lock path. Create atomically opens file with `O_WRONLY | O_CREATE | O_EXCL` — atomic exclusive creation prevents race between existence check and open. Record identity writes PID of the acquiring process to the newly created lockfile. Failure cleanup on failure: closes file, removes lockfile.

**Error Handling**: `SafeResolve` fails propagates as-is to caller. File open fails AND error is `os.IsExist` (lock already held) returns wrapped sentinel error wrapping `ErrLockHeld`, including the lock path — caller can detect stale-lock condition specifically via `errors.Is(err, ErrLockHeld)`. File open fails with any other error wraps: `"failed to acquire lock at %s: <cause>"`. Write PID fails file closed, lockfile removed (cleanup), wrapped error returned.

### Gitignore Maintenance

**Signature**: `func EnsureGitignoreHasLockfile(repoRoot string) (error)`. **Responsibility**: Maintain the convention that the lockfile is excluded from version control but automatically tracked if missing. Appends the lockfile entry to `.gitignore` only if not already present; uses atomic rename via temp file + sync before close to prevent partial writes.

**Algorithmic Steps**: (1) Resolve `.gitignore` path via `SafeResolve(repoRoot)`. (2) Read existing `.gitignore` content (`os.ReadFile`). (3) Parse lines, check if lockfile entry already exists. (4) If not present: construct new content with the lockfile entry appended (with trailing-newline normalization). (5) Create a temp file in same directory as `.gitignore`. (6) Write to temp file → call `Sync()` on it → close it → `Chmod` it. (7) Rename temp file over original `.gitignore`.

**Error Handling**: `SafeResolve` fails propagates as-is. Read fails AND not `os.IsNotExist` (file exists but read error) wraps: `"error reading .gitignore: <cause>"` — caller must handle missing-file vs I/O-error distinction separately. CreateTemp fails wraps with descriptive message including `%w`. Write to temp file fails wraps; temp file is NOT cleaned up (defer handles close + remove, but write error returns before cleanup path executes). Sync fails wraps — note that `os.Chmod` and subsequent rename may have already run on a partially-written temp file if this error occurs late in the sequence. Close fails wraps. Chmod fails wraps. Rename (atomic swap) fails wraps with descriptive message.

The deferred function (`tmpFile.Close()` + `os.Remove(tmpName)`) runs regardless of return path. Because the function returns before the defer has a chance to execute in some failure paths, cleanup may not happen for intermediate states — but since we're using temp file with unique name and same-directory rename, the OS-level temp file is cleaned by `os.Remove` in the defer on any error return path. If *write* fails mid-stream before close/rename, the deferred Remove will clean up the partially-written temp file.

### Data Flow Summary

```
[Initialize] → [Ensure path safety for all repo-root operations (SafeResolve)] → 
[If no process holds the lock] → [Create lockfile with PID via O_EXCL (AcquireLock)] → 
[Run business logic within locked session] → 
[Append entry to .gitignore if needed (EnsureGitignoreHasLockfile)]
```

The two non-obvious constraints are: (1) path resolution walks symlinked ancestors before rejoining suffix components, and (2) gitignore updates use atomic rename with explicit sync before close.

**Key Patterns Observed Across the Module**: Error wrapping throughout — All functions use `fmt.Errorf(..., err)` with `%w`, making root-cause recovery possible via `errors.Is()`. Custom sentinel errors — At least two custom error variables referenced: `ErrPathTraversal` and `ErrLockHeld`. These enable typed error detection by callers without requiring unwrapping logic on this file's side. No panic anywhere — All failure paths return to caller with an error value. Idempotent operations deliberately silent — `Unlock()` on already-closed lock returns nil; missing `.gitignore` in EnsureGitignoreHasLockfile returns nil. These are design choices, not accidentals. Atomic rename pattern for .gitignore — Write to temp → Sync → Close → Chmod → Rename is the standard atomic-update pattern. The `Sync()` call before close is explicit and unusual (normally deferred), but here it's called eagerly before close in this implementation. No global package-level mutable state exists in either file — all state is either local to a function or encapsulated within the `SimpleLock` receiver, which is accessed only through its methods.

## Tools Subsystem

### Module Responsibility

The `internal/tools` package provides repository-scanning primitives for a codebase-analysis tool. It exposes two functional domains: **safe local file I/O** (`file_tools.go`) and **git subprocess orchestration** (`git_tools.go`). Together they enable the caller to discover source-code files, classify their content type, read/write artifacts safely, and validate that the target directory is a git work-tree before proceeding.

Data flow follows a two-phase pattern: (1) `VerifyGitRepo` (or an equivalent pre-flight check) confirms the working directory corresponds to a valid git repository by invoking `git rev-parse --is-inside-work-tree`. (2) Once validated, `DiscoverCodeFiles` walks the entire tree under that root, applying layered ignore rules and returning only text-source files. Both domains share these architectural constraints: **no panics**, **error wrapping** (stderr is preserved for diagnostics), and **no network/DB calls**. All I/O operates within a single local process or on the local filesystem.

### File Operations

**Safe Read / Write Primitive**: `ReadFileSafely` and `WriteFileSafely` implement TOCTOU-safe file operations using the temp-file + rename pattern. Shared sequence: (1) Resolve a virtual path to an absolute filesystem path via `SafeResolve`. (2) On write: create parent directories with `defaultDirPerm`; on read: no directory creation. (3) Open or create a temporary file in the target directory using `os.CreateTemp`. (4) Write content (for writes) or read from the resolved path (for reads). (5) Close and sync (writes only); close (reads only). (6) Rename temp into final destination for writes; return contents for reads.

**Symlink detection**: Both paths verify the target is not a symlink via `os.Lstat`/`f.Stat`. Pre-open checks prevent TOCTOU swap attacks where an attacker replaces a real file with a symlink between resolution and open. Post-open checks catch any state changes after the initial open.

**Error semantics**: `ReadFileSafely`: returns `nil, err` on failure; wraps each step with context strings (`failed to open file`, `failed to lstat`, etc.). Symlink detection yields an explicit "security violation" error rather than a generic I/O error. `WriteFileSafely`: all failure paths return wrapped errors (directory creation, temp write, sync, close, chmod, rename). No panic usage; no sentinel values. Deferred cleanup: Both functions defer `f.Close()` for reads and use deferred `os.Remove` in `WriteFileSafely` to clean up temp files if the rename fails.

**Gitignore Handling**: **`LoadGitignore(repoRoot)`**: Opens `.gitignore` under `repoRoot`. If absent (`os.IsNotExist`), returns `nil, nil` — no error raised. Any other open/read failure is returned unwrapped. **`ShouldIgnoreFile(repoRoot, relPath, gitIgnore)`**: Applies layered rules: (1) Check against compiled `GitIgnore` patterns (user-supplied + `.gitignore`). (2) Reject any path component starting with `.` or ending with `.egg-info`, regardless of other rules. (3) If extension matches a known text-source language list, accept as code. (4) For unknown extensions: resolve absolute path and scan first 1024 bytes for null bytes; binaries return `true`.

**Side effect note**: `ShouldIgnoreFile` opens the file on every invocation to classify unknown extensions — this is a real I/O side effect per call, not cached.

### Code Discovery Pipeline

**Input**: repository root + list of ignore patterns (strings). **Output**: slice of relative source-code paths.

**Algorithmic flow**: (1) Compile user-supplied ignore patterns and `.gitignore` entries into a single `GitIgnore` object via `LoadGitignore`. (2) Walk the entire tree recursively using `filepath.WalkDir`. For each entry: Directory: if name starts with `.` or matches any compiled pattern, skip subtree entirely. File: delegate to `ShouldIgnoreFile`. (3) Collect passing files into result slice.

**Walk error handling**: If a walk callback returns an error for a single entry, the error is printed to `stderr` and that item is skipped (`return nil`). The overall `err` from `WalkDir` propagates at the end — terminal fatal errors are returned; non-fatal entries are swallowed silently.

### Binary Classification

**IsBinaryFile**: Scans first 1024 bytes for null bytes to distinguish text source files from binary content. Returns `true` (ignored) on any I/O error other than EOF — this "fail-closed" assumption treats unreadable files as likely binary rather than propagating the actual error.

**Constants**: `binaryDetectionBufSize` is lowercase and package-private; not exported.

### Git Tools

**External Command Execution (`RunGit`)**: Spawns `git --no-pager` via `exec.Command` with any additional arguments from the caller. The subprocess runs within `repoRoot` as its working directory. Stdout/stderr are captured into in-memory `bytes.Buffer`; no file, network, or DB I/O occurs.

**Failure handling**: On error, wraps the original with stderr content:
```
fmt.Errorf("git command failed: %v, stderr: %s", err, trimmedErr)
```
Returns `(string, error)` — partial stdout is still delivered alongside the error. No panics.

**Repository Validation (`VerifyGitRepo`)**: Delegates to `RunGit` with subcommand `rev-parse --is-inside-work-tree`. If this invocation results in an error, returns a fixed message: `"not a git repository (or any of the parent directories)"`. The original stderr is discarded at this wrapper layer — normalized to a single sentinel-style message for downstream callers.

### Concurrency & State Analysis

**Mutable state**: None detected across both files. All constants are `const` (compile-time); all variables (`safePath`, `dir`, `tmpFile`, `tmpName`, `patterns`, `files`, `buf`, `cmd`, `stdout`, `stderr`, etc.) are function-local and never escape their defining scope. No package-level global state is modified.

**Concurrency mechanisms**: None detected. No `sync.Mutex`, channels, `atomic` types, or other synchronization primitives. Shared mutable state does not exist to protect.

### Error Handling Summary

| Pattern | Where |
|---|---|
| Wrap-and-return (preserve stderr) | `RunGit` |
| Normalize to sentinel message | `VerifyGitRepo` |
| Sentinel + wrap with context | `ReadFileSafely`, `WriteFileSafely`, `LoadGitignore` |
| Boolean return, resolve failures as "ignored" | `ShouldIgnoreFile` |
| Swallow per-entry walk errors, propagate terminal | `DiscoverCodeFiles` |
| Fail-closed (I/O error → binary assumption) | `IsBinaryFile` |

**No panics** observed across either file. All errors are returned to callers as Go values.