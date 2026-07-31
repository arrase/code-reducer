# Architecture Overview — Code-Reducer

## System Boundaries

Code-Reducer is an interactive LLM-driven static analysis pipeline that generates hierarchical markdown documentation for software repositories. The entire system operates against local filesystems and a remote Ollama-compatible HTTP endpoint; no internal module performs network I/O directly, and all external dependencies are funneled through interface abstractions.

The entry point (`main.go`) delegates to `cmd.RootCmd.Execute()`, which routes execution through a lifecycle of three engine modes—`init`, `update`, and an additional mode resolved by `executeCommand`. All processing occurs in the `internal/` packages; no domain logic is implemented at the top level.

### Module Interaction Topology

```
User Terminal ──► RootCmd (cobra) ◄──► executeCommand(Mode)
                                    ├──► checkAndRunSetup(repoRoot)
                                    │    Check git repo, config existence, TTY check
                                    │    If no config + stdin is TTY → RunSetupFlow
                                    ├──► checkInitStatus(repoRoot, docsDir, Mode)
                                    │    Validates init marker presence; enforces:
                                    │      - ModeInit fails if already initialized
                                    │      - ModeUpdate fails if not yet initialized
                                    ├──► runEngine(repoRoot, cfg, Mode)
                                    │    Registers SIGINT/SIGTERM handlers
                                    │    Instantiates runner
                                    │    Streams events (status → stdout, error → stderr)
                                    └──► executeCommand returns engine.Error or nil
```

The static analysis pipeline itself follows: **repository discovery** (`internal/tools`) → **path safety validation** (`internal/security`) → **configuration resolution** (`internal/config`) → **orchestration and synthesis** (`internal/engine`). All external dependencies funnel through the `llmCaller` interface defined in `internal/config`, enabling transport-level abstraction without coupling to a specific LLM provider.

---

## Configuration Module (`internal/config`)

### Responsibility

Owns all runtime configuration for the pipeline: defines the typed schema capturing LLM prompts, extraction step definitions, ignore lists, and Ollama client parameters; persists state with atomic write semantics so partially-written state never replaces valid config on disk; resolves the final `*Config` value by merging four sources in defined priority order — YAML file → environment variables → CLI flags → hardcoded defaults.

### Configuration Schema Types

```go
type ExtractionStep struct {
    Name   string // yaml:"name"
    Prompt string // yaml:"prompt"
}

type Config struct {
    ModelID                     string           // yaml:"model_id"
    OllamaBaseURL               string           // yaml:"ollama_base_url"
    OllamaNumCtx                int              // yaml:"ollama_num_ctx"
    DocsDir                     string           // yaml:"docs_dir"
    SystemPrompt                string           // yaml:"system_prompt"
    ModuleSynthesisPrompt       string           // yaml:"module_synthesis_prompt"
    ArchitecturePrompt          string           // yaml:"architecture_prompt"
    FileFactConsolidationPrompt string           // yaml:"file_fact_consolidation_prompt"
    ExtractionSteps             []ExtractionStep // yaml:"extraction_steps"
    Ignore                      []string         // yaml:"ignore"
}
```

`Config` is the single struct passed between `SaveConfig`, `LoadConfig`, and `ResolveConfig`. All three functions operate on it by value or pointer — `SaveConfig` and `LoadConfig` accept only a directory path, while `ResolveConfig` takes directory + two flag arguments. No methods are defined on `Config`; all mutation happens in external packages via struct initialization.

### Constants and Default Extraction Steps

| Constant | Value | Purpose |
|---|---|---|
| `CodeReducerModelIDEnvKey` | `"CODE_REDUCER_MODEL_ID"` | Env key for model ID override |
| `OllamaBaseURLEnvKey` | `"OLLAMA_BASE_URL"` | Env key for Ollama URL override |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | Env key for context size override |
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | Default local Ollama endpoint |
| `OllamaDefaultModelID` | `"ornith:9b"` | Default LLM model ID |
| `OllamaDefaultNumCtx` | `8192` | Default context window size |
| `DefaultDocsDir` | `"wiki"` | Default documentation folder name |
| `ConfigFileName` | `".code-reducer.yaml"` | Persistent config filename |

`DefaultExtractionSteps` is a package-level `var` of type `[]ExtractionStep`, pre-populated with four entries:

| Index | Name | Purpose |
|---|---|---|
| 0 | `API_SIGNATURES` | Extracts public types, functions, methods and their signatures without explaining internal logic. |
| 1 | `BUSINESS_LOGIC` | Explains the primary domain problem solved by the code and lists high-level algorithm steps, ignoring implementation syntax. |
| 2 | `STATE_AND_CONCURRENCY` | Identifies mutable global/state variables and synchronization mechanisms; outputs `"No mutable state"` if entirely stateless. |
| 3 | `ERRORS_AND_SIDE_EFFECTS` | Details interactions with external systems (network, disk, databases) and how errors propagate or are swallowed. |

The slice is declared without mutex protection — no concurrency primitives exist in this file, and the variable is not modified after initialization.

### File I/O Operations (`io.go`)

#### ConfigExists

```go
func ConfigExists(cwd string) bool
```

Performs `os.Stat()` on the resolved config path (computed by `getConfigPath`); returns `true` only when `.code-reducer.yaml` exists at that location. All OS-level failures — permission denied, inaccessible paths, I/O errors — are swallowed and reported as `false`. The caller has no way to distinguish absence from error.

#### LoadConfig

```go
func LoadConfig(cwd string) (*Config, error)
```

Reads the config file into memory via `os.ReadFile`, unmarshals it with `yaml.Unmarshal` (from `gopkg.in/yaml.v3`), and populates a fresh `*Config`. On any failure — missing file, parse error, or I/O fault — returns `(nil, err)`. Parse errors are wrapped with the prefix `"failed to parse yaml config:"`.

#### SaveConfig

```go
func SaveConfig(cwd string, cfg *Config) error
```

Implements atomic write semantics:

1. Marshal `cfg` via `yaml.Marshal`.
2. Apply formatting normalization — insert double newlines before specific prompt keys (`system_prompt`, `module_synthesis_prompt`, etc.) for consistent display.
3. Create a temp file in the same directory via `os.CreateTemp`.
4. Write content, sync to disk, close the descriptor, set permissions (`fileMode`), then rename the temp over the target path via `os.Rename`.

Each step's error is wrapped with a descriptive prefix (`"failed to create temp file:"`, `"failed to write config to temp file:"`, etc.). The deferred function closes the temp file and removes it; no panic recovery exists — any panic during execution or cleanup propagates unhandled.

#### Non-Exported Helpers

| Function | Purpose |
|---|---|
| `getConfigPath(cwd string) string` | Builds absolute filesystem path from current working directory + configured filename. |
| `formatYAML(data []byte) string` | Applies double-newline normalization to prompt keys for consistent output formatting. |

Both are package-private; only referenced within the `config` package's internal implementation.

### Multi-Source Resolution (`resolve.go`)

#### ResolveConfig

```go
func ResolveConfig(repoRoot string, modelIDFlag string, numCtxFlag string) (*Config, error)
```

Produces a single fully-resolved `*Config` by merging four sources in priority order: CLI flags > environment variables > YAML config file > hardcoded defaults. The returned struct is freshly allocated on each invocation — no shared instance exists within this function's scope.

**Algorithm:**

1. **Load YAML config.** Call `LoadConfig(repoRoot)`. If it returns `(nil, os.ErrNotExist)` — accept absence as valid and substitute an empty `Config{}`. Any other error (parse failure, I/O fault) is wrapped with `"failed to load configuration file:"` and returned.
2. **Resolve extraction steps.** If the loaded YAML omits `ExtractionSteps`, substitute the built-in default set (`DefaultExtractionSteps`). Otherwise use the YAML-provided list verbatim.
3. **Deduplicate ignore list.** Strip duplicate entries from the YAML's `Ignore` field.
4. **Per-field resolution (priority chain).** For each configurable field: start with the hardcoded system default, override if the YAML config provides a non-empty value, override further if an environment variable is set and valid (`os.Getenv` for `CodeReducerModelIDEnvKey`, `OllamaBaseURLEnvKey`, `OllamaNumCtxEnvKey`). Override finally by the CLI flag argument passed to this function.
5. **Validate numeric inputs.** For `OllamaNumCtx`: reject values that fail `strconv.Atoi` parsing or are ≤ 0, returning an error with the offending key name and raw value embedded in the message (`"invalid value for %s: %s"`). If validation fails, return `(nil, err)` — no partial config is emitted.
6. **Return resolved config.** On successful resolution, emit a populated `*Config` struct with all fields merged; on any failure path, return `(nil, error)`.

**Error model:** All errors use Go's standard wrapping convention (`%w`) where applicable, preserving traceability for callers using `errors.Is`. No network I/O occurs within this file. No disk writes occur — only the external `LoadConfig` reads from disk.

