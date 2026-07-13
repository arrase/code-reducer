# Code-Reducer — System Architecture Reference

## Module Responsibility and Data Flow

Code-Reducer is an AI-driven code synthesis system that scans Go source repositories and produces structured Markdown documentation (wiki pages) via orchestration of LLM-based analysis steps. The pipeline operates entirely within the repository filesystem; no network or database interactions occur outside the Ollama HTTP transport layer used by `internal/engine`. All operations are scoped to paths resolved relative to a caller-supplied `repoRoot`.

The system follows a **Map-Reduce** pattern: source files are discovered (map), facts are extracted per-file via LLM calls, and module summaries are synthesized bottom-up through directory traversal (reduce). The CLI entry point (`cmd`) validates environment state, resolves configuration from disk or defaults, verifies engine mode consistency with project lifecycle state, instantiates the runner, and executes the documentation pipeline.

```
OS env / CLI flags → cmd.ResolveConfig() → *config.Config
repoRoot           → security.SafeResolve() → absolute path (symlink-safe)
*config.Config     → engine.NewRunner(cfg).Run(ctx, repoRoot, Mode, callback)
                    │
                    ├─ Lock acquisition via security.AcquireLock(repoRoot)
                    ├─ metadata cache load/save at <docsDir>/<metadataFileName>
                    ├─ gitignore merge + code file discovery (tools.DiscoverCodeFiles)
                    ├─ SHA256 hashing per discovered file (computeSHA256)
                    ├─ DirNode tree construction → affected status propagation
                    ├─ Map: extractFileFacts(ctx, LLM, chunked content) per extraction step
                    ├─ Reduce: synthesizeNode(node *DirNode) bottom-up
                    ├─ Standard docs generation (architecture + quickstart) via 2× LLM calls
                    └─ metadata cache persist → completion event
```

## Subsystem: cmd — CLI Orchestration Layer

### Entry Point and Mode Dispatch (`root.go`)

`RootCmd *cobra.Command` is a singleton constructed at package init. All subcommands register onto it during their respective `init()` functions; no mutation occurs post-initialization. The persistent flags are:

| Flag | Storage | Scope |
|------|---------|-------|
| `modelIDFlag string` | cobra flag via `StringVar` on RootCmd | Process lifetime, modified during flag parsing |
| `numCtxFlag string` | same pattern as above | Process lifetime, modified during flag parsing |

The public execution path is:

```go
executeCommand(mode engine.Mode) error // dispatches to engine runner with mode flag
```

It performs these steps in order: verify Git repository presence via `tools.VerifyGitRepo(repoRoot)` (errors returned as-is), resolve configuration from disk or defaults, validate engine mode against project state (`init` requires uninitialized; `update` requires existing wiki — inconsistent modes fail fast with a descriptive error), instantiate `engine.NewRunner(cfg)`, register SIGINT/SIGTERM handlers on the background context via `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)`, and invoke `Run(ctx, repoRoot, engine.Mode(mode), callback)` wrapping its return as `"documentation run failed: %w"`. The event callback prints `EventStatus`/`EventError` to stdout/stderr; no error propagation from event emission.

### Subcommand Registration (`init.go`, `update.go`)

Both follow the identical thin-wrapper pattern: define an unexported `*cobra.Command`, assign its `RunE` to a function that calls `executeCommand` with a mode constant, register under `RootCmd`. No mutable state; no synchronization. Errors propagate through to cobra for exit-code handling.

- **Init** — `RunE` → `executeCommand(engine.ModeInit)` → engine performs repository scan + wiki generation. Actual I/O (disk reads/writes, network calls) occurs inside `internal/engine`; not visible here.
- **Update** — `RunE` → `executeCommand(engine.ModeUpdate)` → engine handles update workflow. Same encapsulation: all I/O is downstream in `internal/engine`.

### Interactive Setup Wizard (`setup.go`)

Bootstraps the user's first-time or re-configuration experience by collecting LLM pipeline parameters through guided stdin prompts. Persists `.code-reducer.yaml`, used throughout the application lifecycle. Functions:

