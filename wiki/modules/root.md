# Code-Reducer Engine Architecture

## Module Responsibility

Code-Reducer is an automatic software-documentation generation system driven by Ollama-compatible large language models (LLM). It operates on a repository's source code tree to produce markdown artifacts describing the project's architecture, module boundaries, business logic, state management, error handling patterns, and agent guidelines. The system runs in two modes:

- **`init`** — generates documentation from scratch against the full discovered codebase
- **`update`** — performs incremental regeneration based on detected file changes

Execution is initiated through `internal/engine/runner.go` (`Runner.Run`). All disk I/O flows through `internal/tools`; all network calls go through the LLM client in `internal/engine/client.go`; concurrency protection is provided by OS-level file locking managed by `security.AcquireLock`.

---

## Subsystem: config — Multi-Source Configuration Resolution

### Responsibility

Centralizes all configuration state for the engine: loading, persisting, resolving, and validating a `config.Config` struct from CLI flags, environment variables, YAML files, or hardcoded defaults in a strict four-tier priority chain. Persisted state lives at `.code-reducer.yaml` in the current working directory with restricted owner-only permissions (`0600`).

### Data Structures

```go
type Config struct {
    ModelID            string   // Ollama model identifier (falls back to gemma4:26b-a4b-it-qat)
    OllamaBaseURL      string   // Base URL for the Ollama API endpoint
    OllamaNumCtx       int      // Context window size
    DocsDir            string   // Path to source documentation directory
    ExtractionSteps    []ExtractionStep  // Ordered pipeline of analysis tasks
    Ignore             []string            // Directories to exclude from scanning
    IgnoreExtensions   []string            // File extensions to skip
}

type ExtractionStep struct {
    Name   string `yaml:"name"`
    Prompt string `yaml:"prompt"`
}
```

Four extraction steps are defined by default:

| Priority | Step | Purpose |
|---|---|---|
| 1 | API_SIGNATURES | Extract exported structs, interfaces, methods and their types |
| 2 | BUSINESS_LOGIC | Analyze domain concepts and high-level algorithmic flow |
| 3 | STATE_AND_CONCURRENCY | Identify mutable state and concurrency protections (mutexes, channels) |
| 4 | ERRORS_AND_SIDE_EFFECTS | Document external communication (I/O) and error handling patterns |

The step list is overridable in the YAML file; if empty at resolve time it falls back to defaults.

### Module Constants & Defaults

Package-level read-only constants consumed exclusively by `ResolveConfig`:

| Variable | Purpose | Reassignable? |
|---|---|---|
| `DefaultIgnores` | Hardcoded directories to skip (`.git`, `node_modules`, `venv`, etc.) | No — read-only after init |
| `DefaultIgnoredExtensions` | Hardcoded file extensions to skip (`.pyc`, `.class`, etc.) | No — read-only after init |
| `DefaultExtractionSteps` | Ordered list of default analysis tasks | No — read-only after init |

### Functions

**`ConfigExists(cwd string) bool`** — Returns `true` when `.code-reducer.yaml` exists under `cwd`. Treats any `os.Stat` error as "file does not exist" — permission denial and I/O failures are silently swallowed.

**`LoadConfig(cwd string) (*Config, error)`** — Reads the persisted config from `<cwd>/.code-reducer.yaml`. Returns a nil pointer with an unwrapped `os.ReadFile` error if the file cannot be read at all. If YAML parsing fails, returns a wrapped error (`failed to parse yaml config`). On success, returns the raw parsed struct without applying overrides — callers must chain this into `ResolveConfig` for final resolution.

**`SaveConfig(cwd string, cfg *Config) error`** — Marshals the provided `cfg` pointer and writes it to `<cwd>/.code-reducer.yaml` with `0600` permissions via `os.WriteFile`. Marshaling errors and write errors are both wrapped with `%w`, preserving the chain for unwrapping. A nil config is not explicitly guarded; marshaling a nil will result in an empty YAML document.

**`MergeAndDeduplicate(a []T, b []T) []T`** (where T is comparable) — Merges two slices into one deduplicated slice using a map-based seen-set pattern. System defaults are merged first, then user-provided config additions follow — this ordering ensures user values take precedence when duplicates exist across the merge boundary. No error return; any panic from non-comparable types would be a caller-side bug.