### State and Concurrency Analysis

No mutable state exists across the module's public surface:

- `DefaultExtractionSteps` is a package-level variable but is not modified after initialization; no synchronization mechanism protects it, though this is only observable within `config.go`.
- All function-local variables (`seen`, `result`, `cfg`, `resolved`) are scoped to their respective functions.
- No locks, mutexes, atomic types, async/await patterns, or channel-based coordination are used anywhere in the package.

---

## Engine Module (`internal/engine`)

### Responsibility and Data Flow

The `internal/engine` package implements an end-to-end pipeline that **discovers source files in a repository, classifies their change status against cached state, recursively synthesizes hierarchical summaries via LLM calls, and persists the results as markdown documentation**. The pipeline operates in two modes:

- **Init** (`ModeInit`) — full regeneration of all documentation.
- **Update** (`mode ModeUpdate`) — incremental reprocessing limited to directories whose descendants contain changed files.

The data flow proceeds from `Runner.Run()` → orchestrator selection → tree construction → per-node synthesis (bottom-up) → global doc generation → cache persistence. All external dependencies funnel through the `llmCaller` interface, enabling transport-level abstraction without coupling the engine layer to a specific LLM provider.

### Types and Shared State

#### Core Structs

| Type | Scope | Purpose |
|------|-------|---------|
| `orchestrator` | package-private | Orchestrates both init and update pipelines; embeds an `llmCaller`. |
| `Runner` | public (`internal/engine`) | Entry point for pipeline execution. Holds a pointer to `config.Config`. |
| `Message` (exported) | public | LLM protocol message envelope: `{Role, Content}`. Used by the Ollama client. |
| `DirNode`, `FileChange`, `ChangeStatus` | package-private / tree.go | Directory tree node with path, file list, and child map; file-change record for change detection. |
| `MetadataCache`, `FileCacheEntry` (exported) | public (`internal/engine`) — from `cache.go` | Top-level cache state holding per-file extraction facts and per-module directory summaries. |

#### Supporting Types Referenced Externally

- `config.Config`, `llmCaller`, `pipelineContext` — declared elsewhere; referenced by orchestrator, runner, and synthesize modules.
- `security.SafeResolve` — path resolution helper used throughout for safe absolute-path construction.
- `tools.ReadFileSafely`, `tools.WriteFileSafely`, `tools.DiscoverCodeFiles`, `tools.LoadGitignore` — file-system helpers from the `tools` package.

### LLM Client Layer (`client.go`)

The engine communicates with an external language model through a remote HTTP endpoint, specifically targeting Ollama's `api/chat` protocol. Interaction is abstracted behind the `llmCaller` interface so the engine layer can depend on it without tying to a specific transport implementation.

**Request Construction:**
1. Prepend any system prompt to the user-provided message list.
2. Serialize into Ollama's expected JSON schema (model ID, messages, stream flag, optional format/options).

**Execution and Response Parsing:**
3. Execute synchronous HTTP POST to `baseURL/api/chat` using a context-aware client with a fixed timeout; no retries are attempted.
4. On 200 OK, deserialize the JSON into an Ollama-style envelope and return only the model's reply content as the result string.

**Error Propagation:**
- **Non-OK status**: The body is read up to `maxErrorBodyBytes` (constant defined elsewhere) and the content is embedded in `"ollama api error: status {code}, response: {text}"`. If reading fails mid-stream, only the status code is reported; the underlying read failure is lost.
- **Successful status + parse error**: Response body is fully read into memory, then unmarshaled. JSON mismatch yields a wrapped error `"failed to parse response: %w"`.
- **Transport failure** (network down, DNS fail, timeout): Returned directly without retry logic; callers must handle their own backoff upstream.

### Metadata Cache Layer (`cache.go`)

The engine persists per-file extraction results ("facts") along with file integrity hashes, enabling change detection and incremental reprocessing between pipeline runs. The cache is stored as a versioned JSON file at `<docsDir>/<metadataFileName>` relative to `repoRoot`.

**Initialization and Loading:**
1. **Initialize empty cache** — creates versioned metadata container with maps ready to grow (files, modules).
2. **Load existing cache from disk** — reads the serialized metadata file; if missing (`os.ErrNotExist`), returns a fresh empty cache. Incompatible versions are silently recovered by returning a clean slate rather than failing.
3. **Nil safety**: Maps initialized to nil on load are replaced with fresh empty maps before use.

