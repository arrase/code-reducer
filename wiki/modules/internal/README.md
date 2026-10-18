# internal — Codebase Documentation Synthesis Engine

## Module Responsibility

The `internal` package implements an LLM-driven pipeline that automatically generates and maintains codebase documentation. It coordinates four subsystems:

- **config** — Configuration lifecycle: persistence, resolution, and default application.
- **engine** — Orchestration of the synthesis pipeline: cache management, change detection, hierarchical synthesis, and final persistence.
- **security** — Path traversal prevention and process-level locking.
- **tools** — Safe filesystem operations and repository verification utilities.

**Data flow:** `ResolveConfig` (config) → `Run` (engine) → `security.SafeResolve` / `security.AcquireLock` / `tools.*` for all filesystem access. The engine reads configuration, acquires a lock, parses `.gitignore`, discovers code files, computes SHA-256 hashes, builds a directory tree, and dispatches to `RunInit` or `RunUpdate`. The orchestrator coordinates cache loading, change detection, hierarchical synthesis, and final persistence.

---

## Configuration Subsystem (`internal/config`)

### Configuration Persistence (`io.go`)

**Public API**

| Function | Parameters | Return Types |
|---|---|---|
| `ConfigExists` | `cwd string` | `bool` |
| `LoadConfig` | `cwd string` | `*Config`, `error` |
| `SaveConfig` | `cwd string`, `cfg *Config` | `error` |

**Internal Functions**

| Function | Parameters | Return Types |
|---|---|---|
| `getConfigPath` | `cwd string` | `string` |
| `formatYAML` | `data []byte` | `string` |

**File System Interactions**

- **Disk reads:** `os.Stat` in `ConfigExists`; `os.ReadFile` in `LoadConfig`.
- **Disk writes:** `tools.WriteFileAtomic` in `SaveConfig`.
- No network, database, or API interactions.

**Error Propagation**

- `LoadConfig` returns `(*Config, error)`. If `os.ReadFile` fails, the raw error is returned. If YAML parsing fails, the error is wrapped with `fmt.Errorf("failed to parse yaml config: %w", err)`.
- `SaveConfig` returns `error`. If `yaml.Marshal` fails, the error is wrapped with `fmt.Errorf("failed to marshal yaml: %w", err)` before being passed to `tools.WriteFileAtomic`.
- `ConfigExists` derives a boolean from `os.Stat` error check (`err == nil`).
- `getConfigPath` and `formatYAML` have no error handling; failures result in panics.

### Configuration Resolution (`resolve.go`)

**Public API**

| Function | Parameters | Return Type |
|---|---|---|
| `ApplyDefaults` | `cfg *Config` | (none) |
| `ResolveConfig` | `repoRoot string`, `modelIDFlag string`, `numCtxFlag string` | `(*Config, error)` |

**Internal Functions**

| Function | Parameters | Return Type |
|---|---|---|
| `deduplicate` | `a []string` | `[]string` |
| `loadBaseConfig` | `repoRoot string` | `(*Config, error)` |
| `resolveModelID` | `cfgVal string`, `flagVal string` | `string` |
| `resolveBaseURL` | `cfgVal string` | `string` |
| `parsePositiveInt` | `val string`, `errMsg string` | `(int, error)` |
| `resolveNumCtx` | `cfgVal int`, `flagVal string` | `(int, error)` |

**External I/O Interactions**

- **Disk I/O:** `loadBaseConfig` calls `LoadConfig(repoRoot)` from `io.go`. If the file does not exist (`os.ErrNotExist`), an empty `Config{}` is returned instead of an error. Other read errors are wrapped and returned.
- **Environment Variables:** Three environment variables are read via `os.Getenv`:
  - `CodeReducerModelIDEnvKey` in `resolveModelID`
  - `OllamaBaseURLEnvKey` in `resolveBaseURL`
  - `OllamaNumCtxEnvKey` in `resolveNumCtx`
- No network, database, or API calls.

**Error Propagation**

- `ResolveConfig` returns errors from `loadBaseConfig` (which wraps non-`os.ErrNotExist` errors from `LoadConfig`) and from `resolveNumCtx` (which wraps errors from `parsePositiveInt`).
- `ApplyDefaults` and `deduplicate` swallow all errors silently.
- `resolveModelID` and `resolveBaseURL` return values without errors; callers handle the empty-string case implicitly.
- `parsePositiveInt` returns a formatted error string via `fmt.Errorf`.
- Errors are propagated via Go's `error` return convention; no panics or exceptions are used.

