# Code Reducer — Quick Start Guide

## System Overview

Code Reducer is a multi-phase LLM analysis pipeline that generates technical documentation from Go source code. The system spans four subsystems: **config**, **engine**, **security**, and **tools**. No shared mutable state exists across subsystem boundaries—each module operates on caller-provided arguments and returns results without synchronization primitives.

## Entry Point

The binary entry point (`main.go`) is a thin wrapper that reads an error from `Execute()`, prints it to stderr, and exits with code 1. The `cmd` module defines the root cobra command, registers subcommands (init, update, setup), resolves configuration from merged sources, routes engine events to stdout/stderr, handles OS signals for graceful shutdown (INT + TERM via `signal.NotifyContext`), and delegates all substantive work to an unshown executeCommand entry point.

## Configuration Lifecycle

### Resolution Priority Chain

Each configurable field follows its own independent priority chain, lowest to highest: **defaults → YAML config file → environment variables → CLI flags**. The function always returns a non-nil tuple `(*Config, error)`. Config file absence is not an error; if missing, an empty struct is created and defaults are applied. Parse failures propagate as errors.

### Atomic Write Pattern for SaveConfig

The save operation does not write directly to the target path. Sequence: marshal into YAML bytes → apply formatting normalization (collapsing single blank lines to double blank lines before known section headers) → create a temp file matching `.tmp.*` in the same directory → write normalized YAML into it → sync and close → set permissions via `os.Chmod` → atomically rename temp over original. A deferred function calls `tmpFile.Close()` and `os.Remove(tmpName)`.

### Multi-Source Configuration Resolution

The public surface is a single exported function: `ResolveConfig(repoRoot, modelIDFlag, numCtxFlag string)` returning `(*config.Config, error)`. Internal helper `mergeAndDeduplicate` is lowercase. Only usage (not definition) of package-level types (`Config`, `OllamaDefaultModelID`, `CodeReducerModelIDEnvKey`, `DefaultExtractionSteps`) is visible here.

**Field-Specific Priority Chains**:
- `ModelID`: Default > YAML > Env Var > CLI Flag
- `OllamaBaseURL`: Default > YAML > Env Var
- `OllamaNumCtx` (context size): Default > YAML > Env Var > CLI Flag
- `DocsDir`: Default > YAML
- System prompts (`SystemPrompt`, `ModuleSynthesisPrompt`, `ArchitecturePrompt`, `FileFactConsolidationPrompt`): all start with default, then override with YAML if non-empty

**Validation Rules**: Numeric fields: CLI flag and env var values are parsed as integers; a value must be greater than zero to take effect. Invalid or non-positive values fall through silently. String fields: empty string is treated as "not set" — the lower-priority default remains active.

## Engine Pipeline Architecture

### Runner & Entry Point

`Runner` wraps a config pointer and exposes a single `Run(ctx, repoRoot, mode, onEvent)` method that is the only public entry point into the pipeline. It performs four sequential operations: (1) Gitignore lockfile maintenance — attempts to append the lockfile path to `.gitignore`. Failure is logged via `onEvent(EventStatus, ...)` and execution continues; no error returned. (2) Repository lock acquisition — calls `security.AcquireLock(repoRoot)`. On failure, returns wrapped error: `"failed to acquire repository lock: %w"`. No partial work proceeds without the lock. (3) Mode dispatch — constructs an LLM client from config fields (`cfg.ModelID`, `cfg.OllamaBaseURL`, `cfg.OllamaNumCtx`) and instantiates an orchestrator. Dispatches to either `RunInit` or `RunUpdate`; any other mode returns `"unsupported mode: %s"`. (4) Deferred lock release — `defer lock.Unlock()` runs after the pipeline completes, regardless of success or failure.

### Orchestrator & Pipeline Modes

**RunInit — Cold Start Flow**:
1. setupPipeline() → discover files, compute hashes, load .gitignore (swallowed), load cache (swallowed)
2. Mark every directory as affected (full regeneration)
3. Synthesize root summary from full tree via LLM calls
4. Generate architecture.md + quickstart.md using root summary
5. Write agent guidelines file (append if marker missing, overwrite otherwise)
6. Persist cache to disk → complete