**Persistence and Integrity Tracking:**
4. **Persist cache state back to disk** — serializes the current cache (versioned, indented) and writes it atomically via `tools.WriteFileSafely`.
5. **Track extraction step history** — marshals the list of extraction steps into JSON and hashes them; this hash is stored as `steps_hash` in the top-level cache, enabling detection when the processing pipeline itself changed between runs.

### Tree Construction and Change Detection (`tree.go`)

This code provides filesystem change propagation analysis — it builds an in-memory directory tree from file paths, then determines which directories are affected by a set of file changes (additions, modifications, deletions) by traversing the tree and marking parent directories whose children have changed or been removed.

**Algorithm Steps:**
1. **Build Directory Tree**: Parse each input file path into hierarchical components (`/`-separated), recursively constructing `DirNode` objects that represent folders with their files and child subdirectories. Files at the root level go directly under the tree root node; nested paths create intermediate directory nodes.
2. **Initialize Affected Set**: Start with an empty set of affected directories. For each file change, if its status is "Deleted", immediately mark its parent directory as affected.
3. **Tree Traversal to Detect Changes**: Walk every `DirNode` in the tree: if any file under that node appears in a changed-file map, mark the node's path as affected; check whether a corresponding markdown module path exists at disk (relative to `docs/modules/`). If it does not exist, mark the directory as affected — implying the module is being added or removed. If the cached metadata for that path is empty, treat the directory as newly created and mark it affected. Recursively process all child directories.
4. **Propagate Affected Status**: After initial detection, walk the tree a second time: if any descendant is marked affected, propagate that status upward to parent nodes so ancestors of changed subtrees are also flagged.

### Hierarchical Synthesis Pipeline (`synthesize.go`)

This code implements an automated codebase summarization engine that recursively analyzes source files and directories, extracting structured facts about each file's purpose/behavior via LLM calls, then synthesizing hierarchical summaries upward through a directory tree — ultimately producing per-directory documentation artifacts.

**Algorithm Steps:**
1. **Tree Traversal (Bottom-Up)**: Recursively process a directory node by first visiting all child directories (sorted for determinism), then processing files within the current directory.
2. **File Fact Extraction**: check cache and precomputed hashes before reading file content; read raw file bytes, compute SHA-256 hash if not already cached; split file content into overlapping chunks (size dynamically scaled from LLM context window); for each chunk, invoke an LLM with a system prompt + step-specific user prompt to extract facts; consolidate all chunk results through a reduction step per extraction step. Repeat across multiple sequential extraction steps until all perspectives are captured.
3. **Component Assembly**: Combine extracted file summaries and synthesized child-directory summaries into a unified list of components for the current directory node.
4. **Directory Synthesis**: Apply multi-step LLM-based chunked reduction on the assembled components to produce a single consolidated summary for the entire directory.
5. **Persistence and Caching**: Store computed hashes, file facts, and directory summaries in metadata caches; write final directory summaries to disk as markdown documentation under `cfg.DocsDir/modules/<safe-filename>`.

**Mutable State:**
- `pipelineContext.cache.Files` — Modified in `extractFileFacts`: `{SHA256: fileHash, Facts: facts}`.
- `pipelineContext.cache.Modules` — Modified in `synthesizeNode`: cleared when no components exist, set to final summary after synthesis completes.
- `pipelineContext.affectedDirs` — Read for membership checks during per-node processing; not modified within this file.

### Chunking and Reduction Algorithms (`chunking.go`)

This module implements a **map-reduce tree reduction** algorithm for LLM-based code synthesis. It takes multiple text items (code facts, file descriptions, architecture notes), batches them within context-window limits, sends each batch to an LLM, and recursively reduces the outputs until a single consolidated result remains — stopping when further reduction would not shrink the output significantly (loop prevention).

**Algorithm Steps:**
1. **Input Validation**: Return empty string if no items provided; return item as-is for single-item inputs in `reduceFileFacts`.
2. **Pre-Expansion**: Any individual item exceeding the character limit is split into smaller overlapping chunks via `chunkTextWithOverlap`, then all resulting pieces are pooled back into a flat list.
3. **Binning by Size**: Items (and pre-expanded chunks) are grouped into batches such that no batch exceeds `maxChars` runes, with overflow items starting a new batch.
4. **Recursive Reduction**: Each batch is sent to the LLM via `reduceFn`. The function calls itself recursively on each batch's result until only one item remains in `intermediate`.
5. **Loop Prevention Check**: Before recursing again, total output runes are compared against 95% of total input runes. If output ≥ 95% of input (information is not being condensed), the algorithm stops and returns all intermediate results concatenated with double newlines, preserving information without exceeding context windows.
6. **LLM Integration**: The LLM caller receives a system prompt (e.g., "Synthesize architecture for {nodePath}" or "Consolidate facts for {filePath}") plus user content formed by joining batch items with double newlines. Markdown fences are stripped from the response before return via `stripOuterMarkdownFence`.