### Configuration Schema (`config.go`)

**Constants**

| Constant | Value |
|---|---|
| `ConfigFileName` | `.code-reducer.yaml` |
| `configFilePerm` | `0600` |
| `OllamaDefaultBaseURL` | `http://localhost:11434` |
| `OllamaDefaultModelID` | `ornith:9b` |
| `OllamaDefaultNumCtx` | `8192` |
| `DefaultDocsDir` | `wiki` |
| `CodeReducerModelIDEnvKey` | `CODE_REDUCER_MODEL_ID` |
| `OllamaBaseURLEnvKey` | `OLLAMA_BASE_URL` |
| `OllamaNumCtxEnvKey` | `OLLAMA_NUM_CTX` |

**Default Prompts**

The following multi-line string constants define system prompts for downstream synthesis steps:

- `DefaultSystemPrompt` — Code-Reducer role and rules (extract facts, no speculation, no code blocks)
- `DefaultModuleSynthesisPrompt` — Folder summary generation
- `DefaultArchitecturePrompt` — Global architecture document generation
- `DefaultQuickstartPrompt` — Developer quickstart guide generation
- `DefaultFileFactConsolidationPrompt` — Fact consolidation and deduplication

**Structs**

| Type | Fields |
|---|---|
| `ExtractionStep` | `Name` string, `Prompt` string |
| `Config` | `ModelID` string, `OllamaBaseURL` string, `OllamaNumCtx` int, `DocsDir` string, `SystemPrompt` string, `ModuleSynthesisPrompt` string, `ArchitecturePrompt` string, `QuickstartPrompt` string, `FileFactConsolidationPrompt` string, `ExtractionSteps` []ExtractionStep, `Ignore` []string |

**Default Extraction Steps**

`DefaultExtractionSteps` is a `[]ExtractionStep` containing: `API_SIGNATURES`, `BUSINESS_LOGIC`, `STATE_AND_CONCURRENCY`, `ERRORS_AND_SIDE_EFFECTS`.

### Resolution Priority Ordering

The following priority ordering is enforced by `ResolveConfig` and its helpers:

| Field | Priority Order |
|---|---|
| `ModelID` | CLI flag → `CODE_REDUCER_MODEL_ID` env var → config file value |
| `OllamaBaseURL` | `OLLAMA_BASE_URL` env var → config file value |
| `OllamaNumCtx` | CLI flag → `OLLAMA_NUM_CTX` env var → config file value |
| `DocsDir` | System default (`wiki`) when empty/zero |
| `ExtractionSteps` | System default when empty |
| All prompt fields | System default when empty |

`DocsDir` is additionally trimmed and path-cleaned during default application.

---

## Engine Subsystem (`internal/engine`)

### Core Infrastructure

#### LLM Client (`client.go`)

The `llmCaller` interface abstracts LLM invocation. The unexported `llmClient` type implements it by serializing requests to the Ollama `/api/chat` endpoint and returning text responses.

```go
type llmCaller interface {
    CallLLM(ctx context.Context, systemPrompt, userPrompt string) (string, error)
    NumCtx() int
}
```

`CallLLM` sends an HTTP POST with a 10-minute timeout. On a 200 OK, the full response body is read; on non-200, up to 1024 bytes of the error body are captured. Marshaling, request creation, and response parsing errors are wrapped with `fmt.Errorf`. The `onStatus` callback fires when `done_reason == "length"`.

#### Metadata Cache (`cache.go`)

`MetadataCache` persists file state and configuration across engine runs. It stores a version, a `stepsHash` derived from model ID and all prompt templates, per-file SHA-256 hashes with associated facts, and module-to-path mappings.

```go
type FileCacheEntry struct {
    SHA256 string
    Facts  string
}

type MetadataCache struct {
    Version   int
    StepsHash string
    Files     map[string]FileCacheEntry
    Modules   map[string]string
}
```

`loadMetadataCache` reads `.metadata.json` from `docsDir`. If absent, it returns an empty cache; if the version is incompatible, it resets. `saveMetadataCache` writes indented JSON. `IsInitialized` checks file existence. `computeSHA256` hashes arbitrary file paths.

### Tree Construction & Change Detection

#### Directory Tree (`tree.go`)

