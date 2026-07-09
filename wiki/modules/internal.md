# internal — Code Reducer Internal Packages

## Module Responsibility & System Overview

The `internal` directory contains four packages that implement the core operational pipeline for Code Reducer: configuration resolution (`config`), filesystem operations and git utilities (`tools`), repository-level path sandboxing and mutual exclusion (`security`), and LLM-driven documentation generation (`engine`). The system operates as a synchronous, file-system-bound process with no shared mutable state across goroutines. No network I/O originates from any internal package; all external communication flows through the Ollama-compatible client constructed within `engine`.

Data flow enters at `internal/engine` where `Runner.Run` initiates pipeline execution, delegates code discovery and synthesis to `config`, validates paths via `security`, performs filesystem operations through `tools`, and persists state to disk. Configuration is resolved in advance by a separate resolution phase before the engine runs, with settings loaded from CLI flags → environment variables → YAML config → system defaults.

---

## internal/config — Configuration Resolution Pipeline

**Responsibility:** Implements multi-source configuration resolution for Code Reducer through a fixed precedence chain: CLI flags override environment variables, which override YAML config, which override system defaults. All operational parameters are resolved into a single `*Config` value consumed by downstream packages. Filesystem writes enforce mode `0600`; no network I/O occurs in this package.

**Data Flow:** `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string)` orchestrates the full resolution sequence: loads `.code-reducer.yaml` from `repoRoot` via `LoadConfig`, merges ignore paths and extensions with defaults + user values (defaults-first deduplication), selects extraction steps from user config or falls back to `DefaultExtractionSteps`, resolves Ollama parameters through priority chain (default → YAML → env var → flag) with type-safe numeric parsing, sets docs directory from config or default `"wiki"`. Local state is created fresh per invocation; no shared mutable state.

### Struct Definitions

**`Config`** — Root configuration struct representing the fully resolved state:

| Field | Type | Description |
|---|---|---|
| `ModelID` | `string` | Ollama model identifier (highest priority) |
| `OllamaBaseURL` | `string` | Base URL for Ollama inference backend |
| `OllamaNumCtx` | `int` | Context window size; supports integer-from-env parsing and flag-based overrides with bounds checking (`> 0`) |
| `DocsDir` | `string` | Path to documentation directory; defaults to `"wiki"` if unset |
| `ExtractionSteps` | `[]ExtractionStep` | Ordered analysis pipeline phases |
| `Ignore` | `[]string` | Directory paths excluded from analysis |
| `IgnoreExtensions` | `[]string` | File extensions excluded from analysis |

**`ExtractionStep`** — Encodes a single LLM extraction phase: contains only `Name string` and `Prompt string`; used exclusively within the `ExtractionSteps` slice in `Config`.

### Public Functions

**`ConfigExists(cwd string) bool`** — Probes whether the configuration file exists at `cwd` using `os.Stat`. Non-nil errors are treated as "file does not exist" and return `false`; errors are swallowed.

**`LoadConfig(cwd string) (*Config, error)`** — Reads `.code-reducer.yaml` from `cwd` via `os.ReadFile`. Returns parsed configuration on success; if file is absent or malformed, returns an unwrapped error directly. Empty YAML files return an empty struct with nil error. File read errors are passed through unchanged.

**`SaveConfig(cwd string, cfg *Config) error`** — Serializes a `*Config` to disk as YAML via `os.WriteFile` with mode `0600`. Marshaling and writing each wrap their respective errors with descriptive prefixes (`"failed to marshal yaml"`, `"failed to write config file"`). Returns nil on success.

**`MergeAndDeduplicate[T comparable](a []T, b []T) []T`** — Concatenates two slices of the same type, removes duplicates while preserving merge order. Used for merging ignore paths/extension lists where defaults are merged before user values to prevent stale default entries from being silently dropped.

### Concurrency & Error Handling

No mutexes, channels, or synchronization primitives exist in this file. In `ResolveConfig`, local variables `cfg` and `resolved` are created fresh per call and not shared across goroutines. Error patterns: direct pass-through for `LoadConfig` (file read), wrapped with prefix for `SaveConfig` (marshal + write), swallowed/silent fallback for `ResolveConfig` (`LoadConfig` error discarded; empty `*Config{}` substituted). No panics anywhere.

---

## internal/tools — File System Operations & Git Subprocess Utilities

