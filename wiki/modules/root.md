# code-reducer — Architecture & Module Documentation

## 1. Module Overview

**Module path:** `github.com/arrase/code-reducer` | **Go version:** 1.26

The Code-Reducer pipeline is composed of four independent subsystems that interact through well-defined interfaces and shared state contracts:

| Subsystem | Responsibility | Key Data Contract |
|-----------|----------------|--------------------|
| `cmd` | CLI command surface (Cobra) | Root orchestration → engine dispatch |
| `internal/config` | Configuration resolution & persistence | `Config` struct (YAML-serializable); 4-layer precedence merge |
| `security` (path safety) | Path containment enforcement & process serialization | `SafeResolve`, `SimpleLock` |
| `internal/tools` | Repository verification, safe I/O, file discovery | Virtual path abstraction; TOCTOU-safe writes |
| `internal/engine` | Map-Reduce pipeline orchestration | `DirNode` tree, `Event` log, `FileChange` records |

**Execution lifecycle:** Runner acquires process lock (`security`) → verifies repository root (`tools`) → loads/resolves configuration (`config`) → executes synthesis pipeline (`engine`). All I/O operations pass through `SafeResolve` to prevent path traversal escapes outside the canonical repo root.

---

## 2. CLI Command Surface — `cmd` Package

### Root Orchestration (`root.go`)

| Component | Responsibility |
|-----------|----------------|
| `RootCmd` | Cobra root command instance; completion options disabled globally |
| `executeCommand(mode string)` | Orchestrates full documentation workflow: git validation → implicit setup if needed → merged configuration resolution → `.metadata.json` check → `NeedsCredentialSetup()` gate → engine dispatch with signal handling |
| `NeedsCredentialSetup()` | Returns `true` when resolved config lacks a critical LLM model ID; blocks execution until credentials are provisioned |

**Data flow:** `RootCmd` → `executeCommand(mode)` → git validation → implicit setup (if missing) → configuration merge → `.metadata.json` check → `NeedsCredentialSetup()` gate → engine dispatch with signal handling.

### Subcommands

| Component | Responsibility |
|-----------|-----------------|
| `initCmd` (`init.go`) | Triggers repository scanning and generates initial wiki markdown pages on first invocation |
| `updateCmd` (`update.go`) | Scans changed files since the last documented commit; regenerates wiki pages accordingly |

**Registration order:** Package init in `update.go` registers `updateCmd` with `RootCmd`. User invokes `init`, `update`, or setup flow via CLI. Setup flow populates `.code-reducer.yaml` if absent; otherwise loads merged configuration. `executeCommand(mode)` validates state and dispatches engine.

### Interactive Setup Flow (`setup.go`)

| Component | Responsibility |
|-----------|-----------------|
| `RunSetupFlow` | Guides user through interactive setup to generate `.code-reducer.yaml`; prompts for model ID, Ollama Base URL, context size, ignored directories/files, ignored extensions, and documentation directory |

**Data flow:** CLI prompt → parameter collection (model ID, Ollama Base URL, context size, ignore paths/extension, doc dir) → `.code-reducer.yaml` generation → file write-back → configuration ready for `executeCommand`.

---

## 3. Configuration Management — `internal/config`

### Responsibility
Central configuration management for the Code-Reducer pipeline. Handles persistence of user-defined settings (`.code-reducer.yaml`), runtime overrides via environment variables, defaults, and CLI flag merging through a single resolved configuration object consumed by downstream components (model providers, tracing integrations, file-system traversal).

**Data flow:** `ConfigExists(dir)` → `LoadConfig(dir)` → `ResolveConfig()` → `SaveConfig(Config)`. The resolve step merges four precedence layers: **system defaults** < **YAML config** < **environment variables** < **CLI flags**. The final resolved struct is the canonical configuration passed to every pipeline stage.

### Types

