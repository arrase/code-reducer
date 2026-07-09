# Configuration Resolution Module (`internal/config`)

## Responsibility & Data Flow

This module provides a structured configuration resolution system that loads, persists, and resolves `*Config` from multiple sources with explicit priority ordering: CLI flags > environment variables > YAML file values > hardcoded defaults. Empty string/zero values in upstream sources do not override downstream defaults; only non-empty values take precedence (with the exception of model ID, where an explicit zero-length value is treated as "not set" to allow fallback through the chain).

Configuration files are persisted at `<cwd>/.code-reducer.yaml` with Unix permission mode `0600`. The resolution pipeline follows a fixed sequence: existence check → load → merge ignore lists (deduplicated) → merge extension ignores (deduplicated) → apply default extraction steps if user-defined steps absent → resolve each field independently through the priority chain.

## Configuration Types

### `Config`

```go
type Config struct {
    ModelID          string           // yaml:"model_id"
    OllamaBaseURL    string           // yaml:"ollama_base_url"
    OllamaNumCtx     int              // yaml:"ollama_num_ctx"
    DocsDir          string           // yaml:"docs_dir"
    ExtractionSteps  []ExtractionStep // yaml:"extraction_steps"
    Ignore           []string         // yaml:"ignore"
    IgnoreExtensions []string         // yaml:"ignore_extensions"
}
```

### `ExtractionStep`

```go
type ExtractionStep struct {
    Name   string // yaml:"name"
    Prompt string // yaml:"prompt"
}
```

No methods are defined on either type. Both types implement the YAML unmarshalling contract via `yaml` tag annotations, allowing deserialization from `.code-reducer.yaml`.

## Public API Functions

### `ConfigExists(cwd string) bool`

Checks whether `.code-reducer.yaml` exists at `<cwd>/.code-reducer.yaml`. Returns `true` if a regular file is present; returns `false` otherwise. Any error from the underlying filesystem call (not found, permission denied, etc.) is swallowed — the function never returns an error type to its caller.

**I/O:** Read-only (`os.Stat`). Swallows all errors. No wrapping.

### `LoadConfig(cwd string) (*Config, error)`

Reads raw bytes from `<cwd>/.code-reducer.yaml`, then unmarshals them into a `*Config`. Returns the parsed config on success; returns an error (or nil config) on failure. Two distinct error behaviors apply:

- **File read failure** (`os.ReadFile`): The underlying error is returned verbatim, unwrapped, to the caller. No context string prepended.
- **YAML parse failure**: Wrapped with prefix `failed to parse yaml config:` using `%w` verb for error chain preservation.

### `SaveConfig(cwd string, cfg *Config) error`

Marshals a `*Config` into YAML and writes it to `<cwd>/.code-reducer.yaml` with permission mode `0600`. Returns an error on failure. Two distinct wrapping behaviors:

- **Marshal failure**: Wrapped with prefix `failed to marshal yaml:` using `%w` verb.
- **Write failure**: Wrapped with prefix `failed to write config file:` using `%w` verb.

No panic, no silent swallow — all errors propagate wrapped.

### `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config`

Resolves a fully populated `*Config` from multiple sources in priority order: CLI flags > environment variables > YAML file value > hardcoded default. Delegates initial file I/O to `LoadConfig`. If that call fails, the error is swallowed and an empty `*Config{}` is returned instead of propagating — the caller receives a non-nil config with zero-valued fields and no error to inspect.

Post-loading resolution steps (merge ignores, merge extensions, default extraction steps, model ID/base URL/context size overrides) execute without any further error checks or wrapping. These operate on the already-resolved struct.

### `MergeAndDeduplicate[T comparable](a, b []T) []T`

Generic utility that concatenates two slices of a comparable type and removes duplicates while preserving order. Used for combining user-provided ignore/extension lists with system defaults so entries appear only once.

## Constants & Defaults

### Package-Level Variables

| Variable | Type | Description |
|---|---|---|
| `DefaultIgnores` | `[]string` | Default directory ignore list |
| `DefaultIgnoredExtensions` | `[]string` | Default file extension ignore list |
| `DefaultExtractionSteps` | `[]ExtractionStep` | Default extraction pipeline for LLM prompts (four analysis stages) |

### Constants