**Responsibility:** Provides two complementary subsystems: filesystem operations with path-safe read/write primitives and git subprocess execution utilities. All functions operate synchronously with no shared mutable state, making the module safe for concurrent use across goroutines without external synchronization. No database or network communication; all I/O is local filesystem only.

### File System Operations (`file_tools.go`)

**`ReadFileSafely(repoRoot string, virtualPath string) ([]byte, error)`** — Resolves `virtualPath` relative to `repoRoot`, delegates to `security.SafeResolve`, then reads the resulting path via OS read primitive. Returns wrapped error if resolution or read fails. No panic on failure.

**`WriteFileSafely(repoRoot string, virtualPath string, content []byte) (error)`** — Two-stage verification prevents TOCTOU race conditions: resolves `virtualPath` through `security.SafeResolve`, creates parent directories with mode 0755, opens file with `O_WRONLY|O_CREATE` flags and mode 0644, re-stat's both the resolved path and opened descriptor to confirm same inode (different inodes return "symlink race detected"), truncates to zero bytes, writes content. After truncation checks `os.ModeSymlink` via Lstat; if symlink flag is set, returns a security violation instead of overwriting. No panic on failure; errors wrapped across multi-step operations.

**`LoadGitignore(repoRoot string) ([]string, error)`** — Opens `.gitignore` from disk at `repoRoot`. Parses line-by-line: skips empty lines and comments (`#`). Returns nil when absent (not an error). All other errors returned bare without wrapping.

**`ShouldIgnoreFile(repoRoot string, relPath string, gitIgnore *ignore.GitIgnore, ignoredExtensions []string) (bool)`** — Layered file-ignoring logic: compiled patterns from user-provided ignore list, dot-prefixed directories or `.egg-info` suffixes, extension matching normalized to lowercase with leading-dot handling, short-circuit skip for known text file extensions to avoid binary scan, binary classification via `IsBinaryFile` only if neither above matched. Returns `true` on any internal failure (swallows errors from `security.SafeResolve` and `IsBinaryFile`). No error return at all.

**`DiscoverCodeFiles(repoRoot string, ignores []string, ignoredExtensions []string) ([]string, error)`** — Full directory walk: skips repo root itself, prunes entire subtrees when directory names match dot-prefix, `.egg-info`, or gitignore patterns (via `filepath.SkipDir`), delegates individual file checks to `ShouldIgnoreFile`, collects results as forward-slash relative paths. Walk-dir callback returns `nil` for individual entry errors (skip-on-error). Final return wraps the walk error if present.

**`IsBinaryFile(path string) (bool)`** — Reads first 1024 bytes, returns `true` if any null byte found. Used only after all other ignore heuristics fail, minimizing invocation frequency. Returns `false` on any open/read failure; treats failures as "not binary". No error return.

### Git Subprocess Execution (`git_tools.go`)

**`RunGit(repoRoot string, args ...string) (string, error)`** — Executes a git command in given directory, captures stdout and stderr into separate buffers, returns cleaned output and an error if process exits non-zero. Stderr attached even when exit code is 0 so warnings are not silently lost. On failure: trims stderr content, wraps underlying error with new message combining original (`%v`) and trimmed stderr string. Returns `(string, wrappedError)`. No panic, no silent swallow.

**`VerifyGitRepo(repoRoot string) (error)`** — Two-step pre-flight check: confirms `git` executable exists on system path via `exec.LookPath("git")`; if fails returns sentinel-style fixed message "git executable not found in system path" without unwrapping. Runs `git rev-parse --is-inside-work-tree`; if fails returns sentinel-style fixed message "not a git repository (or any of the parent directories)". Does not propagate or wrap underlying error either way.

### Error Handling Summary

| Function | Failure Pattern | Notes |
|---|---|---|
| `ReadFileSafely` | Wrapped with context prefix | Caller sees wrapped read resolution failure |
| `WriteFileSafely` | Mix of wrapped and unwrapped | Multi-step write: each failure wrapped; final success returns nil |
| `LoadGitignore` | Unwrapped intentional absence | Only unwrapped path for non-not-exist errors in the file |
| `ShouldIgnoreFile` | Returns true on internal failure | Swallows all errors from dependencies |
| `DiscoverCodeFiles` | Swallowed walk-dir per-entry errors + wrapped final err | Walk callback returns nil for individual failures; final return wraps walk error if present |
| `IsBinaryFile` | Returns false on open/read failure | Treats all I/O failures as "not binary" |
| `RunGit` | Wrapped with combined message | Original error + trimmed stderr appended |
| `VerifyGitRepo` | Sentinel-style fixed messages | Replaces underlying error entirely, never wraps |