```go
type Config struct {
    Model   *ModelConfig     `yaml:"model,omitempty"`
    Tracing *TracingConfig   `yaml:"tracing,omitempty"`
    Ignore  []string         `yaml:"ignore,omitempty"`
    DocsDir string           `yaml:"docsDir,omitempty"`
}

type MetadataCache struct {
    LastCommitID  string            // last documented commit identifier
    CachedFiles   map[string]string // file path → SHA256 hash
    CachedModules map[string]string // module path → doc summary
}
```

`Config` is the sole struct persisted to disk. All fields are YAML-serializable; pointer semantics prevent zero-value marshaling for optional sections (model, tracing). `MetadataCache` maintains last documented commit identifier along with maps of cached files and modules.

### Constants — Environment Variable Keys & Defaults

| Constant | Purpose |
|---|---------|
| `CodeReducerModelIdEnvKey` | Runtime override of LLM model ID without file edit |
| `OllamaBaseUrlEnvKey` | Custom Ollama API base URL (defaults to `http://localhost:11434`) |
| `OllamaNumCtxEnvKey` | Ollama context window size in tokens |
| `LangsmithApiKeyEnvKey` | LangSmith tracing API key |
| `LangchainProjectEnvKey` | LangChain project name |
| `LangchainTracingEnvKey` | Toggle for LangChain v2 tracing |
| `OllamaDefaultBaseURL` | Hardcoded fallback Ollama base URL (`http://localhost:11434`) |
| `OllamaDefaultNumCtx` | Hardcoded fallback context window size (8192 tokens) |
| `ConfigFileName` | Path suffix for the user config file (`.code-reducer.yaml`) |

### Package-Level Defaults — Ignore Lists

| Variable | Purpose |
|---|---------|
| `DefaultIgnores` | Directory names excluded from analysis by default (`.git`, `node_modules`, `dist`) |
| `DefaultIgnoredExtensions` | File extensions excluded from analysis by default (`.png`, `.zip`, `.pyc`) |

These lists are merged with user-supplied ignore entries during the resolve phase and applied to file-system traversal downstream.

### Functions — Config Validation & Persistence

#### `ConfigExists(dir string) bool`
Returns whether a valid `.code-reducer.yaml` exists at `dir/ConfigFileName`. Does not read contents—only checks filesystem presence. Used by CLI entry points to prompt users for initial configuration on first run.

**Data flow:** `dir` → OS.Stat(`dir + "/" + ConfigFileName`) → `true/false`

#### `LoadConfig(dir string) (Config, error)`
Reads raw YAML bytes from `dir/ConfigFileName`, unmarshals into a `Config` struct. Returns an error if the file is missing or malformed.

**Data flow:** `dir + "/" + ConfigFileName` → OS.ReadFile → yaml.Unmarshal → `Config`. If unmarshal fails, returns zero-value `Config` and non-nil error.

#### `SaveConfig(cfg Config) error`
Serializes a `Config` struct to YAML and writes it to the working directory's `.code-reducer.yaml` with **0600** permissions for security (read/write owner only).

**Data flow:** `cfg` → yaml.Marshal → OS.OpenFile(`dir + "/" + ConfigFileName`, 0600) → OS.Write

#### `ResolveConfig() (Config, error)`
Merges four precedence layers into a single fully-resolved `Config`: **system defaults** (`OllamaDefault*` constants) < **YAML config** (from `LoadConfig`) < **environment variables** (`*_EnvKey` constants) < **CLI flags**. Returns the canonical configuration for pipeline consumption.

**Data flow:**
1. Load YAML from working directory via `LoadConfig`
2. Read each `_*EnvKey` environment variable; apply overrides where set
3. Apply CLI flag values (passed as function parameters)
4. Fill missing fields with defaults (`OllamaDefaultBaseURL`, `OllamaDefaultNumCtx`)
5. Merge `DefaultIgnores` and `DefaultIgnoredExtensions` into ignore lists

The resolved struct is the authoritative configuration consumed by model providers, tracing backends, and file-system traversal logic downstream in the pipeline.

---

## 4. Security & Process Locking — Path Safety