**RunUpdate — Incremental Refresh Flow**:
1. setupPipeline() → discover current files, compute hashes
2. If extraction steps changed → invalidate entire cache (.StepsHash mismatch resets Files and Modules maps)
3. Classify all discovered files into Added/Modified/Deleted buckets by comparing current vs cached SHA-256 hashes
4. Build directory tree from allowed (existing + new) files only
5. Purge stale module summaries: for any `cache.Modules` path not represented in the live tree, delete both the cache entry and the physical `.md` file (`_ = os.Remove(...)` — error swallowed)
6. Compute affected directories; propagate upward through parent dirs
7. If `len(affectedDirs) == 0` → return early (no-op, docs are up to date)
8. Synthesize root summary from the affected region via LLM calls
9. Re-generate architecture.md + quickstart.md only if `.` is in `affectedDirs`, or either file does not exist on disk, or resolution fails (treated as "not exists")
10. Persist cache → complete

### Cache Persistence Layer

Two structs with no methods: **`FileCacheEntry`** — pairs a SHA256 digest string (`sha256`) with extracted facts string (`facts`). Stored in `MetadataCache.Files map[string]FileCacheEntry`. **`MetadataCache`** — carries version int, steps hash string, files map, and modules map (module dir → synthesized summary).

**Version Compatibility Guard**: On load: if `cache.Version != currentCacheVersion` (currently 1), returns a fresh zero-valued `MetadataCache{}` with nil error. No warning, no panic—caller sees success with an empty cache. This prevents stale metadata from corrupting future runs without explicit migration handling.

**Fault-Tolerant Initialization**: If the file does not exist (`os.ErrNotExist` sentinel matched via `errors.Is`), returns a populated zero-valued cache with nil error. If it exists but cannot be read or unmarshaled, returns wrapped error `"failed to read metadata cache: %w"`. Callers can distinguish "not yet initialized" from "corrupt data."

### Chunking & Reduction Engine

All unexported; package-private. Implements a **context-aware recursive reduction** for LLM-based synthesis: (1) Split: Input list divided into batches whose combined character count fits within `contextSize × maxCharsMultiplier`. (2) Reduce each batch: Each batch sent to LLM via domain-specific prompt → one summary string per batch. If only one batch exists, returned directly; otherwise summaries concatenated and fed back into step 1 recursively until a single result remains. (3) Graceful truncation: Items exceeding `maxChars` are truncated with `"...\n...[truncated]"`. If every item exceeds the per-item budget (`allowedPerItem`), all items uniformly truncated to avoid infinite loops. (4) Context cancellation: Checked at entry points and before each recursive reduction round; cancellation errors propagate as-is.

### Synthesis Core

All types and functions unexported (`pipelineContext`, `synthesizeNode`). Package-private.

**Recursive Directory Synthesis**: A directory's summary is derived by recursively synthesizing children (subdirectories) and files together. Final result stored in cache under `cache.Modules[node.Path]` and written to disk at `docs/modules/<safe-filename>`.

**Caching Strategy with Hash-Based Invalidation**: Module-level: Stores synthesized summary per directory path. Reused when affected status indicates no changes (short-circuit return). File-level: Stores SHA256 hash + extracted facts in `cache.Files[f]`. Cache hit determined by comparing precomputed or read file hash against cached entry; only re-reads on miss.

**Multi-Step Extraction Pipeline Per File Chunk**: (1) System prompt augmented with current extraction step's prompt. (2) Single LLM call extracts facts from chunk → one fact per step. (3) All steps produce respective facts for the same file → consolidated via `reduceFileFacts`.

**Synthesis Order & Assembly**: (1) File facts (sorted child name order). (2) Child subdirectory summaries (after all files processed). (3) Combined list reduced to final summary via `reduceInChunks` at directory level.

### Tree & Change Detection

Exported types: **`FileChange`** — `Path string`, `Status string` ("Added", "Modified", "Deleted"). **`DirNode`** — `Path string`, `Files []string`, `Children map[string]*DirNode`.

**Three-Phase Algorithm**: (1) Build tree: Given raw file paths, split each on `/`, create intermediate `DirNode` entries for subdirectories, attach files to leaf nodes via `buildTree`. (2) Determine affected directories from changes: For each input change, mark directly changed file as affected (if exists in current set). When a file is Deleted, mark parent directory as affected. Walk recursively and check additional conditions: if node path matches `docs/modules/<safe-name>` but no such doc exists on disk → affected; if node has empty cached metadata entry → affected. (3) Propagate status upward: Recursive walk of `DirNode` tree propagates any `affected` flag from children to parents so a parent is considered affected if any child is.