No panics. All errors returned to callers. Error wrapping used exclusively except for intentional unwrapped absence detection in `LoadGitignore`. No DB or network communication — all I/O is local filesystem only.

---

## internal/security — Path Sandboxing & Mutual Exclusion

**Responsibility:** Provides process-level mutual exclusion and path sandboxing for the code-reducer toolchain. Enforces that all file operations occur within a single repository root using an atomic lockfile, prevents directory-traversal attacks against user-supplied paths, ensures lock sentinel is excluded from version control tracking via `.gitignore` injection. All functions operate exclusively against local filesystem; no network I/O or database interactions exist in this package. Every operation returns errors to callers; no panics occur anywhere in the public surface area.

### Constants

**`LockFileName`** — Package-level constant `string` with value `.code-reducer.lock`. Immutable after compilation. Referenced directly by `AcquireLock()` and consumed by `EnsureGitignoreHasLockfile()` during housekeeping operations.

### Types

**`SimpleLock`** — Contains `lockPath string` (set once in `AcquireLock`; never reassigned) and `file *os.File` (OS handle opened atomically via O_EXCL). Invariants: `lockPath` is assigned during construction and remains unchanged for the instance lifetime. No concurrent mutation of this field exists. `file` holds an open file descriptor closed exclusively by `(l *SimpleLock).Unlock()`.

### Public API Surface

**`SafeResolve(repoRoot, inputPath string) (string, error)`** — Resolves arbitrary user-supplied path against repository root and rejects any result that escapes outside it: calls `filepath.Abs(repoRoot)` for absolute anchor, `filepath.Abs(inputPath)` for absolute candidate, computes relative position via `filepath.Rel`. If resulting string begins with `".."`, returns an error wrapping the original failure (concatenating descriptive prefix with `%w` applied to underlying `filepath.Rel` or `filepath.Abs` error). A path resolving to a sibling directory, parent directory, or any ancestor of `repoRoot` is rejected.

**`AcquireLock(repoRoot string) (*SimpleLock, error)`** — Atomically opens `.code-reducer.lock` in the repository root: calls `SafeResolve(repoRoot, LockFileName)`, opens file with flags `os.O_WRONLY | os.O_CREATE | os.O_EXCL` and mode 0644. The `O_EXCL` flag guarantees atomic mutual exclusion at kernel level; if a process holds an open writeable descriptor, this call fails immediately without creating or partially writing the file. If open fails, checks `os.IsExist(err)` on returned error object; when true emits specific message indicating stale lockfile is present; otherwise wraps original error with `%w`. Returns constructed `*SimpleLock` and any error encountered during construction or file opening.

**`(l *SimpleLock).Unlock() error`** — Closes and removes sentinel file, releasing the lock: calls `l.file.Close()` to release OS file descriptor, removes underlying path via `os.Remove`. If both operations fail returns non-nil error from `Close`; if only one fails returns that single error (close failure masks removal failure precedence). I/O errors propagated to caller; no panics.

**`EnsureGitignoreHasLockfile(repoRoot string) error`** — Ensures `.gitignore` contains an entry for the lockfile: derives path to `.gitignore` within `repoRoot`, if file does not exist on disk (`os.IsNotExist(err)` is true) treats this as success and skips read step entirely (no error returned for missing files, only genuine I/O failures), otherwise reads existing content via `os.ReadFile`, opens file with flags `os.O_APPEND | os.O_CREATE | os.O_WRONLY` and mode 0644, appends lockfile entry to file handle. Operation is idempotent on repeated calls — duplicate entries are not detected or cleaned up but do not cause functional issues downstream because `.gitignore` treats duplicates as no-ops.

### Concurrency Model