### Responsibility
Implements two orthogonal safety guarantees for the Code-Reducer execution environment: **path containment enforcement** and **process serialization via file-based locking**. All public functions operate against an absolute repository root derived at initialization, ensuring that no downstream operation can escape the intended working directory. The data flow follows a lifecycle pattern:

1.  **Initialization**: `EnsureGitignoreHasLockfile` registers the lock artifact in `.gitignore` to prevent version control pollution
2.  **Serialization Entry**: `AcquireLock` atomically claims an exclusive process slot by writing the current PID into `LockFileName` (`.code-reducer.lock`) using `O_EXCL`, returning a `SimpleLock` handle
3.  **Execution Guardrail**: Any I/O operation must pass through `SafeResolve`, which canonicalizes relative paths and rejects any traversal attempt that would resolve outside the repository root
4.  **Serialization Exit**: `Unlock` closes the underlying file descriptor and unlinks the lockfile, releasing the process slot for the next iteration or concurrent worker

### Path Safety: `SafeResolve`

**Function:** `SafeResolve(path string) (string, error)`

Canonicalizes an input path into its absolute representation relative to the repository root. Performs a strict containment check; returns an error if the resolved target lies outside the boundary or if any traversal component (`..`) is detected pre-resolution. This prevents directory escape vectors and symlink-based bypasses during file system operations.

### Process Locking Protocol

#### Initialization & Cleanup

**Function:** `EnsureGitignoreHasLockfile()`

Idempotently appends `LockFileName` to the repository's `.gitignore`. If the entry does not exist, it creates a new line; if present, it is left untouched. Ensures the lock mechanism remains ephemeral and local to the working tree.

**Function:** `Unlock(lock SimpleLock)`

Releases resources associated with an acquired `SimpleLock`. Closes the underlying file handle (`os.File`) and calls `os.Remove` on the lockfile path stored within the struct. This guarantees no stale PID files persist after process termination or intentional release, preventing deadlock conditions for subsequent acquire attempts.

#### Lock Acquisition

**Function:** `AcquireLock()`

Establishes an exclusive process lock by opening `LockFileName` (`.code-reducer.lock`) with `O_WRONLY | O_CREATE | O_EXCL`. The `O_EXCL` flag enforces atomicity: if a previous instance holds the file, the open fails immediately. On success, the current PID is written to the file descriptor and flushed, then the handle is returned as a `SimpleLock`. This state represents the "locked" condition for the duration of the reduction task.

**Type:** `SimpleLock`

Struct wrapping the canonical lockfile path (`string`) and an associated `*os.File` handle. Represents the active ownership of the process slot. Functions `Unlock` and internal acquire logic consume this struct to manage lifecycle transitions between unlocked, locked, and closing states.

**Constant:** `LockFileName` — String constant defining the artifact name `.code-reducer.lock`. Serves as the sole input to path resolution during lock acquisition and cleanup.

---

## 5. Repository Infrastructure — `internal/tools`

### Responsibility
Provides low-level repository operations required by higher-level analysis modules: safe file I/O, git state queries, path hashing, and recursive source-file discovery. The data flow follows a consistent pattern—**verify the environment → resolve paths safely → read/write with TOCTOU guards → filter against ignore rules**. All functions operate on an absolute or virtual repository root that is resolved once per session; subsequent operations reference this canonical location internally.

### Repository Verification and Git Operations (`git_tools.go`)

#### `VerifyGitRepo(repoPath string) error`
Confirms two preconditions before any git or filesystem operation proceeds:
1.  The host system has a `git` executable accessible via `$PATH`
2.  `repoPath` is inside a valid git working tree (i.e., `.git/` exists and the path resolves to it)

Returns an error if either check fails, preventing silent misbehavior downstream when callers assume a git-backed repository.

#### `GetGitHead(repoPath string) (string, error)`
Delegates to `RunGit("rev-parse", "HEAD")`, trims whitespace from stdout, and returns the current commit hash. Used by discovery and hashing modules to anchor state snapshots.

#### `RunGit(repoPath string, args ...string) (string, error)`
Executes a git command inside `repoPath`. Returns the trimmed stdout string plus any error; stderr is discarded in this wrapper because higher-level callers typically only need success output or a failure signal.