| Function | Parameters | Return Type(s) | Description |
|----------|-----------|----------------|-------------|
| `RunSetupFlow(repoRoot string)` | repo root path | `error` | Orchestrates setup: resolve cwd, load existing config if present, prompt for each parameter in interactive order (model ID → base URL → context size with integer validation → ignore patterns → docs dir), preserve extraction pipeline system prompts from existing config or initialize with built-in defaults, assemble new Config, persist to disk. |
| `promptStringList(reader *bufio.Reader, promptMsg string, existingList []string)` | stdin reader, prompt message, current list of strings | `([]string, bool)` | Prompts for comma-separated list; returns entered values and whether they were modified (empty → use existing). Stdin read errors written to stderr as warnings; fallback preserves existing list. |
| `promptString(reader *bufio.Reader, promptMsg string, existingVal string)` | stdin reader, prompt message, current value | `string` | Prompts for single string value; returns entered value or default if input empty. Stdin read errors written to stderr; function returns `existingVal`. |

### Configuration Persistence Contract (Setup Wizard)

| Step | Operation | Failure Behavior |
|------|-----------|-----------------|
| 1 | `os.Getwd()` → repoRoot | Wrapped via `fmt.Errorf("%w", err)` and returned — setup exits non-zero, error printed to stderr. |
| 2 | `config.LoadConfig(repoRoot)` | If file absent or invalid, returns error → falls back to built-in defaults; no user-visible failure. |
| 3 | `promptString` / `promptStringList` stdin read errors | Error written to stderr as warning; function returns pre-populated default without aborting. |
| 4 | `strconv.Atoi(ctxInputStr)` parse failure or value ≤ 0 | Falls back to `existingNumCtx`. No error propagated upward for this case. |
| 5 | `config.SaveConfig(repoRoot, newCfg)` | Wrapped via `fmt.Errorf("error saving configuration: %w", err)` and returned — setup exits non-zero with descriptive message on stderr. |

### External I/O Summary (cmd)

All interactions are local (disk + stdin/stderr). No network calls, database access, or API interactions occur in this module. The global `setupCmd *cobra.Command` is constructed once at startup and not concurrently accessed in normal usage.

## Subsystem: internal/config — Configuration Management & Resolution

### Responsibility and Data Flow

This package provides configuration loading, persistence (atomic write-through-rename), priority-chain resolution for the synthesis pipeline. The data flow proceeds as follows: **`ResolveConfig(repoRoot, modelIDFlag, numCtxFlag)`** merges CLI flags → environment variables → YAML file contents → built-in defaults into a fully-resolved `*Config`. When committed to disk, **`SaveConfig(cwd, cfg)`** performs an atomic write-through-rename sequence so readers never observe a partially-written state. At runtime, **`LoadConfig(cwd)`** reads and parses that persisted configuration. The module exposes three extraction-phase types (`ExtractionStep`, plus the pre-populated `DefaultExtractionSteps` slice) that drive downstream synthesis prompts during code analysis. All operations are local to the filesystem; no network or database interactions occur within this package.

### Types — `config.go`

**`ExtractionStep`**

```go
type ExtractionStep struct {
    Name   string `yaml:"name"`
    Prompt string `yaml:"prompt"`
}
```

Represents a single prompt-based analysis phase applied to source files. The slice of these is used by the synthesis pipeline; each entry maps an extraction name (e.g., `"API_SIGNATURES"`) to its system prompt text.

**`Config`**

```go
type Config struct {
    ModelID                    string   `yaml:"model_id"`
    OllamaBaseURL              string   `yaml:"ollama_base_url"`
    OllamaNumCtx               int      `yaml:"ollama_num_ctx"`
    DocsDir                    string   `yaml:"docs_dir"`
    SystemPrompt               string   `yaml:"system_prompt"`
    ModuleSynthesisPrompt      string   `yaml:"module_synthesis_prompt"`
    ArchitecturePrompt         string   `yaml:"architecture_prompt"`
    FileFactConsolidationPrompt string  `yaml:"file_fact_consolidation_prompt"`
    ExtractionSteps            []ExtractionStep `yaml:"extraction_steps"`
    Ignore                     []string `yaml:"ignore"`
}
```

Holds all configuration parameters consumed by the synthesis pipeline. Fields are initialized from a priority chain of sources (see **resolve.go**). The struct is decoded/encoded via YAML with standard unmarshaling semantics; missing fields retain Go zero values.