Package relies exclusively on OS-level atomicity. No in-process synchronization primitives (`sync.Mutex`, `sync.RWMutex`, channels) appear anywhere in this file. Mutual exclusion guarantee provided by kernel's handling of `O_EXCL` during `os.OpenFile`. If a process holds an open writeable descriptor to `.code-reducer.lock`, any other process attempting acquisition fails atomically, with no window for race conditions between check and act. No package-level global variables; mutable state contained within the `SimpleLock` struct fields: both `lockPath` and `file` are set once during construction and never modified after that point.

### Error Handling & Side Effects Summary

| Category | Result |
|----------|--------|
| Mutable state | None — fields write-once, no shared mutable state to protect |
| Concurrency protection | OS-level atomicity via `O_EXCL`; no shared mutable state exists in this package |
| Network I/O | None |
| Database operations | None |
| Panics / sentinels | None |

All functions return typed errors via their return values. Errors wrapped with descriptive messages using `%w` for wrapping original `filepath.Abs`, `filepath.Rel`, and OS-level errors. The `Unlock()` method propagates close or remove failures; when both fail, the close error is returned in precedence over removal.

---

## internal/engine — LLM-Driven Documentation Generation Pipeline

**Responsibility:** Implements an LLM-driven documentation generation pipeline for code repositories. Discovers source files under a repository root, computes integrity hashes, determines which modules are affected by changes, synthesizes summaries per module through recursive chunked reduction, persists results to disk. Two operational modes exist: **init** (full documentation regeneration) and **update** (incremental re-documentation of only changed modules). All external communication flows through a custom Ollama-compatible LLM client; all internal state is unprotected against concurrent access.

### Data Structures

**`FileChange`** (`tree.go`) — Contains `Path string` (file path relative to repo root) and `Status string` ("Added", "Modified", or "Deleted"). Represents a single detected change in the repository. Used as input to impact analysis and tree construction.

**`DirNode`** (`tree.go`) — Hierarchical representation of repository's directory layout: contains `Path string` (absolute directory path), `Files []string` (list of file paths within this directory), `Children map[string]*DirNode` (subdirectories keyed by name). Constructed from a flat slice of `FileChange` entries via `buildTree`. Leaf nodes store files; intermediate nodes created implicitly during traversal.

**`MetadataCache`** (`cache.go`) — Persistent state stored at `<docsDir>/.metadata.json`: contains `LastDocumentedCommit string` (commit SHA, or empty if no documentation has been performed), `Files map[string]FileCacheEntry` (file path → cached integrity entry), `Modules map[string]string` (module directory path → synthesized summary). The `Files` and `Modules` maps are re-initialized to empty maps if unmarshaled as `null`, preventing nil-map panics on subsequent operations.

**`FileCacheEntry`** (`cache.go`) — Keyed by full file path: contains `SHA256 string` (content hash for integrity verification) and `Facts string` (concatenated LLM-extracted metadata). Stores result of reading a file's bytes and hashing them, paired with whatever facts were extracted via the multi-step extraction pipeline.

**`Event`** (`orchestrator.go`) — Contains `Type string` (event type identifier) and `Message string` (associated payload). Observer-passed callback value used by `Runner.Run` and related functions to surface status updates without blocking return paths.

### External Communication Matrix

| Function | Direction | Mechanism | Notes |
|---|---|---|---|
| `client.CallLLM` | Outbound network | HTTP POST to `<baseURL>/api/chat`, `stream: false` | Returns single response; non-200 status returns formatted error with truncated body (1KB limit) |
| `client.StreamLLM` | Outbound network | HTTP POST, `stream: true` | Reads line-by-line via `bufio.NewReader`; invalid JSON lines silently skipped during parsing |
| `json_parser.StripOuterMarkdownFence` | In-memory regex | No I/O | Strips triple-backtick fences with optional language specifier; returns original input if no match |
| `json_parser.CleanJSONResponse` | In-memory character scan | No I/O | Locates `{`/`[` within cleaned text, tracks nesting respecting string literals and escapes |
| `json_parser.UnmarshalJSONResponse` | In-memory unmarshal | No I/O | Wraps `json.Unmarshal` error via `%w`; includes cleaned JSON snippet in message |
| File reads (orchestrator/synthesize) | Disk read | `tools.ReadFileSafely(repoRoot, path)` | Relative to repo root; errors swallowed per-file during synthesis loop |
| File writes (orchestrator/synthesize) | Disk write | `tools.WriteFileSafely(repoRoot, path, data)` | Used for architecture.md, quickstart.md, module docs, AGENTS.md. Errors wrapped or silently discarded depending on code path |

