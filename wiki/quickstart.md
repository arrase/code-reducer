# Quickstart — Code-Reducer Architecture

## System Boundary

Code-Reducer is an interactive LLM-driven static analysis pipeline that generates hierarchical markdown documentation for software repositories. The binary entry point (`main.go`) delegates execution to `cmd.RootCmd.Execute()`, which routes through a lifecycle of three engine modes—`init`, `update`, and one additional mode resolved by `executeCommand`. All domain logic lives in the `internal/` packages; no domain logic is implemented at the top level.

## Module Data Flow

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

## Module Responsibilities

| Package | Role |
|---|---|
| `internal/tools` | Safe file I/O with TOCTOU protection and atomic writes; git command abstraction for subprocess execution |
| `internal/security` | Path traversal prevention via `SafeResolve`; cross-process lock management via `SimpleLock`/`AcquireLock`/`Unlock` |
| `internal/config` | Runtime configuration schema (LLM prompts, extraction steps, ignore lists); atomic config persistence; multi-source resolution (YAML → env → flags → defaults) |
| `internal/engine` | End-to-end pipeline: discovers source files, classifies change status against cached state, recursively synthesizes hierarchical summaries via LLM calls, persists results as markdown documentation |

## Configuration Resolution

`Config` is the single struct passed between `SaveConfig`, `LoadConfig`, and `ResolveConfig`. All three functions operate on it by value or pointer — `SaveConfig` and `LoadConfig` accept only a directory path, while `ResolveConfig` takes directory plus two flag arguments. No methods are defined on `Config`; all mutation happens in external packages via struct initialization.

### Priority Order

1. Hardcoded defaults
2. YAML config file (`.code-reducer.yaml`)
3. Environment variables (`CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX`)
4. CLI flags

If the YAML file does not exist, absence is accepted as valid and an empty `Config{}` is substituted. Parse errors are wrapped with `"failed to parse yaml config:"`; I/O faults become `"failed to load configuration file:"`.

### Default Extraction Steps

`DefaultExtractionSteps` is a package-level `var` of type `[]ExtractionStep`, pre-populated with four entries:

| Index | Name | Purpose |
|---|---|---|
| 0 | `API_SIGNATURES` | Extracts public types, functions, methods and their signatures without explaining internal logic. |
| 1 | `BUSINESS_LOGIC` | Explains the primary domain problem solved by the code and lists high-level algorithm steps, ignoring implementation syntax. |
| 2 | `STATE_AND_CONCURRENCY` | Identifies mutable global/state variables and synchronization mechanisms; outputs `"No mutable state"` if entirely stateless. |
| 3 | `ERRORS_AND_SIDE_EFFECTS` | Details interactions with external systems (network, disk, databases) and how errors propagate or are swallowed. |

## Engine Pipeline

The engine orchestrates documentation generation in two modes:

- **Init** (`ModeInit`) — full regeneration of all documentation
- **Update** (`mode ModeUpdate`) — incremental reprocessing limited to directories whose descendants contain changed files

### Data Flow

1. `Runner.Run()` → orchestrator selection → tree construction → per-node synthesis (bottom-up) → global doc generation → cache persistence
2. All external dependencies funnel through the `llmCaller` interface, enabling transport-level abstraction without coupling the engine layer to a specific LLM provider.

### LLM Client Layer (`client.go`)

Interaction is abstracted behind the `llmCaller` interface so the engine layer can depend on it without tying to a specific transport implementation. Interaction targets Ollama's `api/chat` protocol via HTTP POST:

1. Prepend any system prompt to the user-provided message list
2. Serialize into Ollama's expected JSON schema (model ID, messages, stream flag, optional format/options)
3. Execute synchronous HTTP POST to `baseURL/api/chat` with a context-aware client and fixed timeout; no retries are attempted
4. On 200 OK, deserialize the JSON into an Ollama-style envelope and return only the model's reply content

**Error Propagation:** Non-OK status returns `"ollama api error: status {code}, response: {text}"` with body read up to `maxErrorBodyBytes`. Successful status plus parse error yields a wrapped error `"failed to parse response: %w"`. Transport failure (network down, DNS fail, timeout) is returned directly without retry logic.

### Metadata Cache Layer (`cache.go`)

The engine persists per-file extraction results ("facts") along with file integrity hashes, enabling change detection and incremental reprocessing between pipeline runs. The cache is stored as a versioned JSON file at `<docsDir>/<metadataFileName>` relative to `repoRoot`.

**Initialization and Loading:** Initialize empty cache → load existing cache from disk (missing file returns fresh empty cache; incompatible versions silently recover) → nil safety: maps initialized to nil on load are replaced with fresh empty maps.

### Tree Construction and Change Detection (`tree.go`)

This code builds an in-memory directory tree from file paths, then determines which directories are affected by a set of file changes (additions, modifications, deletions) by traversing the tree and marking parent directories whose children have changed or been removed.