### Orchestrator Pipeline (`orchestrator.go`)

This code implements a Map-Reduce pipeline that automatically generates and maintains documentation for software repositories by recursively analyzing source code structure with an LLM, then writing the synthesized results back as markdown files (architecture overview, quickstart guide, per-module summaries).

**Algorithm Steps:**
1. **Code Discovery**: Locate all code files in the repository root, filtering out documentation directories and patterns from `.gitignore` + user-configured ignore lists.
2. **Hash-Based Change Detection**: Compute SHA256 hashes for each discovered file and compare against a cached state to classify changes as Added, Modified, or Deleted.
3. **Tree Construction**: Organize the code files into a hierarchical directory tree structure (`DirNode` with children).
4. **Affected Directory Determination**: *Init mode*: Mark all directories as affected (full regeneration). *Update mode*: Identify only those directories whose descendants contain changed files, propagating "affected" status upward through the tree.
5. **Hierarchical Tree-Merging**: Recursively synthesize each node starting from leaves and working toward the root. Each synthesis call to the LLM produces a summary of that directory's code, which becomes input for parent-level synthesis (reducing as you move up).
6. **Standard Documentation Generation**: After the tree is fully synthesized, generate two global documents: `architecture.md` — high-level system overview based on the root synthesis output; `quickstart.md` — onboarding and usage guide derived from the same root summary.
7. **Agent Guidelines Update**: Write or append an AI Agent Guidelines file that references all generated documentation paths, ensuring future agent interactions are informed by existing docs.
8. **Cache Maintenance**: *Pruning*: Remove cache entries for directories no longer present in the code tree and delete their corresponding markdown files on disk. *Invalidation*: If extraction steps configuration changes, reset the file cache to force full regeneration on next run.

**External I/O and Error Patterns:**
- **LLM API calls**: Delegates to `o.client.CallLLM(ctx, sysPrompt, messages, false)` with a system prompt constructed from `cfg.SystemPrompt + cfg.ArchitecturePrompt`. Called twice per run — once for `architecture.md`, once for `quickstart.md` (in `GenerateStandardDocs`). The pipeline's hierarchical synthesis (`synthesizeNode`) also calls the LLM internally.
- **Disk writes**: Architecture and quickstart docs are written via `tools.WriteFileSafely` after stripping outer markdown fences; modules directory is created with `os.MkdirAll` using `defaultDirPerm`. Agent file is either overwritten or appended depending on content presence. Stale module files are removed via `security.SafeResolve` + `os.Remove`.
- **Swallowed errors**: Missing `.gitignore` (if not present) returns empty list without error; hash computation failures per file log warnings and continue processing; cache save failure in teardown logs a warning but never returns an error, meaning post-run state may be inconsistent if the cache write fails.

### Runner Orchestration (`runner.go`)

This code manages and orchestrates an automated documentation generation pipeline for software projects, supporting both initial documentation creation (init mode) and incremental updates to existing documentation (update mode). It serves as the main entry point that coordinates AI-powered document processing across a repository.

**Algorithm Steps:**
1. Ensure project lockfile is added to `.gitignore` for version control safety.
2. Acquire an exclusive repository lock to prevent concurrent documentation operations.
3. Initialize LLM client and orchestrator based on provided configuration (model, base URL, context settings).
4. Execute the appropriate pipeline mode: either run full initialization or incremental update using the configured AI model.

**Synchronization and Locking:**
- A repository-level lock is acquired via `security.AcquireLock(repoRoot)` and released via `defer lock.Unlock()`. This protects repository state during pipeline execution. The exact lock type (file-based, in-memory) depends on implementation in `internal/security`.
- All other struct fields are assigned once in `NewRunner()` and never modified after that; no mutable shared state exists within this file's boundary.

### Utility Helpers (`utils.go`)

This file provides lightweight utility helpers for the engine module: generating safe markdown filenames from directory paths, and creating adapter-style log event callbacks that support optional listeners.