### Constants — `config.go`

| Constant | Value | Purpose |
|---|---|---|
| `CodeReducerModelIDEnvKey` | `"CODE_REDUCER_MODEL_ID"` | Env var key for model ID override |
| `OllamaBaseURLEnvKey` | `"OLLAMA_BASE_URL"` | Env var key for Ollama base URL |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | Env var key for context size override |
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | Default Ollama endpoint |
| `OllamaDefaultModelID` | `"ornith:9b"` | Default model identifier |
| `DefaultDocsDir` | `"wiki"` | Default output directory for generated docs |
| `ConfigFileName` | `".code-reducer.yaml"` | Persisted config filename |

### Integer Constants — `config.go`

| Constant | Value | Purpose |
|---|---|---|
| `OllamaDefaultNumCtx` | `8192` | Default Ollama context size |

### Lifecycle Operations — `io.go`

#### Public API Surface

| Signature | Description |
|---|-------------|
| `ConfigExists(cwd string) bool` | Checks if `.code-reducer.yaml` exists in the given directory. |
| `LoadConfig(cwd string) (*Config, error)` | Reads and parses the YAML config file from the specified directory. |
| `SaveConfig(cwd string, cfg *Config) error` | Writes (marshals to YAML) configuration back to `.code-reducer.yaml`. |

#### Internal Functions

- **`getConfigPath()`** — Resolves absolute path for the config file; referenced by exported functions.
- **`tmpFile.Close()` / `os.Remove(tmpName)` defer block** — Cleanup on any exit path inside `SaveConfig`. Failure to cleanup panics in the deferred function, which does not affect `SaveConfig`'s return value.

#### Business Logic Summary (io.go)

Manages lifecycle operations on a YAML configuration file: reading it into memory as typed structs and writing it back atomically via temp-file + rename. All I/O targets are local filesystem paths under `cwd`. No network or database interaction exists in this file.

#### Step-by-Step Algorithm (SaveConfig)

1. **Resolve path** — Build absolute config-file path from the current working directory using fixed filename constant (`ConfigFileName`).
2. **Check existence** — `os.Stat(getConfigPath(cwd))`; return whether the file is present on disk.
3. **Load (read)** — `os.ReadFile` raw bytes; unmarshal into `*Config` via YAML decoder; propagate parse errors wrapped with context.
4. **Save (write)**:
   - `yaml.Marshal(cfg)` to in-memory bytes.
   - Apply string replacements inserting blank lines before specific top-level keys (`system_prompt`, `module_synthesis_prompt`, `architecture_prompt`, `file_fact_consolidation_prompt`, `extraction_steps`, `ignore`) for consistent formatting.
   - Create a temporary file under the same directory via `os.CreateTemp`.
   - Write formatted YAML bytes to temp file, sync, close.
   - Apply permissions via `os.Chmod(tmpName, configFilePerm)`.
   - Atomically rename temp file over target path (write-through-rename pattern); readers never see a partially-written config file.

#### Error Propagation Summary (io.go)

| Function | External I/O | Error Propagation |
|---|---|---|
| `getConfigPath` | None | N/A — returns `string` |
| `ConfigExists` | `os.Stat` | Swallowed; returns `false` on any error |
| `LoadConfig` | `os.ReadFile`, YAML parse | Returned as `error`; raw OS error or wrapped YAML parse error |
| `SaveConfig` | `CreateTemp`, Write, Sync, Close, Chmod, Rename | Returned as `error`; each step wraps with descriptive prefix (`failed to create temp file: %w`, etc.) |

### Configuration Resolution — `resolve.go`

#### Public API Surface

| Signature | Description |
|---|-------------|
| `ResolveConfig(repoRoot, modelIDFlag, numCtxFlag string) (*Config, error)` | Merges CLI overrides, environment variables, YAML config, and system defaults into a fully resolved Config struct. |

##### Parameters

- **`repoRoot`** — Repository root path (string).
- **`modelIDFlag`** — CLI flag for model ID override (string).
- **`numCtxFlag`** — CLI flag for Ollama context size override (string).

#### Business Logic Summary