**Algorithm Steps:**
1. Parse each input file path into hierarchical components (`/`-separated), recursively constructing `DirNode` objects that represent folders with their files and child subdirectories
2. Initialize affected set; for each file change, if its status is "Deleted", immediately mark its parent directory as affected
3. Walk every `DirNode`: if any file under that node appears in a changed-file map, mark the node's path as affected; check whether a corresponding markdown module path exists at disk (relative to `docs/modules/`). If it does not exist, mark the directory as affected — implying the module is being added or removed. Recursively process all child directories
4. Walk again: if any descendant is marked affected, propagate that status upward to parent nodes so ancestors of changed subtrees are also flagged

### Hierarchical Synthesis Pipeline (`synthesize.go`)

This code implements an automated codebase summarization engine that recursively analyzes source files and directories, extracting structured facts about each file's purpose/behavior via LLM calls, then synthesizing hierarchical summaries upward through a directory tree — ultimately producing per-directory documentation artifacts.

**Algorithm Steps:**
1. Tree Traversal (Bottom-Up): Recursively process a directory node by first visiting all child directories (sorted for determinism), then processing files within the current directory
2. File Fact Extraction: check cache and precomputed hashes before reading file content; read raw file bytes, compute SHA-256 hash if not already cached; split file content into overlapping chunks (size dynamically scaled from LLM context window); for each chunk, invoke an LLM with a system prompt + step-specific user prompt to extract facts; consolidate all chunk results through a reduction step per extraction step. Repeat across multiple sequential extraction steps until all perspectives are captured
3. Component Assembly: Combine extracted file summaries and synthesized child-directory summaries into a unified list of components for the current directory node
4. Directory Synthesis: Apply multi-step LLM-based chunked reduction on the assembled components to produce a single consolidated summary for the entire directory
5. Persistence and Caching: Store computed hashes, file facts, and directory summaries in metadata caches; write final directory summaries to disk as markdown documentation under `cfg.DocsDir/modules/<safe-filename>`

### Chunking and Reduction Algorithms (`chunking.go`)

This module implements a **map-reduce tree reduction** algorithm for LLM-based code synthesis. It takes multiple text items (code facts, file descriptions, architecture notes), batches them within context-window limits, sends each batch to an LLM, and recursively reduces the outputs until a single consolidated result remains — stopping when further reduction would not shrink the output significantly (loop prevention).

**Algorithm Steps:**
1. Return empty string if no items provided; return item as-is for single-item inputs in `reduceFileFacts`
2. Pre-Expansion: Any individual item exceeding the character limit is split into smaller overlapping chunks via `chunkTextWithOverlap`, then all resulting pieces are pooled back into a flat list
3. Binning by Size: Items (and pre-expanded chunks) are grouped into batches such that no batch exceeds `maxChars` runes, with overflow items starting a new batch
4. Recursive Reduction: Each batch is sent to the LLM via `reduceFn`. The function calls itself recursively on each batch's result until only one item remains in `intermediate`
5. Loop Prevention Check: Before recursing again, total output runes are compared against 95% of total input runes. If output ≥ 95% of input (information is not being condensed), the algorithm stops and returns all intermediate results concatenated with double newlines, preserving information without exceeding context windows
6. LLM Integration: The LLM caller receives a system prompt plus user content formed by joining batch items with double newlines. Markdown fences are stripped from the response before return via `stripOuterMarkdownFence`

### Orchestrator Pipeline (`orchestrator.go`)

This code implements a Map-Reduce pipeline that automatically generates and maintains documentation for software repositories by recursively analyzing source code structure with an LLM, then writing the synthesized results back as markdown files (architecture overview, quickstart guide, per-module summaries).

**Algorithm Steps:**
1. Code Discovery: Locate all code files in the repository root, filtering out documentation directories and patterns from `.gitignore` + user-configured ignore lists
2. Hash-Based Change Detection: Compute SHA256 hashes for each discovered file and compare against a cached state to classify changes as Added, Modified, or Deleted
3. Tree Construction: Organize the code files into a hierarchical directory tree structure (`DirNode` with children)
4. Affected Directory Determination: *Init mode*: Mark all directories as affected (full regeneration). *Update mode*: Identify only those directories whose descendants contain changed files, propagating "affected" status upward through the tree
5. Hierarchical Tree-Merging: Recursively synthesize each node starting from leaves and working toward the root. Each synthesis call to the LLM produces a summary of that directory's code, which becomes input for parent-level synthesis (reducing as you move up)
6. Standard Documentation Generation: After the tree is fully synthesized, generate two global documents: `architecture.md` — high-level system overview based on the root synthesis output; `quickstart.md` — onboarding and usage guide derived from the same root summary
7. Agent Guidelines Update: Write or append an AI Agent Guidelines file that references all generated documentation paths, ensuring future agent interactions are informed by existing docs
8. Cache Maintenance: *Pruning*: Remove cache entries for directories no longer present in the code tree and delete their corresponding markdown files on disk. *Invalidation*: If extraction steps configuration changes, reset the file cache to force full regeneration on next run