### Data Flow: Init Mode

1. **Lock acquisition** — `runner.go` calls `security.AcquireLock(repoRoot)`; deferred unlock on exit. Failure returns wrapped error immediately.
2. **Soft setup check** — `.gitignore` update for lockfile path attempted via `security.EnsureGitignoreHasLockfile`; failure logged as warning and execution continues.
3. **Pipeline setup** (`orchestrator.go`, internal `setupPipeline`) — loads `<docsDir>/.metadata.json` via `loadMetadataCache`; if absent, returns freshly initialized empty cache. Loads `.gitignore` patterns for code-discovery filtering; errors swallowed with log warning.
4. **Code discovery and tree construction** (`orchestrator.go`, internal `discoverCodeFiles` + `buildTree`) — scans `<repoRoot>` excluding ignored paths and git directories, groups results into a `DirNode` tree. Errors from discovery propagate as-is to the caller.
5. **Affected determination** (`tree.go`, `determineAffected` / `propagateAffected`) — for each node in the tree: marks affected if any file appears in `filteredChanges`, if no markdown module exists at `<docsDir>/modules/<safe-filename>.md`, or if path is absent from `cache.Modules`. Then propagates upward so any parent of an affected child becomes affected.
6. **LLM client construction** (`runner.go`) — instantiates `*LLMClient` with configured model ID and base URL, 10-minute HTTP timeout.
7. **Root summary synthesis** — feeds all changed files into `reduceInChunks` (internal) to produce consolidated root summary, then generates `architecture.md` and `quickstart.md` via two successive LLM calls keyed by `"architecture"`. Each call wrapped with system persona prompt injected at construction time.
8. **Module synthesis** (`synthesize.go`, internal `synthesizeNode`) — for each affected directory: reads file contents, computes SHA256, checks cache hit; if miss, truncates content to ~75% of context window and runs extraction steps from `cfg.ExtractionSteps`; accumulates results into facts; stores in both `cache.Files` (by path) and `cache.Modules` (by directory).
9. **AGENTS.md maintenance** — checks for existing file; if absent writes fresh content with marker `"AI Agent Guidelines"`; otherwise appends conditionally. Write errors silently discarded (`_ = ...`).
10. **Cache persistence** (`orchestrator.go`, internal `teardownPipeline`) — saves updated `MetadataCache` back to `<docsDir>/.metadata.json`. Errors logged as warning and swallowed.

### Data Flow: Update Mode

Same setup phase through step 5 (affected determination). Key divergence begins at step 6: if no affected directories exist, pipeline short-circuits with success message; no docs regenerated. If `affectedDirs["."]` is false and both architecture/quickstart files exist on disk, global documents are skipped entirely (`RunUpdate` returns early). Otherwise invokes `GenerateStandardDocs` to regenerate global docs (same LLM path as init), then proceeds with module synthesis for affected directories only.

### Concurrency Characteristics

No synchronization primitives present anywhere in the package. All mutable state (`MetadataCache.Files`, `MetadataCache.Modules`, `DirNode.Children`, `affectedDirs` map) is unprotected against concurrent access. The `Runner.Run` function acquires a repository-level lock via `security.AcquireLock` before executing pipeline logic, but this lock is external to the engine package and its internal behavior is not observable here.

### Error Handling Summary

| Pattern | Where | Behavior |
|---|---|---|
| Wrapped + returned | LLM calls in standard doc generation; file writes for architecture/quickstart/module docs; directory creation | `fmt.Errorf("...: %w", err)` propagated up call chain |
| Returned raw | Recursive `reduceInChunks` results; pipeline execution via LLM client | Direct pass-through, no wrapping |
| Swallowed + logged warning | `.gitignore` load in setup; metadata cache load/save in teardown and update | Logged through event callback; execution continues |
| Silently discarded (`_ = err`) | AGENTS.md write path (both create and append branches) | Pipeline reports success regardless of write outcome |
| Swallowed, no signal | `GetLastDocumentedCommit` on any failure from cache load | Returns `"", false`; caller cannot distinguish between "not yet documented" and "load failed" |

No panics raised anywhere in the visible code. Where errors could theoretically cause downstream issues (e.g., `chunkRes` used without nil-check after recursive call), no protection exists within this file's scope.