### Safe File I/O Operations (`file_tools.go`)

#### Read Pathway: `ReadFileSafely(virtualPath string) ([]byte, error)`
Resolves a virtual (relative-to-repo-root) path to its absolute on-disk location and reads the content into memory. The resolver normalizes separators and canonicalizes the result so that `./foo`, `../bar/baz`, and `baz` all produce identical byte slices for the same file.

#### Write Pathway: `WriteFileSafely(absPath string, data []byte) error`
Writes file content using a **TOCTOU-safe pattern**: before truncating the target, it verifies that `absPath` is not a symlink (via `os.Lstat()`). If the path is a symlink, the function returns an error rather than silently following the link and overwriting its destination. This prevents privilege escalation vectors where an attacker replaces a file with a symlink pointing to `/etc/passwd`.

### Path Resolution and Hashing (`registry.go`)

#### `HashRepoRoot(path string) string`
Computes the SHA-256 hex digest of the resolved absolute path of the repository root. The input `path` may be relative or absolute; internal resolution normalizes it before hashing. This produces a deterministic fingerprint for the current repo location, useful for caching, diff detection, and state anchoring across runs.

### File Discovery and Ignore Logic (`file_tools.go`)

#### `LoadGitignore(repoRoot string) ([]string, error)`
Reads `.gitignore` from `repoRoot`, parses line-by-line, skips comments (`#`) and blank lines, and returns the list of active ignore patterns. Patterns are returned in their original form (no glob expansion); callers apply them against relative paths using standard path-matching semantics.