**`ResolveConfig(repoRoot, modelIdFlag string, numCtxFlag string) *Config`** — Orchestrates the full resolution pipeline: (1) calls `LoadConfig` to read persisted state (errors swallowed — falls back to empty struct); (2) applies CLI flag overrides in order: `modelIdFlag` → `OllamaBaseURL`, then `numCtxFlag` → `OllamaNumCtx`; (3) merges default ignore lists (`DefaultIgnores`, `DefaultIgnoredExtensions`) with user-provided values via `MergeAndDeduplicate`; (4) falls back to hardcoded defaults for extraction steps if the slice is empty; (5) returns a pointer to the final resolved config; no error return — any failure path results in an empty `&Config{}`.

---

## Subsystem: security — Path-Boundary Enforcement and Mutual Exclusion

### Responsibility

Implements three infrastructure contracts for the Code-Reducer agent: path-boundary enforcement against directory-traversal attacks, process mutual exclusion via file locking, and lockfile version-control exclusion through `.gitignore` management. No synchronization primitives (`sync.Mutex`, channels, atomics) are used; concurrency safety is delegated to the OS-level `O_EXCL` semantics on lock acquisition.

### Domain Concepts and Business Rules

1. **Path-boundary enforcement** — `SafeResolve` requires any user-supplied path to resolve strictly inside the repository root. If a resolved absolute path escapes the root (detected via the `..` prefix check), the operation fails with an explicit violation error. This is a defense-in-depth rule against directory-traversal attacks.

2. **Process mutual exclusion** — The lock mechanism ensures only one Code-Reducer instance operates on a given repository at a time. Acquisition requires creating the lockfile atomically (via `O_EXCL`); if it already exists, acquisition fails with a "lock held" error. Release closes the file and removes the lockfile.

3. **Lockfile version-control exclusion** — The rule that `.code-reducer.lock` must appear in `.gitignore`. If the ignore file doesn't exist or lacks this entry, one is created/updated so the lockfile is not tracked by git.

### Data Structures

```go
type SimpleLock struct {
    lockPath string // Absolute path to the lock file. Set in AcquireLock; read in Unlock.
    file     *os.File // File handle opened during acquisition. After Unlock() the underlying file is closed and removed from disk but the field is never nilled out.
}
```