```go
const CodeReducerModelIdEnvKey   = "CODE_REDUCER_MODEL_ID"
const OllamaBaseUrlEnvKey        = "OLLAMA_BASE_URL"
const OllamaNumCtxEnvKey         = "OLLAMA_NUM_CTX"
const OllamaDefaultBaseURL       = "http://localhost:11434"
const OllamaDefaultNumCtx        = 8192
const ConfigFileName              = ".code-reducer.yaml"
```

## Error Handling Strategy Summary

| Behavior | Where It Happens | Notes |
|---|---|---|
| **Swallow (silent)** | `ConfigExists` stat errors; `ResolveConfig` load failure → empty config | No wrapping, no sentinel value. Caller receives bool or non-nil config with zero-valued fields. |
| **Propagate raw** | `LoadConfig` file-not-found / permission-denied (`os.ReadFile`) | Unmodified error returned to caller. |
| **Wrap with prefix + `%w`** | All YAML parse, marshal, and write errors in `LoadConfig` and `SaveConfig` | Error wrapping preserved via `%w`. |
| **No panic** | Anywhere | No panics occur within this module's public surface. |
| **Sentinel values** | None | No custom error types or sentinel interfaces used. |

## I/O Communication Table

| Function | Read/Write | Path | Notes |
|---|---|---|---|
| `getConfigPath` | Pure (no filesystem access) | — | Unexported helper; returns string only. |
| `ConfigExists` | **Read** (`os.Stat`) | `<cwd>/.code-reducer.yaml` | Swallows all stat errors → returns `false`. |
| `LoadConfig` | **Read** (`os.ReadFile`) | `<cwd>/.code-reducer.yaml` | Unmarshals YAML into `*Config`. Two distinct error behaviors. |
| `SaveConfig` | **Write** (`os.WriteFile`, 0600) | `<cwd>/.code-reducer.yaml` | Writes marshalled YAML with Unix permission mode `0600`. |
| `ResolveConfig` | **Read** (via `LoadConfig`) | `<repoRoot>/.code-reducer.yaml` | Delegates file I/O entirely. No additional direct filesystem access. |

## Key Resolution Rules

- Config files stored with restricted permissions (`0600`).
- Empty string values in YAML/CLI/env do not override defaults; only non-empty values take precedence, except for model ID where explicit zero-length is treated as "not set" to allow fallback through the priority chain.
- Context size validation requires positive integers only (enforced downstream of this module).
- A predefined default extraction pipeline provides four analysis stages with specific prompts, ensuring consistent output structure across runs.

---

# Engine Pipeline Module (`internal/engine`)

## Responsibility & Data Flow

The engine module orchestrates a single-purpose pipeline: **extract architectural facts from source files, synthesize them into Markdown documentation via LLM calls, and persist results incrementally across runs.** It is invoked once per repository with either an initialization mode (full rebuild) or an update mode (incremental refresh). The pipeline's state survives between invocations through a JSON-backed metadata cache stored at `{docsDir}/.metadata.json`.

**Data flow summary:**

```
Runner.Run() ──► LLMClient.CallLLM / StreamLLM ──► json_parser.go
     │                                              ▲
     ├──► tree.go (DirNode construction + change detection)   │
     ├──► synthesize.go (per-file extraction → chunking → synthesis)
     ├──► cache.go (persistent hash/facts/module summary store)
     └──► orchestrator.go (pipeline coordination, global doc generation)
```

All four public entry points funnel through `Runner.Run()`. The runner acquires an exclusive lockfile, instantiates a fresh `LLMClient` from config, branches on the mode string (`init` | `update`), and emits typed events via the caller-supplied `onEvent` callback. All domain-specific work lives downstream of the runner; it never contains synthesis logic itself.

## LLM Interaction Layer (`client.go`)

The engine talks to Ollama-style endpoints through a single struct, `*LLMClient`. It is constructed once per run via `NewLLMClient(modelID, baseURL, numCtx)` and reused for every call in that run's lifetime. The HTTP client inside it carries a fixed 10-minute timeout; there is no way to swap or inject a custom one after construction.

**Synchronous path — `CallLLM`:** sends a complete request, blocks until the full response arrives, returns parsed content or error. Non-OK status codes do not propagate the raw read/Do error: up to 1024 bytes are consumed and returned as `"ollama api error: status %d, response: %s"`.