Solves the configuration resolution problem by producing a single, fully-resolved `*Config` struct from four sources in strict priority order: **CLI flags > environment variables > YAML config file > built-in defaults**. All field resolutions follow this identical chain. Only one real error path reaches the caller — when loading the YAML fails for reasons other than "file not found."

#### High-Level Algorithm Steps

1. **Load base config** — Read YAML configuration from disk via `LoadConfig(repoRoot)`; if absent, start with an empty default state.
2. **Pre-process ignore list** — Deduplicate any `Ignore` entries loaded from the YAML file.
3. **Set extraction steps** — Use user-provided extraction steps if present, otherwise fall back to built-in defaults (`DefaultExtractionSteps`).
4. **Resolve each field independently** — All fields follow the same priority chain: start with hardcoded default constant → override if YAML config supplied non-empty value → override if environment variable is set and valid → override if CLI flag was provided.
5. **Return** merged `*Config` struct; return error only if YAML load fails for reasons other than file absence (detected via `os.IsNotExist`).

#### External I/O Analysis (resolve.go)

| Source | Type | Interaction |
|---|---|---|
| `LoadConfig(repoRoot)` | Disk (filesystem) | Reads configuration file. Error checked via `os.IsNotExist(err)` — absent error yields empty config; otherwise wrapped and propagated. |
| `os.Getenv(...)` × 3 | OS environment | Reads process environment variables for model ID, base URL, context size overrides. Empty string treated as "not set." No errors returned by this call on standard platforms. |
| `strconv.Atoi(...)` × 2 | Pure runtime | Parses numeric strings from env vars and CLI flags. Errors (empty input, non-numeric) are swallowed — code only proceeds if `err == nil && n > 0`. |

#### Error Propagation Summary (resolve.go)

| Point | Mechanism | Behavior |
|---|---|---|
| YAML load error | Exception (error return) | Wrapped and propagated. If error is NOT `os.IsNotExist`, returns `(*Config, fmt.Errorf("failed to load configuration file: %w", err))`. Caller receives non-nil error. |
| YAML "not exist" | Swallowed | Treated as expected — replaced with empty `&Config{}`. No propagation. |
| `os.Getenv` reads × 3 | Implicit success | No errors returned; empty string == not set. Not propagated. |
| `strconv.Atoi` × 2 | Inline swallow | Only proceeds if both `err == nil && n > 0`. Otherwise defaults retained. No error return. |

## Subsystem: internal/security — Repository-Safe Path Resolution & Locking

### Responsibility

This package provides two safety primitives for a repository-aware tool: preventing directory escape during path resolution, and coordinating concurrent access through an exclusive lockfile. All operations are scoped to local filesystem state within the repository tree; no network calls or external APIs are invoked.

### Sentinel Errors — `errors.go`

#### ErrPathTraversal

Returned by external path validation logic when a resolved target lies outside the repository root boundary. Defined as an `errors.New()` sentinel with payload prefixed `"security violation:"`. No I/O or state mutation occurs in this file; the error exists solely for consumer handlers to return from their respective call sites.

#### ErrLockHeld

Returned by external lock acquisition logic when another process already holds the shared resource. Also defined as an `errors.New()` sentinel with payload prefixed `"security violation:"`. The package-level description distinguishes inter-process contention ("another process") from intra-process re-entry, which this error does not signal.

### Path Sanitization — `SafeResolve`

**Signature:** `func SafeResolve(repoRoot string, inputPath string) (string, error)`

#### Algorithm Steps

1. Convert `repoRoot` to an absolute path via `filepath.Abs`.
2. Resolve symlinks on the existing ancestor parts of the joined (`root + input`) path by walking upward until a physically existing directory is found.
3. Rebuild the full path from the resolved ancestor and any non-existent suffix components.
4. Verify the resulting path lies strictly within the symlink-resolved repo root using `filepath.Rel`; reject if the relative path starts with `".."`.

#### Error Handling (SafeResolve)

A specific `os.IsExist(err)` branch is checked after opening the lockfile with `O_EXCL` to distinguish "lock held by another process" from generic I/O errors. The existence case returns a user-facing message; other OS errors are wrapped and propagated upward. Write failures during PID capture trigger cleanup: the file is closed and removed before returning an error, preventing stale partial lockfiles.