## Security Invariants

### Module Responsibility

This module enforces two non-negotiable invariants for any code-reducer session: (1) the working directory must remain strictly within the repository root boundary, and (2) only one process may hold an exclusive lock on the shared resource at any given moment. It also maintains a `.gitignore` entry that excludes the lockfile from version control while ensuring it is automatically tracked if absent.

### Sentinel Error Contract

Package-level variables: **`ErrPathTraversal`** (`error`) — returned when a resolved path escapes outside the repository root boundary. **`ErrLockHeld`** (`error`) — returned when another code-reducer process holds an exclusive file lock on the shared resource. Both variables are initialized once via `errors.New` and remain read-only throughout their scope. No mutable state, no concurrency primitives, no panic handling exists in this file. Callers detect violations by matching against these sentinels using `errors.Is(err, ErrPathTraversal)` or equivalent patterns; because the errors are unwrapped as-is (no wrapping), identity is preserved directly through the error chain.

### Lock Acquisition and Release

**Struct Definition**: `SimpleLock` carries: `lockPath string` (absolute path to the lockfile on disk), `file *os.File` (open file descriptor for the lockfile, or nil if not acquired), `mu sync.Mutex` (protects internal struct fields during `Unlock()`), `closed bool` (marks whether the lock has been released and the file descriptor closed).

**`*SimpleLock.Unlock()`**: Closes the lock file descriptor and removes the lockfile in a thread-safe manner. **Idempotency contract**: Calling `Unlock()` on an already-closed lock returns `nil`. This is intentional, not accidental. Error handling inside the method: If `l.closed == true` → return `nil`. Close the file descriptor (`os.File.Close`) — error captured in local variable; if close fails, set `file = nil` so a subsequent close becomes a no-op. Remove the lockfile via `os.Remove`. If both close and remove fail: remove error overwrites prior close error only when close returned without error. If close failed first, any remove error is ignored. This means the final returned error reflects whichever operation last produced an error, not necessarily the most severe one — an intentional design choice worth flagging for callers. If `os.IsNotExist(err)` on remove → silently accepted as normal idempotent cleanup.

### Path Resolution

**Signature**: `func SafeResolve(repoRoot, inputPath string) (string, error)`. **Responsibility**: Resolve any input path strictly inside the repository root. The function performs a multi-step containment check to defeat symlink-based escape vectors and physical directory traversal.

**Algorithmic Steps**: (1) Call `filepath.Abs` on `inputPath`. (2) Evaluate symlinks on the existing ancestor via `EvalSymlinks` — prevents symlink-based escape from parent directories that exist but are symbolic links. (3) Walk up from the target until a physically-existing directory is found, then re-evaluate symlinks on that ancestor via `EvalSymlinks` again. (4) Rejoin with the remaining suffix components after the physical ancestor boundary. (5) Verify final result is under `resolvedRoot` via relative path check (`filepath.Rel`).

**Error Handling**: Any error from `Abs`, first `EvalSymlinks`, second `EvalSymlinks`, or `Lstat` (non-exist) wraps with descriptive message using `fmt.Errorf(..., err)` — underlying cause recovered via `%w`. Unrecognized error from `Lstat` (`!os.IsNotExist`) returns immediately as wrapped error. Path escapes repo root (`..` prefix or non-nil rel) returns sentinel-style error wrapping `ErrPathTraversal` with the original input path.

### Lock Acquisition (Detailed)

**Signature**: `func AcquireLock(repoRoot string) (*SimpleLock, error)`. **Responsibility**: Establish an exclusive process-level lock on a shared resource. Uses `O_EXCL` flag for atomic creation of the lockfile — if the lockfile already exists, acquisition fails immediately without silent success.

**Lockfile Lifecycle**: Resolve path calls `SafeResolve(repoRoot)` to obtain absolute lock path. Create atomically opens file with `O_WRONLY | O_CREATE | O_EXCL` — atomic exclusive creation prevents race between existence check and open. Record identity writes PID of the acquiring process to the newly created lockfile. Failure cleanup on failure: closes file, removes lockfile.