`DirNode` represents a directory containing files and child directories. `buildTree` parses a file slice into a hierarchical `*DirNode` structure.

```go
type DirNode struct {
    Path    string
    Files   []string
    Children map[string]*DirNode
}
```

`FileChange` tracks a file's status (`StatusAdded`, `StatusModified`, `StatusDeleted`) and path. `propagateAffected` walks the tree upward, marking ancestors affected when a descendant is affected. `isNodeAffected` checks whether a node's files appear in the changed set or its module is missing from cache. `determineAffected` combines changed files with cache state to produce a final affected-dirs map.

#### Orchestrator (`orchestrator.go`)

The `orchestrator` struct holds the `llmCaller` and coordinates the full pipeline. `pipelineState` is created fresh per invocation and contains `repoRoot`, `cfg`, `cache`, `affectedDirs`, `precalculatedHashes`, and `onStatus`.

```go
type orchestrator struct {
    client llmCaller
}

type pipelineState struct {
    client              llmCaller
    repoRoot            string
    cfg                 *config.Config
    cache               *MetadataCache
    affectedDirs        map[string]bool
    precalculatedHashes map[string]string
    onStatus            StatusFunc
}
```

`Run` is the entry point. It acquires a lock via `security.AcquireLock`, loads `.gitignore`, loads the metadata cache, and dispatches to `RunInit` or `RunUpdate`. `RunInit` builds the tree, hashes all code files, synthesizes the root summary, generates `architecture.md` and `quickstart.md`, updates `AGENTS.md`, and persists the cache. `RunUpdate` detects file changes, prunes stale cache entries, and regenerates only affected modules.

`setupPipeline` initializes the cache and affected-dirs map. `teardownPipeline` cleans up stale module files and empty directories. `hashCodeFiles` computes SHA-256 hashes for a file list. `collectTreeDirs` returns the set of directories present in the tree. `updateAgentGuidelines` appends documentation references to `AGENTS.md`.

### Hierarchical Synthesis

#### Synthesis Pipeline (`synthesize.go`)

`extractFileFacts` invokes the LLM for each extraction step per chunk of a file. It strips outer markdown fences, filters "none" facts, and caches results keyed by file SHA-256. Read failures are swallowed via `onStatus`; LLM and chunking failures propagate.

`synthesizeChildren` processes child directories recursively, synthesizing subsystem summaries before individual files. `collectComponents` combines subsystem summaries and file facts into a unified component list. `synthesizeNode` reduces all components for a directory into a final summary, writes it to `modules/<path>.md`, and persists the cache. If no components are produced, the cached summary is deleted and the markdown file is removed.

```go
func extractFileFacts(ctx context.Context, p *pipelineState, f string, siblingFiles []string) (string, error)
func synthesizeChildren(ctx context.Context, p *pipelineState, node *DirNode) ([]string, error)
func collectComponents(ctx context.Context, p *pipelineState, node *DirNode, subsystems []string) ([]string, error)
func synthesizeNode(ctx context.Context, p *pipelineState, node *DirNode) (string, error)
```

`buildExtractionPrompt` constructs the user prompt for a single extraction step. `maxExtractionOverhead` computes the maximum characters available for extraction given system prompt and file context. `toSafeMarkdownFilename` sanitizes a module path for use as a filename.

#### Text Reduction & Chunking (`chunking.go`)

`reduceWithLLM` calls the LLM to reduce a batch of items. `reduceInChunks` splits oversized items with an 800-character overlap, batches remaining items, and reduces each batch. If total output reaches 95% of input, reduction stops and results are concatenated. Otherwise, the intermediate results are recursively reduced.

`chunkTextWithOverlap` splits text with overlap validation. `stripOuterMarkdownFence` removes surrounding code fences. `calculateFileLimit` computes the maximum processing characters from context window minus system prompt overhead. `isNoneFact` filters empty, "none", and "n/a" facts. `expandOversizedItems` splits items exceeding the limit. `batchItems` groups remaining items. `countRunes` counts Unicode code points. `reduceItems` orchestrates the reduction with a custom function.

### Error Handling Summary