### Exclusive Locking — `AcquireLock` & `SimpleLock.Unlock()`

#### AcquireLock

**Signature:** `func AcquireLock(repoRoot string) (*SimpleLock, error)`

1. Resolve the lockfile path via `SafeResolve` so it is guaranteed inside the repo root.
2. Open the file with `O_EXCL` (exclusive creation) to atomically detect whether another process holds the lock.
3. If acquisition fails due to existence, return an error suggesting manual cleanup of a stale lockfile.
4. Write the current PID into the newly created lockfile.

#### SimpleLock

**Fields:** All fields are lowercase/private (`closed`, `mu`, etc.). The struct embeds `sync.Mutex` for serialization of unlock calls on the same instance only; concurrent acquisition across different instances (different processes) is by design—each process gets its own lock.

**Instance-Level Mutable State:**
| Field | Type | Modified By | Synchronization |
|-------|------|-------------|-----------------|
| `closed` | `bool` | `Unlock()` sets to `true` | Internal `sync.Mutex` (`mu`) |

#### Unlock

**Signature:** `func (l *SimpleLock) Unlock() error`

1. Close the underlying file and remove the lockfile from disk; treat missing-file errors as benign so repeated calls do not fail.
2. If both close and remove fail: close error is returned only if no other error occurred; otherwise the remove error (non-`IsNotExist`) takes precedence.
3. Idempotent: checks `l.closed` first; if already released, returns nil without further I/O.

### Gitignore Maintenance — `EnsureGitignoreHasLockfile`

**Signature:** `func EnsureGitignoreHasLockfile(repoRoot string) error`

1. Read `.gitignore` (or tolerate its absence).
2. Append a commented entry for the lockfile if it is not already present.
3. Write the updated content to a temporary file, then atomically rename it over the original `.gitignore`.

#### Atomicity Guarantees

- `os.CreateTemp(dir, ".gitignore.tmp.*")` creates a temp file in the same directory as `.gitignore`, ensuring rename is atomic on the filesystem.
- `tmpFile.Sync()` forces OS-to-disk flush before close.
- `os.Rename(tmpName, gitignorePath)` atomically replaces `.gitignore` with the new content via rename; temp file cleanup runs via a deferred function always executed regardless of success or failure path.

### Concurrency Model (security)

No mutable state at package level. All constants (`LockFileName`) are compile-time immutable values. Only `SimpleLock` carries instance-local state protected by an internal mutex for the unlock path. No global counters, shared caches, or concurrently-accessed variables exist across instances.

## Subsystem: internal/engine — Architecture Documentation Pipeline

### Responsibility and Data Flow

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

### Entry Point & Pipeline Control — Runner

```go
type Mode = string // "init" | "update"
type Runner struct {
    cfg *config.Config
}

func NewRunner(cfg *config.Config) *Runner
func (r *Runner) Run(ctx context.Context, repoRoot string, mode Mode, onEvent func(Event)) error
```

`Run()` acquires a file-based lock via `security.AcquireLock(repoRoot)`; errors from this call are wrapped and returned immediately. On success it instantiates an `orchestrator`, then routes execution: init → `orch.RunInit`; update → `orch.RunUpdate`. The lock is released in the deferred path regardless of outcome.

### Orchestrator — Map-Reduce Pipeline Core

#### Types & Events

```go
type EventType = string // "Progress" | "Error"
type Event struct {
    Type   EventType
    Message string
}
```

`EventStatus` and `EventError` are constants of type `EventType`. The event callback (`onEvent func(Event)`) is invoked on each pipeline milestone.

#### Methods

```go
func (o *orchestrator) RunInit(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error
func (o *orchestrator) RunUpdate(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error
func (o *orchestrator) GenerateStandardDocs(ctx context.Context, repoRoot string, docsDir string, rootSum string, cfg *config.Config, logEvent LogEventFunc) error
```

`RunInit` and `RunUpdate` share a common setup phase: load `.gitignore`, merge ignores, load/save metadata cache, compute `StepsHash`. The update path additionally compares cached file hashes against current state to derive per-file change status.