**Streaming path — `StreamLLM`:** opens a streaming connection; each decoded content chunk is forwarded to an injected `onChunk(string)` callback. Empty lines are skipped. Malformed JSON on any individual line is silently ignored and the loop continues; that specific line's data never reaches the callback and no signal propagates up. Iteration stops when `chunk.Done == true` or when `io.EOF` arrives.

**Prompt assembly:** every call prepends a base system prompt (hardcoded in `GetBaseSystemPrompt`) that defines the agent persona and defensive output rules. `GetDefaultSystemPrompt(command)` extends this base with task-specific framing for `"module_synthesis"` and `"architecture"`; any other command returns only the base prompt.

**Internal request preparation — `prepareOllamaRequest`:** builds the HTTP body once per call: prepends a system message to user messages, sets context window size from client config (`NumCtx`), optionally forces JSON output format. Reused across both batch and streaming paths.

## Persistent Metadata Cache (`cache.go`)

All state between runs lives in `MetadataCache`, serialized as `{docsDir}/.metadata.json`. The struct holds three maps: `Files` (keyed by file path → `FileCacheEntry{SHA256, Facts}`), `Modules` (keyed by directory path → synthesized summary string), and a scalar `LastDocumentedCommit` tracking the last commit SHA that was fully documented.

**Load semantics — `loadMetadataCache`:** if the metadata file does not exist, returns an empty-but-initialized cache (`Files: make(map[string]FileCacheEntry)`, `Modules: make(map[string]string)`). Any other read/parse/write failure is wrapped with `%w` and returned as a hard error.

**SHA-256 computation — `computeSHA256`:** reads the raw bytes of any file at its virtual path via `tools.ReadFileSafely`, returns the hex-encoded SHA-256 digest in memory. The hash itself is not persisted; callers store it into `FileCacheEntry.SHA256`, which then gets flushed to disk by `saveMetadataCache`.

**Save semantics — `saveMetadataCache`:** marshals the full cache back to `.metadata.json` and writes via `tools.WriteFileSafely`. Marshaling or write failures propagate raw, unwrapped. No panic anywhere in this file.

**Concurrency:** none. The maps are nil-initialized by default; reads reassign them via `make()` if nil before use. Writes happen without locking. Read/write race conditions exist but no protection mechanism is present.

## Tree Construction & Change Detection (`tree.go`)

The engine builds an in-memory directory tree from flat file paths using `DirNode{Path, Files, Children}`. The root represents the current working directory; each path is split on `/` to walk down and create intermediate nodes as needed.

**Change detection — `determineAffected`:** walks the tree top-down and marks a directory as affected under three conditions: any file in the directory matches one of the filtered `FileChange{Path, Status}` records (`Added`, `Modified`, `Deleted`); the safe resolved absolute path for the directory's markdown equivalent does not exist on disk (used when a module was removed but metadata has not yet been cleaned up); or the directory has no entry in `cache.Modules`. For each matched condition the directory is added to `affectedDirs`; recursion continues into children.

**Bottom-up propagation — `propagateAffected`:** after the top-down walk completes, this function walks bottom-up so that any parent with an affected descendant becomes marked too. The result: every path whose subtree has at least one source of impact appears in `affectedDirs`, regardless of whether it was a direct change or inherited from a child.

## Synthesis Engine (`synthesize.go`)

All synthesis logic lives unexported inside `engine` as `synthesizeNode`. It is the recursion anchor: every directory-level summary is produced by calling this function on each child first, then assembling their summaries plus per-file facts into a final parent summary.

**Per-file processing:** for each file in a node's `Files` slice, the engine reads bytes safely (skip silently if unreadable), computes SHA-256, checks the cache for a hash match to reuse stored facts without re-prompting. If no cache hit, the text is chunked with overlap and every extraction step runs across all chunks; each step's results are then consolidated into one fact per step via `reduceFileFacts`. The resulting `FileCacheEntry{SHA256, Facts}` is cached for future reuse.

**Component assembly:** builds a slice containing one entry per file (prefixed with filename) plus one entry per child summary (prefixed with `"Subsystem"`). If no components exist, the module cache is cleared and an empty string is returned. Otherwise `reduceInChunks` merges them into a single final summary.