**Error Handling**: `SafeResolve` fails propagates as-is to caller. File open fails AND error is `os.IsExist` (lock already held) returns wrapped sentinel error wrapping `ErrLockHeld`, including the lock path — caller can detect stale-lock condition specifically via `errors.Is(err, ErrLockHeld)`. File open fails with any other error wraps: `"failed to acquire lock at %s: <cause>"`. Write PID fails file closed, lockfile removed (cleanup), wrapped error returned.

### Gitignore Maintenance

**Signature**: `func EnsureGitignoreHasLockfile(repoRoot string) (error)`. **Responsibility**: Maintain the convention that the lockfile is excluded from version control but automatically tracked if missing. Appends the lockfile entry to `.gitignore` only if not already present; uses atomic rename via temp file + sync before close to prevent partial writes.

**Algorithmic Steps**: (1) Resolve `.gitignore` path via `SafeResolve(repoRoot)`. (2) Read existing `.gitignore` content (`os.ReadFile`). (3) Parse lines, check if lockfile entry already exists. (4) If not present: construct new content with the lockfile entry appended (with trailing-newline normalization). (5) Create a temp file in same directory as `.gitignore`. (6) Write to temp file → call `Sync()` on it → close it → `Chmod` it. (7) Rename temp file over original `.gitignore`.

**Error Handling**: `SafeResolve` fails propagates as-is. Read fails AND not `os.IsNotExist` (file exists but read error) wraps: `"error reading .gitignore: <cause>"` — caller must handle missing-file vs I/O-error distinction separately. CreateTemp fails wraps with descriptive message including `%w`. Write to temp file fails wraps; temp file is NOT cleaned up (defer handles close + remove, but write error returns before cleanup path executes). Sync fails wraps — note that `os.Chmod` and subsequent rename may have already run on a partially-written temp file if this error occurs late in the sequence. Close fails wraps. Chmod fails wraps. Rename (atomic swap) fails wraps with descriptive message.

The deferred function (`tmpFile.Close()` + `os.Remove(tmpName)`) runs regardless of return path. Because the function returns before the defer has a chance to execute in some failure paths, cleanup may not happen for intermediate states — but since we're using temp file with unique name and same-directory rename, the OS-level temp file is cleaned by `os.Remove` in the defer on any error return path. If *write* fails mid-stream before close/rename, the deferred Remove will clean up the partially-written temp file.

### Data Flow Summary (Security Module)

```
[Initialize] → [Ensure path safety for all repo-root operations (SafeResolve)] → 
[If no process holds the lock] → [Create lockfile with PID via O_EXCL (AcquireLock)] → 
[Run business logic within locked session] → 
[Append entry to .gitignore if needed (EnsureGitignoreHasLockfile)]
```

## Tools Subsystem

### Module Responsibility

The `internal/tools` package provides repository-scanning primitives for a codebase-analysis tool. It exposes two functional domains: **safe local file I/O** (`file_tools.go`) and **git subprocess orchestration** (`git_tools.go`). Together they enable the caller to discover source-code files, classify their content type, read/write artifacts safely, and validate that the target directory is a git work-tree before proceeding.

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

### Git Tools

**External Command Execution (`RunGit`)**: Spawns `git --no-pager` via `exec.Command` with any additional arguments from the caller. The subprocess runs within `repoRoot` as its working directory. Stdout/stderr are captured into in-memory `bytes.Buffer`; no file, network, or DB I/O occurs.

**Failure handling**: On error, wraps the original with stderr content:
```
fmt.Errorf("git command failed: %v, stderr: %s", err, trimmedErr)
```
Returns `(string, error)` — partial stdout is still delivered alongside the error. No panics.

**Repository Validation (`VerifyGitRepo`)**: Delegates to `RunGit` with subcommand `rev-parse --is-inside-work-tree`. If this invocation results in an error, returns a fixed message: `"not a git repository (or any of the parent directories)"`. The original stderr is discarded at this wrapper layer — normalized to a single sentinel-style message for downstream callers.

## Concurrency & State Summary

**Mutable state**: None across all modules. All constants are `const` (compile-time); all variables (`safePath`, `dir`, `tmpFile`, `tmpName`, `patterns`, `files`, `buf`, `cmd`, `stdout`, `stderr`, etc.) are function-local and never escape their defining scope. No package-level global state is modified.

**Concurrency mechanisms**: None detected across any module. No `sync.Mutex`, channels, `atomic` types, or other synchronization primitives. Shared mutable state does not exist to protect. The engine runner's returned error is then wrapped as `"documentation run failed: …"`.