### Runner Orchestration (`runner.go`)

This code manages and orchestrates an automated documentation generation pipeline for software projects, supporting both initial documentation creation (init mode) and incremental updates to existing documentation (update mode). It serves as the main entry point that coordinates AI-powered document processing across a repository.

**Algorithm Steps:**
1. Ensure project lockfile is added to `.gitignore` for version control safety
2. Acquire an exclusive repository lock to prevent concurrent documentation operations
3. Initialize LLM client and orchestrator based on provided configuration (model, base URL, context settings)
4. Execute the appropriate pipeline mode: either run full initialization or incremental update using the configured AI model

**Synchronization and Locking:** A repository-level lock is acquired via `security.AcquireLock(repoRoot)` and released via `defer lock.Unlock()`. This protects repository state during pipeline execution. The exact lock type (file-based, in-memory) depends on implementation in `internal/security`. All other struct fields are assigned once in `NewRunner()` and never modified after that; no mutable shared state exists within this file's boundary.

### Utility Helpers (`utils.go`)

This file provides lightweight utility helpers for the engine module: generating safe markdown filenames from directory paths, and creating adapter-style log event callbacks that support optional listeners.

**Domain Rules:**
- Empty/`.` module paths map to `"README.md"` by default via `toSafeMarkdownFilename`
- Unknown or invalid callbacks are silently swallowed — no panic on nil listeners in `makeLogEvent`

## Security Module (`internal/security`)

This module provides two security primitives scoped to a repository root:

1. **Path traversal prevention** via `SafeResolve`, which validates that user-supplied paths resolve within the repository boundary, resolving symlinks on existing ancestor parts before comparison
2. **Cross-process lock management** via `SimpleLock`/`AcquireLock`/`Unlock`, which ensures only one process holds exclusive access to a protected resource at any given time through an atomic file-based lock mechanism

Both primitives operate against the local filesystem; no network, database, or remote API interactions occur within production code. All errors are returned to callers and propagated via Go's `error` interface; panics are not raised by this module.

### Path Resolution — `SafeResolve`

```go
func SafeResolve(repoRoot, inputPath string) (string, error)
```

Resolves a candidate path against an anchor root while preventing escape through symlinks and directory traversal. The function returns the cleaned absolute path if it remains strictly inside the resolved repository boundary; otherwise it returns `ErrPathTraversal`.

**Algorithm Steps:**
1. Compute the absolute root directory from `repoRoot` via `filepath.Abs`, wrapping any resulting error with `%w`
2. Resolve symlinks on the absolute root using `filepath.EvalSymlinks(absRoot)`, again wrapping errors with `%w`
3. Walk up from the joined path (`absRoot + inputPath`) until a physically existing ancestor is found via repeated `os.Lstat(current)` calls; each Lstat failure that is not "not exist" is wrapped and returned immediately
4. Resolve symlinks on the first physically existing ancestor via `filepath.EvalSymlinks(current)`, wrapping errors with `%w`
5. Reconstruct the full target path from the resolved ancestor plus all previously-skipped components
6. Verify that the reconstructed path remains inside the resolved root; reject if it escapes by returning a wrapped error using `ErrPathTraversal` with `inputPath` as context

### Lock Acquisition and Release — `SimpleLock`

**Algorithm Steps:**
1. Calls `SafeResolve(repoRoot, LockFileName)` to obtain the canonical lock file path inside the repo root. If this fails, the error propagates directly without wrapping
2. Opens the lockfile with `os.OpenFile(lockPath, O_WRONLY|O_CREATE|O_EXCL, 0644)`. The OS-level atomicity of O_EXCL means failure indicates another writer holds the file or a stale lock persists; this is treated as an error condition requiring manual cleanup by the caller
3. Writes the current process PID into the lockfile via `f.Write([]byte(fmt.Sprintf("%d\n", os.Getpid())))` for identification/inspection purposes. If this write fails, the method closes the file and removes it before returning a wrapped error

**Unlock() error (method on *SimpleLock)** — Releases the lock by closing the file descriptor and removing the lockfile from disk. Idempotent and thread-safe with respect to itself. The struct owns its own mutex; concurrent calls to `Unlock` on the same instance serialize through `mu.Lock()/defer mu.Unlock()`, making the operation atomic with respect to itself.

## Tools Module (`internal/tools`)

The `internal/tools` package provides two complementary capabilities for analyzing a code repository's structure: (1) safe file I/O operations with TOCTOU protection and atomic writes, and (2) Git command abstraction for process execution. All functions operate against a local filesystem only; no network calls, database interactions, or external API invocations occur.

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