`GenerateStandardDocs` issues two LLM calls (architecture + quickstart) using the system prompt concatenated with architecture prompts; both are wrapped in error handlers that return prefixed failure messages.

### LLM Client Layer — HTTP Transport Abstraction

#### Type & Message

```go
type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}
```

`Message` is package-private; it participates in request payloads but never appears on the public API surface.

#### Unexported Functions

All functions are unexported (lowercase). The struct `llmClient` holds `modelID`, `baseURL`, `numCtx`, and an immutable `*http.Client{Timeout: defaultHTTPTimeout}` set once in construction. Synchronization relies on the HTTP client's internal locking; no external mutex is observed.

**Data flow:**
1. System prompt prepended as first message with role `"system"`.
2. User/assistant messages appended in order.
3. Payload marshaled to JSON, POSTed to `baseURL/api/chat` (streaming disabled).
4. On 200: body read via `io.ReadAll`, unmarshaled into Ollama response schema, generated content extracted and returned.
5. Non-200: error body truncated at `maxErrorBodyBytes` bytes; status code and truncated payload returned as a single formatted string.

**Errors:** Marshal failure → `"failed to marshal request: %w"`; transport/network errors propagate raw; OK-body read failures are swallowed into `return "", err`; JSON parse errors wrapped with `"failed to parse response: %w"`. No retries exist.

### Cache Layer — Metadata Persistence

#### Types

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

#### Functions

```go
func loadMetadataCache(repoRoot, docsDir string) (*MetadataCache, error)
func saveMetadataCache(repoRoot, docsDir string, cache *MetadataCache) error
func IsInitialized(repoRoot, docsDir string) bool
func computeSHA256(repoRoot, virtualPath string) (string, error)
```

**Error handling:** `loadMetadataCache` returns empty cache + nil error when the metadata file is missing (`os.ErrNotExist`) or version mismatched; other read errors are wrapped. JSON parse errors are wrapped and returned. `saveMetadataCache` marshals with indent; no nil check on receiver exists, so a nil argument would panic at marshal time. `computeSHA256` reads file content and produces hex-encoded SHA-256 digest; read failures propagate directly. `IsInitialized` is a boolean probe that silently converts any error (including permission issues) to false.

### Chunking & Reduction — Context Window Management

#### Functions

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

### Synthesis Engine — Recursive Directory Processing

#### Internal Types & Functions

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

### Tree Building — Directory Hierarchy & Affected Detection

#### Types & Constants

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

#### Functions

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

### Utilities — Supporting Operations

#### Functions

```go
func stripOuterMarkdownFence(input string) string
func toSafeMarkdownFilename(modulePath string) string
func makeLogEvent(onEvent func(Event), eventType EventType, message string) LogEventFunc
```

**`stripOuterMarkdownFence`:** Trims whitespace; matches 3+ backtick fences with optional `markdown`/`json` language identifier; returns inner content if matched. Unrecognized formatting passes through unchanged. Failures from regex matching are silently swallowed.

**`toSafeMarkdownFilename`:** Replaces `/` with `_`, collapses empty/single-segment to `"root"`, appends `.md`. No error check on output (e.g., filesystem length limits not validated).

**`makeLogEvent`:** Returns a closure that logs events; nil callback is handled gracefully by returning an inert function.

#### Constants

All identifiers are unexported: `defaultHTTPTimeout`, `maxErrorBodyBytes`, `defaultChunkOverlap`, `minNumCtxFloor`, `contextWindowAllocRatio`, `maxCharsMultiplier`, `metadataFileName`, `agentsFileName`, `defaultDirPerm`. These define operational boundaries (10-minute HTTP timeout, 1024-byte error truncation, 800-character chunk overlap, 512-context minimum floor, 75% pre-allocation ratio) and filesystem conventions.

### Concurrency & Thread Safety Summary (engine)

No synchronization mechanisms (`sync.Mutex`, `sync.RWMutex`, atomics, channels) are present in any file within this package. All mutable state (cache maps, orchestrator struct fields) is accessed in single-threaded contexts only; thread safety cannot be verified from source alone and must be assumed caller-side responsible for serialization if used across goroutines. The repository-level lock in `Runner.Run` provides coarse-grained concurrency control but operates outside the engine's own fields.