| Operation | Failure Mode | Handling |
|-----------|-------------|----------|
| Lock acquisition | `security.AcquireLock` fails | Fatal error returned |
| Gitignore load | `tools.LoadGitignore` fails | Fatal error returned |
| Code discovery | `tools.DiscoverCodeFiles` fails | Fatal error returned |
| File read (extraction) | `tools.ReadFileSafely` fails | Swallowed, logged via `onStatus` |
| LLM call | `CallLLM` fails | Propagated as wrapped error |
| Chunking | Invalid `maxRunes` or `overlapRunes` | Returned as error |
| Disk write | `tools.WriteFileSafely` fails | Fatal error returned |
| Stale cleanup | `os.Remove` fails | Silently ignored |
| Cache save | Marshaling or write fails | Fatal error returned |
| HTTP non-200 | Response body read | Wrapped with status code |
| Context cancellation | Any operation | Propagated as error |

---

## Security Subsystem (`security.go`)

### Module Responsibility

This module provides two non-negotiable security primitives for a repository-level tool:

1. **Path traversal prevention** — `SafeResolve` ensures any input path resolves strictly inside the repository root, even when symlinks are involved.
2. **Process-level locking** — `AcquireLock` / `SimpleLock` / `Unlock` prevents concurrent execution by multiple processes of the same tool.

**Data flow:** `SafeResolve(repoRoot, inputPath)` → `(string, error)`. `AcquireLock(repoRoot)` → `(*SimpleLock, error)`. `(*SimpleLock).Unlock()` → `error`. No global mutable state exists; all constants and sentinel errors are immutable.

### Path Traversal Prevention

#### `SafeResolve(repoRoot, inputPath string) (string, error)`

Cleans `inputPath` and verifies the resolved result lies strictly inside the repository root.

**Algorithm:**

1. `filepath.Abs(repoRoot)` — canonicalize the repository root to an absolute path.
2. `filepath.EvalSymlinks(repoRoot)` — resolve symlinks on the repository root itself.
3. Walk up the input path from the root, stopping at the first physically existing ancestor (checked via `os.Lstat`).
4. `filepath.EvalSymlinks` on that existing ancestor.
5. Rebuild the full path by appending the non-existent suffix components.
6. `filepath.Rel` computes the relative path from the resolved root to the rebuilt path. If the result is not `""`, return `ErrPathTraversal`.

**Error propagation:** Returns `(string, error)`. Errors are wrapped with `fmt.Errorf` using `%w` for chainability. `ErrPathTraversal` is returned directly as a sentinel.

### Process-Level Locking

#### `AcquireLock(repoRoot string) (*SimpleLock, error)`

Acquires a simple file lock in `repoRoot` using `O_EXCL` for atomicity.

**Algorithm:**

1. Resolve the lock file path using `SafeResolve`.
2. `os.OpenFile` with `O_WRONLY|O_CREATE|O_EXCL`. If the file already exists, return `ErrLockHeld`.
3. Write the current process PID (`os.Getpid`) to the file.
4. Return a `*SimpleLock` handle.

**Error propagation:** If `OpenFile` fails with `os.IsExist`, wraps `ErrLockHeld`. If `Write` fails, the file is closed and removed before returning a wrapped error.

#### `(*SimpleLock) Unlock() error`

Releases the lock by closing the file and removing it. Idempotent and thread-safe.

**Algorithm:**

1. Acquire the internal `sync.Mutex` (`l.mu`).
2. If `l.closed` is true, return `nil`.
3. Close the file descriptor.
4. Remove the lock file. If both close and remove fail, return the error from the non-nil one.

**Error propagation:** If close succeeds but remove fails (and is not `os.IsNotExist`), the remove error is assigned to `err`. If close fails, the remove error is ignored.

### Types

#### `SimpleLock`

```go
type SimpleLock struct {
    lockPath string
    file     *os.File
    mu       sync.Mutex
    closed   bool
}
```

- `lockPath` — absolute path to the lock file.
- `file` — open file descriptor for the lock.
- `mu` — protects `closed` field.
- `closed` — set to `true` in `Unlock()`; guards idempotency.

### Constants and Errors

**Constants**

- `LockFileName` = `".code-reducer.lock"`
- `defaultFilePerm` = `0644`

**Error Variables**

- `ErrPathTraversal` = `errors.New("security violation: path traversal detected")`
- `ErrLockHeld` = `errors.New("lock is already held by another process")`

### External I/O Interactions

All operations target the local filesystem. No network, database, or API interactions.