**Persistence:** after synthesis completes for a directory, the final summary is written to `{docsDir}/modules/<safe-filename-of-path>.md`. Write failures are wrapped with `%w` and returned as error. Any read failure during per-file processing is swallowed; the file is skipped entirely without affecting the rest of the node's processing.

**Cache mutations:** `cache.Files[f] = FileCacheEntry{SHA256: fileHash, Facts: facts}` updates the per-file cache after extraction. `cache.Modules[node.Path]` stores the synthesized summary keyed by directory path; it is also consulted at entry if no descendants flag the directory as affected and a cached module exists — in which case the cached value is reused without re-prompting.

## Chunking & Reduction (`chunking.go`)

All four functions here are unexported package-private: `reduceInChunks`, `reduceFileFacts`, `reduceItems`, `chunkTextWithOverlap`. None of them mutate global state or use concurrency primitives.

**Chunking with overlap — `chunkTextWithOverlap`:** splits a string into overlapping chunks (default 800-character overlap). If the text fits in one chunk, returns `[]string{text}`; otherwise iterates with overlap logic. No error return at all.

**Reduction pipeline:** `reduceItems` groups strings into batches whose total rune length stays within `maxChars`. Each batch is sent to an LLM and the response (markdown fences stripped) is returned as a single string. All returned strings are concatenated into one intermediate result, which is then fed back through `reduceItems` recursively until only one final string remains.

**LLM-specific reductions — `reduceInChunks` and `reduceFileFacts`:** both follow the same pattern but use different system prompts (`module_synthesis` for architecture synthesis; a base prompt with a fixed role instruction for fact consolidation). LLM errors are wrapped with descriptive prefixes: `"LLM error during synthesis: %w"` and `"LLM error during file fact consolidation: %w"`. Both return `("", nil)` for empty or single-item inputs without taking the error path.

**Infinite-loop guard:** if every item ends up in its own batch and there are more than one item, the function silently truncates each proportionally to `maxChars / len(items)` and forces them into a single batch — no error is returned. Callers receive whatever truncated result was produced with no way to know truncation occurred unless they inspect output length/content themselves.

## JSON Response Parsing (`json_parser.go`)

This module solves one business rule: extract valid structured data from LLM-style API responses wrapped in markdown code fences. All three functions are pure — zero external I/O, no DB, no network.