#### `ShouldIgnoreFile(relPath string) bool`
Determines whether a relative file path should be excluded from processing by evaluating:
-  Custom patterns loaded via `LoadGitignore`
-  Dot-prefixed components (`. prefix at any level)
-  Known binary/extension suffixes (e.g., `.exe`, `.so`)
-  Suffix-based rules for common non-source extensions

Returns `true` if the file matches any ignore criterion; otherwise `false`.

#### `DiscoverCodeFiles(repoRoot string) ([]string, error)`
Recursively walks the repository tree to collect high-signal source files (`.go`, `.py`, `.rs`, etc.). Skips:
-  Build directories (`node_modules/`, `vendor/`, `__pycache__/`, etc.)
-  Dependency paths identified by common prefixes
-  Any entry matching the ignore list produced by `ShouldIgnoreFile`

Returns a flat slice of relative file paths, sorted for deterministic output. Used as the primary input to analysis and transformation pipelines.

---

## 6. Code Reduction Engine — `internal/engine`

### Responsibility & Data Flow
The `internal/engine` module implements the Map-Reduce pipeline for automated codebase documentation generation. It orchestrates three phases: **discover** (file-system traversal and tree construction), **synthesize** (LLM-driven hierarchical content aggregation with SHA256 caching), and **reduce** (global architecture summary assembly). The runner exposes a single entry point that acquires the lock, instantiates the LLM client, and dispatches to context-specific pipelines.

### File System Traversal & Tree Construction

#### `DirNode`
```go
type DirNode struct {
    Path     string   // absolute path
    Files    []string // direct files within this directory
    Children []*DirNode // subdirectories
}
```
Represents a node in the recursive directory tree. Each `DirNode` holds its absolute path, a flat list of immediate file paths, and child subtrees for nested directories.

#### `buildTree`
Constructs a root `*DirNode` from a slice of absolute file paths by parsing each path into directory components, then recursively nesting children under their parent nodes.

#### `FileChange`
```go
type FileChange struct {
    Path   string // relative file path
    Status string // "Added", "Modified", or "Deleted"
}
```
A single file-change record produced by diffing the current state against the baseline repository snapshot.

#### `determineAffected / propagateAffected`
`determineAffected` walks the directory tree and marks directories as **affected** when any descendant is marked affected, a module doc is missing, or cache entries are empty. `propagateAffected` is the recursive walker that bubbles upward from leaf nodes to ancestors, ensuring every ancestor of a changed file inherits the affected flag.

#### `isAllowedFile`
Returns `true` when the relative path passes ignore rules defined by `*security.ShouldIgnoreFile`. Paths failing this check are excluded from tree construction and subsequent synthesis.

### LLM Client & Communication Layer

#### `Message`
```go
type Message struct {
    Role   string // role identifier (system / user)
    Content string // text payload for prompt construction
}
```
Represents a single turn in an LLM interaction record used to construct the full prompt input.

#### `LLMClient & NewLLMClient`
Aggregates connection settings and HTTP transport required to communicate with an Ollama-compatible endpoint. `NewLLMClient` instantiates a new client initialized with model ID, base URL, context window size, and default timeout.

#### `CallLLM / StreamLLM`
-  **CallLLM** — Executes a synchronous non-streaming HTTP POST to the configured Ollama API endpoint; returns the raw response string or an error
-  **StreamLLM** — Initiates a streaming HTTP request, parses incoming chunks line-by-line, and invokes the supplied callback for each content segment

#### `stripOuterMarkdownFence / CleanJSONResponse / UnmarshalJSONResponse`
`stripOuterMarkdownFence` strips surrounding markdown code fences from input strings using regex to clean common LLM output artifacts. `CleanJSONResponse` extends this by stripping fences and isolating the first/last JSON delimiters when fences are absent. `UnmarshalJSONResponse` chains both: cleans the raw response then unmarshals into a target interface, wrapping parse errors with context.

#### `GetDefaultSystemPrompt / LoadSystemPrompt`
`GetDefaultSystemPrompt` returns a context-specific system prompt string based on command type, defining the AI persona and task instructions for each analysis mode. `LoadSystemPrompt` delegates directly without additional error handling.

### Caching Layer

#### `FileCacheEntry & MetadataCache`
-  **FileCacheEntry** — Stores the SHA256 hash and associated facts for a single file within the cache structure
-  **MetadataCache** — Maintains the last documented commit identifier along with maps of cached files and modules

#### `loadMetadataCache / saveMetadataCache`
`loadMetadataCache` reads a metadata JSON from the docs directory, returning an initialized or parsed `*MetadataCache`. `saveMetadataCache` serializes the provided instance into a formatted JSON file at the specified metadata path.

#### `computeSHA256`
Calculates the SHA-256 hash of content located at a given virtual path and returns the hex-encoded string. Used to deduplicate synthesis results across re-runs.

### Core Engine Pipeline

#### `synthesizeNode`
Traverses a directory tree node's children and files, extracts file facts through LLM analysis with SHA256 caching, then hierarchically merges all components into a consolidated summary. Each leaf is analyzed independently; intermediate nodes aggregate their descendants' summaries before returning upward.

#### `reduceInChunks`
Recursively batches code items by character limit and synthesizes them via LLM calls to produce architecture summaries, truncating oversized content when needed. Splits the input set into chunks that fit within the context window, processes each chunk through `synthesizeNode`, then merges results bottom-up until a single root summary remains.

#### `RunInit` (method on `*LLMClient`)
Executes the full Map-Reduce pipeline: discovers code files, builds the hierarchical directory tree, performs recursive synthesis via `reduceInChunks`, and generates global architecture documentation plus a quickstart guide. Returns an `Event` struct holding Type and Message for logging emitted during execution.

### Orchestration & Runner

#### `Runner / NewRunner / Run`
-  **Runner** — Struct holding an engine configuration pointer; primary container for executing code-reduction pipelines
-  **NewRunner** — Initializes a new `*Runner` by accepting a config pointer and storing it within the struct fields
-  **Run** — Orchestrates the full execution lifecycle: acquires lock, instantiates LLM client from config, dispatches to context-specific documentation pipeline based on repository root and command type

### Event Logging

#### `Event`
```go
type Event struct {
    Type    string // event category identifier
    Message string // human-readable description of the emitted event
}
```
Struct holding a Type string and Message string used for logging events emitted during engine execution.