**`SimpleLock.Unlock() error`** — Closes the lock file and removes it from disk. Returns an error if either step fails; individual errors are captured into local vars (`err1`, `err2`) and only one is ever returned (the second operation's error may be silently discarded in favor of the first).

### Public Functions

**`SafeResolve(repoRoot, inputPath string) (string, error)`** — Resolves and validates an input path against a repository root. Returns the cleaned absolute path or an error on traversal detection. Uses pure path manipulation: `filepath.Abs`, `filepath.Clean`, `filepath.Rel`. No actual disk read/write occurs during this call. The security check is performed via prefix inspection of the relative path after resolution.

**`AcquireLock(repoRoot string) (*SimpleLock, error)`** — Acquires a simple file lock in the repo root. Requires creating the lockfile atomically (via `O_CREATE|O_EXCL`); if it already exists, acquisition fails with a "lock held" sentinel message. On success writes PID (newline-terminated). If PID write subsequently fails, `f.Close()` and `os.Remove(lockPath)` are called explicitly — this cleans up the partially-created lockfile so another process can retry.

**`EnsureGitignoreHasLockfile(repoRoot string) error`** — Ensures `.code-reducer.lock` is present in `.gitignore`. If the ignore file doesn't exist, one is created with a single line. Otherwise: scans for existing entry; if not found, appends a commented block plus the lockfile name. Opens `.gitignore` for appending with `O_APPEND|O_WRONLY`; if append fails, returns a wrapped error. No partial state change occurs beyond the open handle (deferred close); no explicit cleanup of any partial content is performed on write failure after opening.

### Error Handling Patterns

1. **Wrap pattern (`fmt.Errorf %w`)** — Used in every failure path:
   - `SafeResolve`: wraps `filepath.Abs` error as `"failed to get absolute root path"`; wraps traversal detection as a hardcoded sentinel message.
   - `AcquireLock`: wraps the raw `os.OpenFile` error when it's not `IsExist`.
   - `EnsureGitignoreHasLockfile`: wraps stat/read/write errors individually.

2. **Sentinel / Hardcoded messages** — Not wrapped with `%w`, returned as-is:
   - `"security violation: path traversal detected"` — in `SafeResolve` when relative path starts with `..`.
   - `"lock at %s is already held by another process..."` — when `os.IsExist(err)` on OpenFile. Domain-specific sentinel for lock contention.

3. **Error swallowing** — `Unlock()` captures each step's error into local vars (`err1`, `err2`) and returns whichever is non-nil (or nil if both succeed). The second operation's error may be silently discarded in favor of the first, or vice versa — only one error is ever returned.

4. **No panic recovery** — None of these functions recover from panics. Any unexpected panic propagates to the caller with an empty stack trace in `runtime` (Go's default).

---

## Subsystem: tools — Repository Traversal, Safe File I/O, and Git Utilities

### Responsibility

Provides three independent capability groups: **repository code discovery**, **safe file read/write operations** with TOCTOU race mitigation, **gitignore-aware filtering**, and **Git subprocess execution**. All functions are standalone (no methods on types); no exported structs or interfaces exist. No goroutines, channels, or sync primitives are used anywhere in the package—every call is synchronous and single-threaded.

### Repository Code Discovery

**`DiscoverCodeFiles(repoRoot string) ([]string, error)`** — Walks `repoRoot` recursively via `filepath.WalkDir`, skipping directories matching `.gitignore` patterns (compiled by `LoadGitignore`) or any dot-prefixed component (`.build`, `.svn`, etc.), then filters each leaf through `ShouldIgnoreFile`. Valid source paths are accumulated into a slice and returned. Walk callback errors from `filepath.WalkDir` are swallowed—individual directory entries that fail to stat return `nil` to the caller, making it impossible to distinguish an intentional skip from a walk failure downstream.

### Safe File Read / Write Operations

**`ReadFileSafely(repoRoot, virtualPath) ([]byte, error)`** — Resolves `virtualPath` through `security.SafeResolve` (path-traversal guard), then reads the resulting absolute path via `os.ReadFile`. Errors from both `SafeResolve` and `os.ReadFile` are wrapped with prefix `"failed to read file:"` using `%w`.

**`WriteFileSafely(repoRoot, virtualPath, content []byte) error`** — Performs TOCTOU-safe writes:
1. Resolve the safe absolute path via `security.SafeResolve`.
2. Call `os.MkdirAll(dir, 0755)` to create parent directories if missing; wraps with `"failed to create directory"`.
3. Open for write+create (`O_WRONLY|O_CREATE`, mode `0644`); wraps open failure with `"failed to open file for writing"`.
4. Call `os.Lstat(safePath)` (no symlink follow) and `f.Stat()`; compare via `os.SameFile(fiLstat, fiFstat)`. If they differ—indicating a symlink swap or race condition—return an error prefixed `"security violation"` or `"TOCTOU symlink race detected"`.
5. Truncate (`f.Truncate(0)`), then write content; wrap each with `"failed to truncate file:"` / `"failed to write file content:"`.

### Gitignore Handling and Binary Classification

**`LoadGitignore(repoRoot) ([]string, error)`** — Reads `.gitignore` from `repoRoot`, scanner processes line-by-line. Empty file returns `nil, nil`; non-existence (`os.IsNotExist`) also returns `nil, nil`. All other errors are wrapped. Patterns returned are active non-comment entries.

**`ShouldIgnoreFile(repoRoot, relPath string, gitIgnore *ignore.GitIgnore, ignoredExtensions []string) bool`** — Applies layered ignore rules:
- Compiles user-supplied patterns via the external `github.com/sabhiram/go-gitignore` package (`*ignore.GitIgnore`).
- Rejects paths with dot-prefixed components or `.egg-info` suffixes (build artifacts).
- Checks file extension against a provided blacklist.
- For unknown extensions, reads the first 1024 bytes and scans for null bytes via `IsBinaryFile`. If null bytes are present, returns `true` (binary → ignore).

**`IsBinaryFile(path string) bool`** — Opens the path; if `os.Open` fails, returns `false` (treats unreadable as non-binary). Reads up to 1024 bytes. Only wraps `io.EOF` (expected EOF) without error—other read errors return `false`. No error wrapping for open or other read failures beyond EOF handling.

### Git Subprocess Execution

**`RunGit(repoRoot string, args ...string) (stdout string, err error)`** — Constructs an `exec.Command` for `git`, prepends `--no-pager` to user-supplied arguments, sets `cmd.Dir = repoRoot`. Captures stdout into a `bytes.Buffer`, reads it back, trims whitespace. On success returns trimmed output; on failure wraps the original error with stderr text: `"git command failed: %v, stderr: %s"`.

**`VerifyGitRepo(repoRoot string) error`** — Two-stage validation:
1. Calls `exec.LookPath("git")`; if unavailable, returns a fresh error without wrapping—no reference to any prior failure since none exists at that point.
2. Otherwise delegates to `RunGit(repoRoot, "rev-parse", "--is-inside-work-tree")`. If the inner call fails, discards its returned error entirely and returns a new message: `"not a git repository (or any of the parent directories)"`.

### Error Handling Summary

| Function | Failure Mode | Strategy |
|---|---|---|
| `ReadFileSafely` | Resolve or read failure | Wrap with `"failed to read file:"`, `%w` |
| `WriteFileSafely` | Directory open, file open, lstat/stat mismatch, truncate, write | Contextual prefix per step; symlink race → `"security violation"` / `"TOCTOU symlink race detected"` |
| `LoadGitignore` | Non-existence or read error | `nil, nil` on missing/empty; wrap everything else |
| `DiscoverCodeFiles` | Walk entry failure | Swallowed—caller sees no signal |
| `IsBinaryFile` | Open failure or non-EOF read error | Returns `false`; EOF is expected and ignored |
| `RunGit` | Non-zero exit / exec error | Wrap original + stderr text with `%v`, `%s` |
| `VerifyGitRepo` | Missing git binary | Fresh error, no wrapping |
| `VerifyGitRepo` | Git query failure | Discard inner error; return fresh message only |

---

## Subsystem: engine — Documentation Generation Pipeline

### Responsibility

Implements an automatic software-documentation generation pipeline that uses a large language model (Ollama-compatible API) to analyze repository structure and produce markdown artifacts. The system operates in two modes—**init**, which generates fresh documentation from scratch, and **update**, which performs incremental regeneration based on detected code changes.

### LLM Client (`client.go`)

Provides synchronous and streaming communication with an Ollama-compatible LLM API. The client is immutable after construction; all fields are assigned only in `NewLLMClient`. Both modes share a base system prompt that establishes agent identity and operating rules.

**`CallLLM(messages []Message) (*Response, error)`** — Builds a JSON request body combining the configured model ID, user-supplied messages, and a leading system prompt message. The request is POSTed to `<BaseURL>/api/chat` with `"Content-Type: application/json"`. On success (200), the full response body is read into memory and parsed once; on non-200 status, up to 1024 bytes of the error body are captured for reporting. The HTTP client has a 10-minute timeout applied globally.

**`StreamLLM(messages []Message, onChunk func(string)) error`** — Performs an identical outbound POST but with streaming enabled. Each line from the response is read via `bufio.NewReader`, trimmed, and parsed as JSON. Non-empty content segments invoke the caller-supplied `onChunk` callback immediately. The loop exits on EOF or when a chunk's `Done` flag is true. Invalid JSON lines inside the stream are silently skipped—no error returned to the caller unless the reader itself fails.

**`GetBaseSystemPrompt() string`** — Returns static base prompt text.

**`GetDefaultSystemPrompt(command) string`** — Switches on `command`: `"module_synthesis"` returns a composition for per-module documentation; `"architecture"` returns a composition for global architecture documents.

### Response Parsing (`json_parser.go`)

Normalizes raw LLM output into parseable JSON before downstream consumption. The parser tolerates markdown code fences (with or without language tags) and handles bracket/brace mismatches within embedded text by walking character-by-character while respecting string boundaries.

**`StripOuterMarkdownFence(input string) string`** — Trims whitespace at entry, then applies a compiled regex to match content between ```` ``` ` (with optional `markdown`/`json` tag) and captures the inner text. No match returns input unchanged.

**`CleanJSONResponse(input string) (string, error)`** — Scans for the first `{` or `[`. Walks forward tracking an `inString` flag to skip escaped characters inside quoted strings. Pushes opening delimiters onto a stack; pops on matching close. Returns the substring from start index up to and including the closing character once the stack drains. If neither delimiter is found, input passes through unchanged.

**`UnmarshalJSONResponse(input string) (interface{}, error)`** — The cleaned string is passed directly to `encoding/json.Unmarshal`. Errors are wrapped with context: `failed to unmarshal JSON (cleaned: %q): <original>`.

### Chunk Reduction (`chunking.go`)

Implements a recursive chunk-reduction algorithm that processes collections of code/architecture items through an LLM-based synthesis pipeline and produces a single consolidated output. Items are grouped into batches bounded by `maxChunkChars` (3× the LLM context window), then each batch is synthesized independently, and intermediate results are concatenated and reduced again until one item remains—a tree-like reduction pattern.

**`ReduceInChunks(items []string) (string, error)`** — Algorithmic flow:
1. **Empty input** — Returns `("", nil)` immediately.
2. **Single-item truncation** — When exactly one item exceeds the context window, it is mutated in-place to `<items[0][:maxChunkChars]>\n...[truncated]>`. No error is returned; processing continues.
3. **Batching phase** — Items are greedily grouped into batches where each batch's combined length stays within `maxChunkChars`. Overflow starts a new batch.
4. **Fallback truncation** — If every item exceeds limits individually, each item is proportionally truncated and forced into a single batch. This prevents deadlock on unbatchable inputs.
5. **Single-batch terminal** — When exactly one batch exists after grouping, the entire set is sent directly to `CallLLM` for synthesis; the result is stripped of markdown fences before return.
6. **Multi-batch recursion** — Each batch is recursively reduced via `ReduceInChunks`. Intermediate results are concatenated into a single list, which then triggers another recursive call until reaching the base case where all items fit in one context window.

Error handling: Recursive-call errors and LLM synthesis errors propagate directly. Synthesis errors are wrapped with `"LLM error during synthesis: %w"`. Truncation is a silent side effect, not an error path—no warning logged unless it's the only item (in which case `logEvent` is invoked).

### Hierarchical Synthesis (`synthesize.go`)

Recursively walks a directory tree (`DirNode`), producing synthesized textual summaries for each node. Each directory's summary is built from child summaries plus per-file facts extracted by LLM calls. Unchanged files reuse cached SHA256 hashes and previously extracted facts; changed or new files trigger fresh extraction via configurable `ExtractionSteps`.

**`SynthesizeNode(node *DirNode, cache MetadataCache) (string, error)`** — Algorithmic flow:
1. **Context check** — Aborts early if context is cancelled.
2. **Cache hit short-circuit** — If node is not affected (`affectedDirs` map) and a cached summary exists, returns immediately without LLM calls.
3. **Recursive child synthesis** — Children are sorted; each is synthesized in order. Non-empty summaries accumulate into `childSummaries`.
4. **File fact extraction loop** — For each file: compute SHA256 via `computeSHA256`; decide cache hit vs. LLM-driven extraction; accumulate facts. Read errors on individual files are swallowed (`continue`).
5. **Component assembly** — File components followed by child subsystem summaries form an ordered slice.
6. **Chunked reduction** — All assembled components feed `ReduceInChunks` to produce the directory's final summary.
7. **Cache update & persist** — The result is stored in both `MetadataCache.Files[f]` (with new hash and facts) and `MetadataCache.Modules[node.Path]`. Module docs are written under `<docsDir>/modules/<safe-filename>.md` via `tools.WriteFileSafely`; write errors are wrapped.

Error propagation: LLM call failure during extraction is fatal for the node: wrapped as `"LLM error extracting %s for %s: %w"` and stops all further processing. No partial results returned. SHA256 computation or file read failures on individual files are swallowed; other files still contribute components. The result may be incomplete but not an error.

### Directory Tree (`tree.go`)

Defines the in-memory `DirNode` hierarchy and implements affected-directory propagation logic that distinguishes between directories containing changed files and unaffected ones, enabling incremental synthesis without re-analyzing unchanged subtrees.

**`FileChange`** — `Path string`, `Status string` (`"Added" | "Modified" | "Deleted"`). Input primitive for tracking changes across the repository.

**`DirNode`** — `Path string`, `Files []string`, `Children map[string]*DirNode`. Each node holds its own file list and a map of child subdirectories keyed by name.

**Affected-Directory Propagation**:
1. Individual directories are flagged as affected if any direct file appears in the change set, or if no cached module entry exists for the path, or if `modules/<safe-filename>.md` does not exist at the expected location under `docsDir/modules`.
2. Once individual nodes are flagged, ancestors are recursively marked affected by walking upward—ensuring parent summaries reflect changes even when they themselves contain no direct file modifications.

Error handling: `SafeResolve` errors in `determineAffected` are caught and ignored; the entire block is skipped silently. `os.Stat(absModulePath)` errors: only `os.IsNotExist(err)` triggers the affected flag; other Stat errors (permission denied, etc.) are treated as non-existence and silently ignored.

### Orchestrator (`orchestrator.go`)

Implements the automatic documentation generation pipeline for software repositories. Coordinates code discovery, tree building, hierarchical synthesis, metadata cache operations, standard doc regeneration, AGENTS.md maintenance, and module cleanup. Emits typed `Event` objects (type + message) so callers can observe pipeline progress without parsing log output.

**`Event`** — `Type string`, `Message string`. Typed event used for cross-cutting logging.

**RunInit Flow**:
1. Load `.gitignore`, apply configured ignore lists, scan repository for source-code files.
2. Convert flat file list into a hierarchical `DirNode` tree rooted at the repo root via `buildTree`.
3. Walk the tree bottom-up; each node is summarized by LLM invocation. The root node's summary becomes `rootSum`.
4. Invoke LLM to write `architecture.md` and `quickstart.md`, passing `rootSum` as context. Errors are wrapped with `"failed to generate ...: %w"`.
5. Append or create an "AI Agent Guidelines" section in `AGENTS.md`; if file exists and already contains the marker, skip. Write errors are swallowed.
6. Persist cache via `saveMetadataCache` with a `"local"` commit marker.

**RunUpdate Flow**:
1. Re-discover code files using identical filtering logic.
2. Detect changes via SHA256 comparison against cached hashes in `MetadataCache.Files`. Differences classify into `"Added"`, `"Modified"`, or `"Deleted"`.
3. For every changed file, walk upward from its parent to the root directory, marking each ancestor as affected. Deleted files are removed from cache immediately; modules whose directories are no longer active are purged via `os.Remove` (errors ignored).
4. Rebuild tree and prune cache so it reflects only currently existing code.
5. Propagate affect markers through the tree via `propagateAffected`. If no directories are affected, short-circuit with a "no modifications" message; return without further LLM calls.
6. Synthesize only affected nodes via `SynthesizeNode`, skipping unaffected subtrees entirely—reusing cached summaries for them.
7. Regenerate `architecture.md`/`quickstart.md` if they are missing or any top-level directory was affected; otherwise reuse existing files.
8. Persist updated cache with `"local"` commit marker.

Cross-cutting rules: **Gitignore stacking** — `.gitignore`, configured ignore lists, and the `docsDir` itself are all applied together when discovering code files. **Safe file resolution** — All paths resolved through `security.SafeResolve` before any filesystem operation. **Atomic writes** — Documentation written via `tools.WriteFileSafely`. **Module cleanup** — Modules whose directories no longer appear in the active set are removed from disk; removal errors are ignored (`_ = os.Remove`).

### Runner (`runner.go`)

Acts as the orchestrator for code analysis or documentation pipelines. Manages lifecycle execution from initialization through completion, handling state transitions based on a provided mode string. Ensures repository integrity by verifying that the lockfile is excluded from version control before executing pipeline logic.

**`Runner`** — Internal field `cfg *config.Config`. No exported fields; construction and configuration are external to this file's scope.

**`NewRunner(cfg *config.Config) *Runner`** — Factory for creating a new runner instance.

**`Run(ctx context.Context, repoRoot string, mode string, onEvent func(Event)) error`** — Main entry point. Acquires an exclusive lock on the repository; if acquisition fails, returns immediately with wrapped error. A deferred unlock releases the lock unconditionally regardless of how the function exits.

Mode dispatch:
- `"init"` → calls `client.RunInit(ctx, repoRoot, r.cfg, onEvent)`
- `"update"` → calls `client.RunUpdate(ctx, repoRoot, r.cfg, onEvent)`
- Any other value → returns `fmt.Errorf("unsupported mode: %s", mode)` as a fatal error.

Error handling summary:

| Step | Behavior on Failure |
|---|---|
| `EnsureGitignoreHasLockfile` | Swallowed; logged as warning via `onEvent`. Execution continues. |
| `AcquireLock` | Fatal, wrapped: `"failed to acquire repository lock: %w"`. Caller sees original error wrapped. |
| `client.RunInit` / `RunUpdate` | Propagated raw without wrapping. Deferred lock cleanup still runs. |
| Unsupported mode | Fatal, wrapped: `"unsupported mode: %s"`. |

### Utilities (`utils.go`)

Maps internal identifiers (module paths) onto safe, human-readable filenames for markdown output. Operates at the boundary between the engine and the documentation generation system.

**`ToSafeMarkdownFilename(modulePath string) string`**:
1. All filesystem separator characters are replaced with underscores (`_`). This normalizes platform-specific path separators into a single ASCII character.
2. If the resulting string is empty or equals `"."`, it is replaced with the literal string `"root"`.
3. A `.md` suffix is appended, signaling that the output is a markdown document file.

The mapping is deterministic and pure—same input always yields the same filename. No error return for invalid inputs. All transformations are best-effort with silent normalization.