**StripOuterMarkdownFence:** detects and removes surrounding ````json` or ```markdown` fences using a package-level compiled regex (`markdownFenceRe`). If no fence matches, returns the original trimmed input unchanged. No error returned; caller cannot distinguish "no fence" from "fence with empty body."

**CleanJSONResponse:** applies `StripOuterMarkdownFence`, then performs bracket-matching extraction: tracks brace depth while skipping content inside string literals (respecting escape sequences), and when brackets balance to zero returns the matched substring. If brackets never close, returns the full trimmed input unchanged — caller receives either valid JSON or garbage with no sentinel to tell which.

**UnmarshalJSONResponse:** the only function that surfaces errors. Calls `CleanJSONResponse` internally, then invokes `encoding/json.Unmarshal`. Failure is wrapped as `"failed to unmarshal JSON (cleaned: %q): %w"`, preserving the original error chain for `errors.Unwrap`. No panic anywhere in this file.

## Orchestrator (`orchestrator.go`)

The orchestrator coordinates the full documentation pipeline and exposes three public methods on `*LLMClient`: `GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum, logEvent)`, `RunInit(ctx, repoRoot, cfg, onEvent)`, and `RunUpdate(ctx, repoRoot, cfg, onEvent)`. Two internal helpers — `setupPipeline` and `teardownPipeline` — are package-private.

**Disk I/O surface:**
- Writes: `architecture.md` and `quickstart.md` via `tools.WriteFileSafely` (wrapped with `%w`) during global doc generation; `{repoRoot}/docsDir/modules/` created via `os.MkdirAll` mode 0755 in init only.
- Reads: `.gitignore` loaded once per run (errors swallowed as log warning); metadata cache load (same swallow pattern).
- Append-only writes to `{repoRoot}/AGENTS.md` with `"AI Agent Guidelines"` header check — errors assigned to `_`, never returned or logged, preventing duplicate content on re-runs.

**Global doc decision:** `architecture.md`/`quickstart.md` are only refreshed when at least one module's root directory was affected OR either file is missing entirely; otherwise they remain intact without regeneration.

**Affected propagation:** in update runs, for each change the system walks upward from the file's directory to mark all parent directories as affected. This ensures that if a subdirectory has changes, all ancestor directories are flagged too, so parent summaries reflect child changes during re-synthesis.

**Idempotent AGENTS.md writes:** only appends when the header marker isn't already present. If the read itself fails, it immediately writes fresh guidelines without checking — errors assigned to `_`. No log event emitted for this path.

## Runner (`runner.go`)

`Runner{cfg *config.Config}` is the top-level entry point that coordinates a full run from lock acquisition through completion. It owns the execution lifecycle: validates gitignore hygiene (best-effort), acquires an exclusive repository lock, instantiates the LLM client from config, branches on mode, and releases the lock via defer regardless of outcome.

**Lock semantics:** `security.AcquireLock(repoRoot)` is called before any work begins with a deferred unlock guaranteeing release. Acquisition failure wraps as `"failed to acquire repository lock: %w"` and returns immediately — short-circuits all downstream work (no LLM client creation, no pipeline execution). Lock acquisition failure is treated as fatal; there is no recovery attempt or fallback path.

**Git hygiene:** `security.EnsureGitignoreHasLockfile(repoRoot)` appends the lockfile entry to `.gitignore`. Failure is swallowed — logged as an event with `"Warning:"` prefix, then execution continues regardless of severity. No early return. The operation is not critical to correctness; a missing lockfile in `.gitignore` is acceptable.

**Mode dispatch:**
- `init` → runs the initialization pipeline (first-time documentation generation: full tree build, all directories synthesized bottom-up, standard docs generated from root summary).
- `update` → runs the update pipeline (change detection via SHA-256 comparison against cached map, affected dir marking, cache pruning for deleted files, selective re-synthesis on affected paths only).
- Any other mode → returns an error. Unknown modes are rejected outright with no wrapping.

**Event callback pattern:** a user-supplied `onEvent` function is threaded through every stage of execution. Each step emits typed `"status"` events with messages, allowing external consumers to observe progress without the runner needing to know what they are. No domain logic for documentation generation lives in this file — it only coordinates and forwards.

## Utilities (`utils.go`)

`ToSafeMarkdownFilename(modulePath string)` maps internal code paths to readable Markdown filenames by replacing path separators with underscores. Pure string manipulation, no external I/O, no global state mutation, no concurrency primitives. Returns a single `string`; any failure mode results in an undefined value — no error return type exists for this function.

## Concurrency & Safety Summary

No file in the engine module uses `sync.Mutex`, channels, atomic operations, or equivalent concurrency primitives anywhere except within internal types not visible here. All state mutations occur on a single goroutine's call stack during recursion (`synthesizeNode`). The metadata cache is read/written without locking — race conditions exist but no protection mechanism is present.

**No panics:** every function returns `(value, error)` or equivalent; no `panic`, `runtime.Goexit()`, or unhandled recover blocks are present in any of the examined files.

## Error Wrapping Patterns

| Category | Functions | Notes |
|---|---|---|
| Swallowed / logged (no return) | lockfile gitignore check, `.gitignore` load, metadata cache load, AGENTS.md read failures, `os.Remove` on stale module files | No error returned to caller. |
| Wrapped with `%w` | all LLM errors from `CallLLM`, file write failures during synthesis and global doc generation, lock acquisition failure, JSON unmarshal failure | Error chain preserved via `%w`. |
| Propagated directly (no wrapping) | pipeline errors (`RunInit`, `RunUpdate`), context cancellation signals | Raw error surfaced. |

---

# Security Module (`internal/security`)

## Responsibility

This package provides path-traversal prevention, cross-process coordination via file-based locking, and `.gitignore` hygiene for the lock artifact. It operates on a single repository root; no global or package-level mutable state exists. All external I/O is filesystem-bound.

## Constants & Types

### `LockFileName`

```go
const LockFileName = ".code-reducer.lock"
```

Exported string constant identifying the lockfile artifact. Referenced by both `AcquireLock` and `EnsureGitignoreHasLockfile`.

### `SimpleLock`

```go
type SimpleLock struct {
    lockPath string
    file     *os.File
}
```

Per-instance value used only as the return from `AcquireLock`. Fields are private. Allocation is local to each call; no sharing across goroutines or process invocations.

## Data Flow: Lock Lifecycle

`AcquireLock` → `SimpleLock.Unlock()` forms a linear lifecycle. `Unlock` closes the underlying handle and removes the file via `os.Remove`. The order is close-then-remove, which aligns with POSIX semantics (close invalidates the handle). No coordination exists for concurrent callers holding locks; if another process expects to observe a successful write before removal, there is no synchronization primitive governing that path.

## Path Resolution: `SafeResolve`

Canonicalizes `repoRoot` and `inputPath` into absolute form via `filepath.Abs`, then computes the relative offset from root. If the relative prefix begins with `..`, returns `"security violation: path traversal detected: %q"`. Otherwise returns the cleaned absolute path. Wraps any `filepath.Abs` error with context `"failed to get absolute root path: %w"`.

## Lock Acquisition & Release: `AcquireLock` / `SimpleLock.Unlock()`

Opens `.code-reducer.lock` in repoRoot using O_EXCL semantics; on success, writes the current process PID via `fmt.Sprintf("%d\n", os.Getpid())`. Atomic creation via O_EXCL ensures that if an existing lock is detected at acquire time, surfaces a descriptive error suggesting manual cleanup of a stale lockfile rather than silently failing.

**Unlock**: Closes the open handle (if non-nil) and deletes the file from disk. Returns last non-nil error between close and remove — priority given to whichever operation fails; if close already failed, remove errors are not re-surfaced unless close succeeded first.

## Git Hygiene: `EnsureGitignoreHasLockfile`

Reads `.gitignore` via `os.ReadFile`, tolerating absence. Scans every line for one matching the lock filename. If found, returns success without modification. Otherwise appends a dedicated comment line and the lock filename in append-create mode (`O_APPEND|O_CREATE|O_WRONLY`), preserving pre-existing contents.

## Concurrency Model

No Go concurrency primitives (mutexes, channels) exist within this package. Cross-process coordination relies exclusively on OS file semantics via O_EXCL creation. No in-memory mutable state protected by any mutex or channel exists. Each `AcquireLock` call allocates a new `SimpleLock`; fields are not shared across goroutines or process invocations.

## Side Effects Summary

| Function | I/O Target | Action |
|---|---|---|
| **SafeResolve** | Local filesystem (repo root) | Reads `filepath.Abs` to resolve absolute path. No writes. |
| **AcquireLock** | `.code-reducer.lock` in repoRoot | Creates file with O_EXCL, writes PID (`fmt.Sprintf("%d\n", os.Getpid())`). Atomic creation via O_EXCL. |
| **SimpleLock.Unlock()** | `.code-reducer.lock` in repoRoot | Calls `os.Remove(l.lockPath)` — deletes the lockfile on disk. Does NOT close `f` (caller's responsibility). |
| **EnsureGitignoreHasLockfile** | `.gitignore` in repoRoot | Reads via `os.ReadFile`. If file missing (`IsNotExist`), skips read path. Appends `# Code-Reducer Lockfile\n.code-reducer.lock\n` if lockfile not already listed. Uses O_APPEND\|O_CREATE\|O_WRONLY. Writes with `f.WriteString`. |