| Operation | Purpose |
|-----------|---------|
| `filepath.Abs` | Resolves absolute path for `repoRoot` |
| `filepath.EvalSymlinks` | Resolves symlinks on `repoRoot` and intermediate ancestors |
| `os.Lstat` | Checks existence of path components during traversal resolution |
| `os.OpenFile` | Opens the lock file with `O_WRONLY|O_CREATE|O_EXCL` |
| `os.Remove` | Removes the lock file during `Unlock()` and on write failure in `AcquireLock()` |
| `os.Getpid` | Reads process ID (no external I/O) |
| `filepath.Rel` | Computes relative path to verify containment |

### Synchronization

- `SimpleLock.Unlock()` uses `sync.Mutex` (`l.mu`) to protect the `closed` field.
- `AcquireLock()` is a package-level function that creates a new `SimpleLock` instance; it does not use any synchronization mechanism.

### Error Handling Summary

- Errors are not logged or printed; they propagate to the caller.
- No `panic`/`recover` is used.
- Sentinel errors (`ErrPathTraversal`, `ErrLockHeld`) are returned directly.
- All other errors are wrapped with `fmt.Errorf` using `%w` for chainability.

---

## Tools Subsystem (`internal/tools`)

### Module Responsibility

This module provides safe filesystem operations and repository verification utilities for source code management workflows. It operates on a repository root (`repoRoot`) and resolves virtual paths through a security layer to prevent path traversal attacks. All operations are disk I/O only—no network, database, or external API calls.

**Data Flow:**

```
caller → virtualPath → security.SafeResolve → filesystem path → os.* operations
```

All public functions accept a `repoRoot` parameter and resolve virtual paths before accessing the filesystem. Errors are propagated as return values; no panics occur.

### File Tools (`file_tools.go`)

#### Safe File Access

**`FileExistsSafely(repoRoot string, virtualPath string) bool`**

Checks whether a file exists at the resolved filesystem path. Swallows `os.Stat` errors and returns `false` on any failure.

**`ReadFileSafely(repoRoot string, virtualPath string) ([]byte, error)`**

Reads file contents from the resolved filesystem path. Returns the file data and an error. Swallows `os.Open` errors.

**`RemoveFileSafely(repoRoot string, virtualPath string) error`**

Removes a file at the resolved filesystem path. Ignores `os.IsNotExist` errors (file may already be absent). Propagates all other errors.

#### Atomic File Writes

**`WriteFileAtomic(targetPath string, data []byte, perm os.FileMode) error`**

Writes data to `targetPath` atomically: writes to a temporary file first, then renames to the target path. Wraps all sub-errors with `fmt.Errorf("failed to %s: %w", ...)`.

**`WriteFileSafely(repoRoot string, virtualPath string, content []byte) error`**

Writes content to the resolved filesystem path. Uses atomic write semantics.

#### Gitignore Integration

**`EnsureGitignoreHasLockfile(repoRoot string) error`**

Ensures `.code-reducer.lock` is included in `.gitignore` to prevent it from being committed.

**`LoadGitignore(repoRoot string) ([]string, error)`**

Loads `.gitignore` patterns from the repository root. Returns the parsed patterns and an error.

**`ShouldIgnoreFile(relPath string, gitIgnore *ignore.GitIgnore) bool`**

Determines whether a relative path matches any loaded gitignore patterns. Uses `ignore.CompileIgnoreLines` for pattern parsing (in-memory, no external I/O).

### Global State

| Variable | Type | Mutability |
|---|---|---|
| `skippedFileNames` | `map[string]bool` | Immutable after init |
| `skippedExtensions` | `map[string]bool` | Immutable after init |
| `skippedTestSuffixes` | `[]string` | Immutable after init |

These maps are initialized once and never modified. They define the multi-layer file filtering rules applied during file discovery.

### Git Tools (`git_tools.go`)

#### Repository Verification

**`VerifyGitRepo(repoRoot string) error`**

Verifies whether `repoRoot` resides inside a git repository by executing `git rev-parse --is-inside-work-tree` in the specified directory.

**Process Execution Details:**

- Spawns `git` as a child process via `exec.Command("git", "--no-pager", "rev-parse", "--is-inside-work-tree")`
- Redirects child process stderr to a `bytes.Buffer` for capture
- Sets child process working directory to `repoRoot`

**Error Handling:**

- If `cmd.Run()` returns an error, the function wraps it with `fmt.Errorf` and returns the wrapped error (including captured stderr content)
- If `cmd.Run()` returns nil, returns nil (success)
- No panics or swallowed errors