**Domain Rules:**
- Empty/`.` module paths map to `"README.md"` by default via `toSafeMarkdownFilename`.
- Unknown or invalid callbacks are silently swallowed — no panic on nil listeners in `makeLogEvent`.

### Constants and Configuration (`constants.go`)

This file defines operational configuration constants for an AI inference/agent engine runtime. It establishes fixed parameters that govern timeout behavior, error handling limits, context window management, and filesystem conventions used during agent execution.

| Constant | Purpose |
|---|---|
| `defaultHTTPTimeout` (10 minutes) | Maximum duration for remote API calls before abort. |
| `maxErrorBodyBytes` (1 KB) | Limits how much error body data can be captured from failed requests. |
| `defaultChunkOverlap` (800 tokens) | Overlap between consecutive text chunks during streaming or processing. |
| `minNumCtxFloor` (512) | Minimum threshold for context length calculations. |
| `contextWindowAllocRatio` (0.75) | Reserves 75% of available space for primary content, remainder for metadata/overhead. |
| `maxCharsMultiplier` (3x) | Multiplies base character limit to determine maximum allowed output length. |
| `metadataFileName`, `agentsFileName`, `defaultDirPerm` | Filesystem conventions for state persistence during agent runs. |

### Test Coverage (`chunking_test.go`, `tree_test.go`)

**Chunking Tests — Inferred Signatures:**
- `chunkTextWithOverlap(text string, maxRunes int, overlapRunes int)` → `([][]string, error)`: Splits arbitrary text into fixed-size chunks with configurable overlap. Validates that `maxRunes > 0` and `overlapRunes < maxRunes`. Short texts return single-element slices; exact-fit texts also return single elements.
- `reduceItems(ctx context.Context, items []string, maxChars int, reduceFn func([]string) (string, error))` → `(string, error)`: Reduces an ordered slice of items into a single string while respecting a character limit. All input items must still appear somewhere in the final output after reduction.

**Tree Tests — Inferred Signatures:**
- `buildTree(files []string)` → `*DirNode`: Parses flat file paths into hierarchical directory structures; files at root level go directly under tree root node; nested paths create intermediate directory nodes.
- `determineAffected(tree *DirNode, tempDir string, docs string, cache Cache, changes []FileChange)` → `map[string]bool`: Walks the tree while tracking file changes; marks modules whose source files changed as affected; unrelated branches remain unaffected.
- `propagateAffected(tree *DirNode, affected map[string]bool)` → `map[string]bool`: Propagates affected status to parent directories after initial detection — if any descendant is marked affected, ancestors are also flagged (transitive propagation).

**Test Observations:** All test functions invoke production methods without checking return values or handling errors; any errors they produce are swallowed. Assertions use `t.Errorf` and `assert.True` patterns with no caller chain visible in tests alone.

---

## Security Module (`internal/security`)

### Responsibility

This module provides two security primitives scoped to a repository root:

1. **Path traversal prevention** via `SafeResolve`, which validates that user-supplied paths resolve within the repository boundary, resolving symlinks on existing ancestor parts before comparison.
2. **Cross-process lock management** via `SimpleLock`/`AcquireLock`/`Unlock`, which ensures only one process holds exclusive access to a protected resource at any given time through an atomic file-based lock mechanism.

Both primitives operate against the local filesystem; no network, database, or remote API interactions occur within production code. All errors are returned to callers and propagated via Go's `error` interface; panics are not raised by this module.

### Data Flow Overview

1. Callers invoke `SafeResolve(repoRoot, inputPath)` with a candidate path string and an anchor directory; the function returns either a resolved absolute path confined within `repoRoot` or an error (typically `ErrPathTraversal`).
2. Callers invoke `AcquireLock(repoRoot)` to obtain a lock handle; if contention is detected — either via OS-level atomic failure of `O_EXCL` or presence of a stale lockfile — the function returns an error wrapping the sentinel `ErrLockHeld`.
3. When release is needed, callers call `Unlock()` on the returned `*SimpleLock`; this method is idempotent and thread-safe with respect to itself, closing the file descriptor and removing the lockfile from disk under a mutex guard.

### Error Definitions — `errors.go`

The module declares two sentinel error variables used as context markers during error propagation:

| Sentinel | Declaration Scope | Usage Context |
|---|---|---|
| `ErrPathTraversal` | package-level in `internal/security/errors.go` | Returned by `SafeResolve` when the resolved path escapes outside `repoRoot`. The original traversal signal is swallowed and replaced with a formatted message carrying `inputPath` as context. |
| `ErrLockHeld` | package-level in `internal/security/errors.go` | Returned by `AcquireLock` when another process holds the lock or a stale lockfile persists; returned via `%w` wrapping of the underlying OS error with descriptive context. |

No functions, imports beyond `errors`, or runtime operations exist in this file. Error propagation from these sentinels (e.g., further wrapping via `fmt.Errorf`) occurs elsewhere, not here.

### Path Resolution — `SafeResolve`

#### Signature

```go
func SafeResolve(repoRoot, inputPath string) (string, error)
```

#### Responsibility

Resolves a candidate path against an anchor root while preventing escape through symlinks and directory traversal. The function returns the cleaned absolute path if it remains strictly inside the resolved repository boundary; otherwise it returns `ErrPathTraversal`.

#### Algorithm Steps

1. Compute the absolute root directory from `repoRoot` via `filepath.Abs`, wrapping any resulting error with `%w`.
2. Resolve symlinks on the absolute root using `filepath.EvalSymlinks(absRoot)`, again wrapping errors with `%w`.
3. Walk up from the joined path (`absRoot + inputPath`) until a physically existing ancestor is found via repeated `os.Lstat(current)` calls; each Lstat failure that is not "not exist" is wrapped and returned immediately.
4. Resolve symlinks on the first physically existing ancestor via `filepath.EvalSymlinks(current)`, wrapping errors with `%w`.
5. Reconstruct the full target path from the resolved ancestor plus all previously-skipped components.
6. Verify that the reconstructed path remains inside the resolved root; reject if it escapes by returning a wrapped error using `ErrPathTraversal` with `inputPath` as context.

### Lock Acquisition and Release — `SimpleLock`

#### Types

| Field | Type | Mutability | Notes |
|---|---|---|---|
| `lockPath` | `string` | Modified once during acquisition, then read-only thereafter. Not mutated by any other method. | Absolute path of the lockfile within the repository root. |
| `file` | `*os.File` | Modified once during acquisition. Closed during unlock and removed from disk afterward. | File descriptor holding the exclusive lock; opened with `O_WRONLY\|O_CREATE\|O_EXCL`. |
| `mu` | `sync.Mutex` | Read via lock/unlock primitives only. Field itself is never reassigned after struct initialization. | Protects the close-and-remove sequence in `Unlock()`, making unlock atomic with respect to itself. |
| `closed` | `bool` | Set to `true` during unlock; read inside unlock for idempotency check (`if l.closed { return nil }`). | Tracks release state so subsequent calls return immediately without further I/O. |

#### Methods

##### AcquireLock(repoRoot string) (*SimpleLock, error)

**Responsibility**: Acquires an exclusive file-based lock within the provided repository root. Uses O_EXCL to ensure atomicity; failure implies another process holds the lock or a stale lockfile exists.

**Algorithm Steps:**
1. Calls `SafeResolve(repoRoot, LockFileName)` to obtain the canonical lock file path inside the repo root. If this fails, the error propagates directly without wrapping.
2. Opens the lockfile with `os.OpenFile(lockPath, O_WRONLY\|O_CREATE\|O_EXCL, 0644)`. The OS-level atomicity of O_EXCL means failure indicates another writer holds the file or a stale lock persists; this is treated as an error condition requiring manual cleanup by the caller.
3. Writes the current process PID into the lockfile via `f.Write([]byte(fmt.Sprintf("%d\n", os.Getpid())))` for identification/inspection purposes. If this write fails, the method closes the file and removes it before returning a wrapped error.

**Error Propagation:**
- Direct propagation when `SafeResolve` fails (no wrapping).
- Wraps with `%w` using `ErrLockHeld` plus context string describing stale lockfile when the OS reports `os.IsExist`.
- On write failure after successful OpenFile: closes file and removes it, then returns a wrapped error that swallows close/remove errors into a single formatted message — only the original write error is surfaced.

##### Unlock() error (method on *SimpleLock)

**Responsibility**: Releases the lock by closing the file descriptor and removing the lockfile from disk. Idempotent and thread-safe with respect to itself.

**Algorithm Steps:**
1. Acquires `l.mu.Lock()` at entry; releases via `defer mu.Unlock()`.
2. Checks `if l.closed { return nil }`; if already closed, returns immediately without further I/O.
3. Closes the file descriptor (`l.file.Close()`). If this fails, the error is swallowed when reporting removal errors — only surfaces the remove error if it subsequently fails.
4. Attempts to remove the lockfile from disk via `os.Remove(l.lockPath)`.