## Error Handling Patterns

| Function | Strategy |
|---|---|
| **SafeResolve** | Wraps `filepath.Abs` error with context `"failed to get absolute root path: %w"`. Separately detects traversal via `strings.HasPrefix(rel, "..")` and returns a sentinel-style message (`"security violation: path traversal detected: %q"`). |
| **AcquireLock** | On O_EXCL failure, checks `os.IsExist(err)` first. If true → descriptive error telling user the lock is held by another process, suggesting manual deletion of stale locks. Otherwise wraps raw OS error with `%w`. |
| **SimpleLock.Unlock()** | Returns last non-nil error (close or remove). Priority: if close fails AND remove succeeds, returns close error. If close succeeds AND remove fails, returns remove error. Uses `&& err == nil` guard to avoid masking remove errors when close already failed — though this logic is inverted in practice (it returns the first non-nil, then overrides only if previous was nil). |
| **EnsureGitignoreHasLockfile** | On read: treats `IsNotExist` as success (silently skips). Wraps other read errors with context. On write: returns wrapped error if append fails. No panic paths — all OS errors are returned to caller. |

## Observations

- All external I/O is filesystem-only. No network, no DB, no stdin/stdout writes beyond `fmt.Sprintf`.
- Errors are never panicked; all surfaced as `(result, error)` tuples except `EnsureGitignoreHasLockfile` which returns only an error.
- `SimpleLock.Unlock()` does NOT close the file before removing — this may be intentional (close truncates), but if another caller holds the lock and expects to see a successful write completion before removal, there is no coordination.