## Subsystem: internal/tools — Repository-Aware File & Git Operations

### Responsibility and Data Flow

This module provides safe, repository-aware file operations (read/write/discover) that respect `.gitignore` rules, plus utilities for executing `git` commands against a repo root. All functions operate on an absolute filesystem rooted at the caller-supplied `repoRoot`. No mutable global state is held; every call resolves paths locally and returns results directly.

Data flow: caller supplies `repoRoot` → module validates or constructs absolute paths under it → I/O occurs within that boundary → results (bytes, strings, file lists) are returned to caller with errors propagated (or swallowed per documented convention). No network, database, or API calls exist in this module; all external interactions are local filesystem and OS process invocations.

### File Operations — `file_tools.go`

#### Safe Path Resolution

All read/write/discover functions begin by resolving a virtual/relative path into an absolute filesystem path confined to `repoRoot`. The resolution step is delegated to `security.SafeResolve`, which performs the traversal-safe check before any I/O. Errors from this step are returned directly (or wrapped) and never panic/recovered within this module.

#### Reading Files — `ReadFileSafely`

```go
func ReadFileSafely(repoRoot, virtualPath string) ([]byte, error)
```

**Parameters:**
- `repoRoot` (`string`) — repository root path.
- `virtualPath` (`string`) — logical file path inside the repo.

**Returns:**
- `([]byte)` — file content bytes.
- `error`

**Flow:** Resolves `repoRoot/virtualPath` via safe resolution → reads the absolute target with `os.ReadFile` → wraps any read error as `"failed to read file content: %w"`. No panic/recovery path exists; all errors propagate to caller.

#### Writing Files — `WriteFileSafely`

```go
func WriteFileSafely(repoRoot, virtualPath string, content []byte) error
```

**Parameters:**
- `repoRoot` (`string`) — repository root path.
- `virtualPath` (`string`) — logical file path inside the repo.
- `content` (`[]byte`) — bytes to write.

**Returns:**
- `error`

**Flow:** Resolves target path safely → creates parent directories via `os.MkdirAll` (errors wrapped individually with descriptive prefix) → writes content into a temporary file in the same directory using `os.CreateTemp`, suffix `.tmp.*` → syncs and closes → atomically renames temp over final target → applies `os.Chmod`. Each step's error is wrapped individually before propagating to caller. No partial-write corruption path: atomic rename ensures either pre-rename state or post-rename state only.

#### Loading Gitignore — `LoadGitignore`

```go
func LoadGitignore(repoRoot string) ([]string, error)
```

**Parameters:**
- `repoRoot` (`string`) — repository root path.

**Returns:**
- `([]string)` — active ignore patterns (empty slice if no `.gitignore` exists).
- `error`

**Flow:** Opens `<repoRoot>/.gitignore` via `os.Open` with deferred close (file is always closed regardless of error) → if the file does not exist (`os.IsNotExist`), returns `(nil, nil)` → any other open error propagates after deferred-close semantics. The returned slice contains non-comment, non-empty lines parsed from the file.

#### Ignoring Files — `ShouldIgnoreFile`

```go
func ShouldIgnoreFile(relPath string, gitIgnore *github.com/sabhiram/go-gitignore.GitIgnore) bool
```

**Parameters:**
- `relPath` (`string`) — relative file path.
- `gitIgnore` (`*github.com/sabhiram/go-gitignore.GitIgnore`) — compiled Git ignore matcher (may be nil).

**Returns:**
- `bool`

**Flow:** Checks whether `relPath` matches any loaded gitignore pattern via the provided compiled matcher. Additionally filters dot-prefixed directory components and `.egg-info` suffixes regardless of gitignore content. Returns true if matched; false otherwise. A nil `gitIgnore` produces a default-matching behavior (not explicitly documented beyond the nil allowance).

#### Discovering Code Files — `DiscoverCodeFiles`

```go
func DiscoverCodeFiles(repoRoot string, ignores []string) ([]string, error)
```

**Parameters:**
- `repoRoot` (`string`) — repository root path.
- `ignores` (`[]string`) — list of ignore strings to compile into a GitIgnore matcher.

**Returns:**
- `([]string)` — discovered file paths (relative, slash