**Thread Safety**: The struct owns its own mutex; concurrent calls to `Unlock` on the same instance serialize through `mu.Lock()/defer mu.Unlock()`, making the operation atomic with respect to itself. Concurrent acquisition of different instances each creates independent `SimpleLock` objects with separate mutexes — no contention between instances is modeled or guaranteed by this code.

### Test Coverage — `security_test.go`

**Functions Under Test:**
- `SafeResolve(repoRoot, inputPath)` returns an error to its caller; tests verify both success and failure paths via a `wantErr` flag comparison (`t.Errorf(...)`).
- `AcquireLock(repoRoot)` returns `(lock, err)`. A second concurrent call failing with `AcquireLock() should have failed when lock is already held` confirms the production code signals contention via error return.

**Test Fixture Operations:** All operations below target the local filesystem via Go's standard library:
- Directory creation for test fixtures via `os.MkdirAll()` — propagated to test framework via `t.Fatal(err)` — terminates immediately, no recovery.
- File writing for traversal tests via `os.WriteFile()` — same as above; propagated to `t.Fatal(err)`.
- Symlink creation (conditional) via `os.Symlink()` — swallowed and replaced with `t.Skip("Symlinks not supported on this OS/filesystem")`; test continues if operation fails.

### Concurrency and Synchronization Summary

- No package-level variables are declared anywhere in this module; all mutable state is encapsulated within individual `SimpleLock` instances.
- `Unlock()` self-synchronizes via `mu.Lock()/defer mu.Unlock()`, making it safe for concurrent calls on the same instance.
- Concurrent calls to `AcquireLock` on different instances each create their own independent `SimpleLock` with separate mutexes — no contention between instances is modeled or guaranteed by this code.
- The file-level lock (the `.code-reducer.lock` file itself) provides OS-level mutual exclusion, which complements the in-memory `mu`.
- No locks/mutexes are used within the test file itself; each `Test*` function runs independently in a single goroutine.

---

## Tools Module (`internal/tools`)

### Responsibility and Data Flow

The `internal/tools` package provides two complementary capabilities for analyzing a code repository's structure: (1) safe file I/O operations with TOCTOU protection and atomic writes, and (2) Git command abstraction for process execution. All functions operate against a local filesystem only; no network calls, database interactions, or external API invocations occur.

The module's entry point is the `repoRoot` string passed to every public function. Data flows from caller → `internal/tools` package functions → OS subsystems (POSIX file system for I/O operations, git binary via subprocess execution). No mutable state exists within any function scope; all operations are stateless and idempotent with respect to repository content.

### Atomic Write — `WriteFileAtomic`

```go
func WriteFileAtomic(targetPath string, data []byte, perm os.FileMode) error
```

Writes binary data to `targetPath` using a temp-file + rename pattern that prevents partial or corrupt state if interrupted. The directory is created with mode `0755`. Returns an error on any failure; returns `nil` only after the final rename succeeds.

**Sequence:**
1. Create temporary file in same directory as target (ensures atomic rename semantics)
2. Write data to temp file, sync before close for crash safety
3. Close and flush via defer block with cleanup gated by success flag
4. Apply `perm` mode via `os.Chmod`
5. Atomic rename from temp path into final location

Errors at any step are wrapped; deferred cleanup runs on failure paths to prevent partial temp file leakage. No panic/recover anywhere in this function.

### Safe Read — `ReadFileSafely`

```go
func ReadFileSafely(repoRoot string, virtualPath string) ([]byte, error)
```

Resolves the virtual path against `repoRoot`, then reads via `os.ReadFile`. Wraps any read failure with context ("failed to read file content"). The path resolution error from `security.SafeResolve` propagates unwrapped.

### Safe Write — `WriteFileSafely`

```go
func WriteFileSafely(repoRoot string, virtualPath string, data []byte) error
```

Resolves the virtual path against `repoRoot`, then writes via `os.WriteFile`. Wraps any write failure with context ("failed to write file content"). The path resolution error from `security.SafeResolve` propagates unwrapped.

### Git Abstraction — `RunGit`

```go
func RunGit(repoRoot string, args ...string) (string, error)
```

Executes a git subprocess via `exec.CommandContext`. Returns stdout on success; wraps any failure with context ("failed to run git"). The command is spawned in the repository root so relative paths work naturally.