---

# Repository Tooling Module (`internal/tools`)

## Module Responsibility & Data Flow

The `internal/tools` package provides two capability clusters: **safe filesystem operations** within a bounded repository root (`file_tools`) and **git process orchestration** (`git_tools`). Both clusters share the same safety contract—no mutable global state, no concurrency primitives, sequential execution only. All functions return `(result, error)`; panics are absent.

Data flow is unidirectional: callers pass in a `repoRoot` boundary and receive either structured output or a wrapped error. No shared state exists between functions, so call order is undefined from the module's perspective alone.

## File Operations (`file_tools`)

### Safe Read / Write

- **ReadFileSafely** — resolves the input path through `SafeResolve` (external dependency) and reads via `os.ReadFile`. Errors are wrapped with contextual prefix; no buffering beyond OS page cache is applied.
- **WriteFileSafely** — resolves path, creates parent directories (`MkdirAll`), opens target with `O_WRONLY|O_CREATE`, then applies a TOCTOU mitigation: an `Lstat` followed by `Fstat` compares inodes to detect symlink substitution between open and write. If the inode differs, a wrapped error is returned. Partial writes that fail after truncation are not rolled back; this is a documented limitation rather than an unhandled path.

### Ignore Decision Tree (`ShouldIgnoreFile`)

The decision tree evaluates four layers in order:

1. **Compiled gitignore patterns** — passed as `*ignore.GitIgnore` (compiled externally via `sabhiram/go-gitignore`, not within this file).
2. **Path-component heuristics** — dot-prefixed directories and `.egg-info` suffixes are auto-ignored without external configuration.
3. **Known text-file extensions** — a constant map of recognized source extensions short-circuits further checks.
4. **Binary detection fallback** — if no prior layer matched, the file is treated as ignored (binary).

The function returns `bool`. If `SafeResolve` fails (path outside repo root), it returns `true`; anything beyond the repository boundary is assumed safe to ignore by design.

### Code Discovery (`DiscoverCodeFiles`)

Walks the repository tree via recursive directory traversal. Each directory node checks its name against dot-prefix and `.egg-info` patterns, plus compiled gitignore matches — a match yields `SkipDir`. File nodes run through `ShouldIgnoreFile`; survivors are appended as slash-normalized relative paths. Walk errors encountered during iteration are dropped; only the accumulated walk error is surfaced at function exit.

### Binary Detection (`IsBinaryFile`)

Reads up to 1024 bytes and scans for null bytes. Returns `true` on detection, `false` otherwise. If the file cannot be opened, it returns `false`; this open-failure path is treated as a false negative rather than surfacing an error.

## Git Operations (`git_tools`)

### Process Execution (`RunGit`)

Constructs a command prefixed with `--no-pager`, executes in the supplied `repoRoot`, and captures both stdout/stderr into memory buffers. Whitespace is trimmed from captured output before return. If execution fails, stderr content is folded into a single wrapped error message alongside any captured stdout.

### Repository Validation (`VerifyGitRepo`)

Two-step check: first confirms the `git` binary exists in system PATH via `LookPath`; second invokes `rev-parse --is-inside-work-tree`. Either failure returns a distinct error — "not found" for missing binary, or explicit message indicating the directory is not inside any git work tree (or no parent qualifies).

## Module-Level Observations

- **No standard library package names are referenced** in this documentation; all external dependencies are called by fully qualified path.
- **Error wrapping is consistent**: `fmt.Errorf` with `%w` or `%v`/`%s` prefixes throughout. Errors support standard unwrapping semantics (`errors.Is`, `errors.Unwrap`). No sentinel errors defined.
- **No network calls, no database access** observed in either file.
- **Side effects are limited to disk I/O and process execution**. Both files spawn child processes (git binary) or perform direct filesystem reads/writes.