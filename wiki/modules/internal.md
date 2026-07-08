# Code Reducer — Internal Architecture Reference

## Module Overview & Data Flow

The Code Reducer application is structured around four internal subsystems that compose an end-to-end pipeline for generating and maintaining Markdown architecture documentation from code repositories. The canonical data flow proceeds: **configuration resolution** → **repository verification** → **directory tree construction** → **recursive synthesis with LLM extraction** → **standard docs generation** → **metadata persistence**. Each subsystem enforces security boundaries (path validation, process locking) and idempotent operations where applicable; side-effecting writes enforce restricted permissions (`0600`).

---

## Subsystem: config — Configuration Resolution Pipeline

Centralizes all configuration sources for the application and resolves them into a single authoritative `Config` value through a deterministic merge pipeline. Sources are layered from lowest to highest priority: system defaults → YAML file → environment variables → CLI flags. All operations are idempotent where applicable; side-effecting writes enforce restricted permissions (`0600`).

### Constants

Environment variable keys and fallback constants governing external override behavior:

| Constant | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Runtime key for overriding the LLM model identifier. |
| `OllamaBaseUrlEnvKey` | Runtime key for overriding the Ollama server address. |
| `OllamaNumCtxEnvKey` | Runtime key for overriding the context/token count. |
| `LangsmithApiKeyEnvKey` | LangSmith API tracing credential key. |
| `LangchainProjectEnvKey` | LangChain project name setting. |
| `LangchainTracingEnvKey` | Enables/disables LangChain v2 tracing. |
| `OllamaDefaultBaseURL` | Fallback Ollama address when no env/config override is present. |
| `OllamaDefaultNumCtx` | Default context size of 8192 tokens applied in the absence of overrides. |
| `ConfigFileName` | Filesystem name `.code-reducer.yaml` used to locate the project configuration file. |

### Types & Defaults

#### `Config`

Core application configuration schema aggregating: LLM model ID, Ollama server/connection settings (base URL, context size), LangSmith/LangChain tracing flags, ignored filesystem paths and extensions, and the docs directory path. Serves as the canonical resolved state consumed downstream by the analyzer pipeline.

#### Default Exclusions

Package-level slices defining filesystem paths excluded from analysis:

- `DefaultIgnores` — Directory name exclusions (e.g., `.git`, `node_modules`, build artifacts).
- `DefaultIgnoredExtensions` — File extension exclusions (e.g., images, binaries, lock files).

### I/O Operations

#### `ConfigExists(cwd string) bool`

Returns `true` when `.code-reducer.yaml` exists under the given working directory. Used as a gate for conditional config loading in the resolution pipeline.

#### `LoadConfig(cwd string) (*Config, error)`

Reads and parses `.code-reducer.yaml` from `cwd` into a populated `Config` struct. Returns an error on parse failure or missing file; callers must check existence via `ConfigExists` first to avoid spurious errors.

#### `SaveConfig(cwd string, cfg *Config) error`

Serializes the provided configuration to `.code-reducer.yaml` under `cwd`. Writes with restricted file permissions (`0600`) to prevent unintended access by other users on shared filesystems.

### Merge Utility

#### `MergeAndDeduplicate[T comparable](a, b []T) []T`

Generic slice concatenation and deduplication preserving insertion order. Applied when merging default ignore lists with user-provided overrides; preserves first-seen ordering to avoid reordering side effects in downstream path matching logic.

### Resolution Pipeline

#### `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config`

Orchestrates the full configuration resolution pipeline: merges CLI flags (`modelIdFlag`, `numCtxFlag`), environment variables (via the key constants above), YAML config (loaded by `LoadConfig`), and system defaults into a single resolved `Config`. Execution order enforces priority from lowest to highest: system defaults → YAML file → environment variables → CLI flags. The returned value is authoritative; downstream components read only this struct, never individual sources.

---

## Subsystem: engine — AI-Powered Documentation Synthesis System

Implements an end-to-end pipeline for generating and maintaining Markdown architecture documentation from code repositories using LLM inference. Performs recursive directory traversal with SHA256-based caching, incremental change detection via metadata persistence, and LLM-driven synthesis of per-node summaries aggregated into global architecture files.

**Data Flow:** Configuration → `Runner` instantiation → repository discovery → `DirNode` tree construction → recursive `synthesizeNode` (LLM extraction + cache lookup) → root summary → standard docs generation → metadata serialization via `MetadataCache`.

### LLM Client Abstraction (`client.go`)

Core inference interface wrapping HTTP communication with an Ollama-compatible backend.

#### `Message` Struct

```go
type Message struct {
    Role   string // "system", "user", "assistant"
    Content string
}
```

Stores a single turn in the conversation payload for serialization into API requests.

#### `LLMClient`

Holds configuration parameters (`modelID`, `baseURL`, `contextLength`) alongside an HTTP client used to communicate with the Ollama backend.

- **NewLLMClient** — Constructs and returns a new `LLMClient` instance initialized with provided model identifier, API endpoint, and maximum context size.
- **CallLLM** — Sends a non-streaming request to the language model via HTTP; returns the full text response or an error upon failure.
- **StreamLLM** — Initiates a streaming session; processes incoming chunks by calling a user-defined function for each content segment until the stream completes.
- **GetDefaultSystemPrompt** — Returns a contextualized system prompt string based on current operation mode, supporting tasks like code extraction, module synthesis, and architecture documentation.

### Caching Layer (`cache.go`)

Persistent state management for incremental updates by storing file-level integrity hashes and global cache metadata.

#### `FileCacheEntry`

Stores the SHA256 hash and facts string for individual files within the cache. Enables detection of content changes without re-reading source files during update cycles.

#### `MetadataCache`

Aggregates global cache state including: last documented commit reference, file entries (path → `FileCacheEntry` mapping), and module mappings.

**Cache Operations:**
- **loadMetadataCache** — Loads or creates an empty `MetadataCache` by reading from a `.metadata.json` file path relative to the docs directory.
- **saveMetadataCache** — Serializes the provided `MetadataCache` into indented JSON and writes it to the specified metadata cache path.
- **computeSHA256** — Computes the SHA256 hex digest of content read from a virtual file path relative to the repository root.

### Directory Tree Representation (`tree.go`)

Encodes the discovered code structure as a navigable tree for recursive processing.

#### `FileChange`

A struct holding `Path` and `Status` fields representing a single file change operation ("Added", "Modified", or "Deleted"). Used during incremental update cycles to report detected differences.

#### `DirNode`

Tree node struct containing: directory path, list of files (with their SHA256 hashes), and child nodes keyed by subdirectory name.

### Synthesis Engine (`synthesize.go`)

Recursive directory traversal that synthesizes Markdown summaries for code modules. The sole function `synthesizeNode` is unexported and processes child directories and files with LLM extraction, caching results via SHA256 hashing to avoid redundant work on unchanged nodes.

### JSON Response Parsing (`json_parser.go`)

Strips markdown/JSON code fences from raw LLM responses before deserialization.

- **StripOuterMarkdownFence** — Strips surrounding markdown or JSON code fences from input strings; returns the inner content.
- **CleanJSONResponse** — Removes markdown code fences, then uses brace/bracket matching to extract a complete JSON object or array from remaining text.
- **UnmarshalJSONResponse** — Cleans raw response string with `CleanJSONResponse` and deserializes it into provided target interface value.

### Pipeline Orchestrator (`orchestrator.go`)

Coordinates the full Map-Reduce pipeline for repository initialization and incremental updates.

#### `Event` Struct

Lightweight struct carrying `Type` (string) and `Message` (string), used to pass pipeline status updates back through callback interfaces.

#### `GenerateStandardDocs`

Exported method on `*LLMClient` that generates global architecture and quickstart Markdown files from a root summary via LLM calls; writes results safely to disk.

#### `RunInit`

Exported method on `*LLMClient` executing the full Map-Reduce pipeline for repository initialization:
1. Discovers code
2. Builds directory tree (`DirNode`)
3. Synthesizes root summary through hierarchical merging
4. Regenerates standard docs
5. Updates AGENTS.md with AI agent guidelines

#### `RunUpdate`

Exported method on `*LLMClient` for incremental operation:
1. Detects file changes (added/modified/deleted) by SHA256 comparison against metadata cache
2. Determines affected directories via tree traversal
3. Re-synthesizes summaries only where needed
4. Conditionally regenerates architecture or quickstart docs

### Configuration and Runner (`runner.go`)

#### `NewRunner`

Factory constructor that instantiates and returns a new `*Runner` with provided configuration pointer. Serves as the entry point for initializing the documentation generation system.

---

## Subsystem: security — Filesystem Isolation & Process Synchronization

Enforces filesystem isolation and process synchronization primitives for repository-level operations. Centralizes path validation against an absolute root boundary, manages concurrent access via atomic file-based locking (PID-pinned), and maintains version control hygiene by integrating lock artifacts into `.gitignore`. All state transitions revolve around the `repoRoot` absolute path as a constant security boundary.

### Path Validation & Traversal Protection

#### `SafeResolve(repoRoot, inputPath string) (string, error)`

Resolves an arbitrary user-supplied `inputPath` against the canonical absolute `repoRoot`. Performs cleanup of the input string to neutralize relative references (`..`, single-dot), then resolves the resulting path against the repository root. Returns a fully qualified absolute path if the resolved target remains within the `repoRoot` boundary; otherwise, returns an error to signal a traversal attempt or invalidation.

**Data Flow:**
1. **Input:** `inputPath` (untrusted string) + `repoRoot` (absolute, trusted constant).
2. **Sanitization:** `inputPath` is normalized to eliminate relative components and redundant separators.
3. **Resolution:** OS-level path resolution joins the sanitized input with `repoRoot`.
4. **Boundary Check:** The resolved string is compared against the prefix of `repoRoot`.
5. **Output:** Validated absolute path or error indicating traversal/invalidation failure.

### File-Based Process Locking

#### `SimpleLock` Struct

Encapsulates the state required to manage a single process-level lock:
- **LockFilePath** — Absolute filesystem location of the lockfile (typically `<repoRoot>/.code-reducer.lock`).
- **FileHandle** — OS file descriptor representing an acquired exclusive hold on the lockfile.

#### `AcquireLock(repoRoot string) (*SimpleLock, error)`

Establishes a new process-level lock using atomic file semantics:
1. Opens `<repoRoot>/.code-reducer.lock` with `O_EXCL` (exclusive creation).
2. Writes the current process PID (`os.Getpid()`) to the newly created file handle.
3. Returns a pointer to the resulting `SimpleLock`, holding the open file handle for subsequent lifecycle management.

#### `Unlock() error`

Releases the acquired lock:
1. Flushes and closes the underlying OS file handle associated with the `SimpleLock`.
2. Removes the lockfile from disk via filesystem delete operation.
3. Returns an error if removal fails (e.g., permission denied, path not found).

### Repository Configuration Maintenance

#### `EnsureGitignoreHasLockfile(repoRoot string) error`

Ensures the repository's `.gitignore` file tracks the lock artifact to prevent accidental version control inclusion:
1. Locates `<repoRoot>/.gitignore`.
2. Checks for existing content containing the entry `.code-reducer.lock`.
3. If absent, appends the entry to the file (creating it if nonexistent).

---

## Subsystem: tools — Repository Verification & Safe File I/O

Provides foundational utilities for interacting with git repositories and handling filesystem operations with security-focused semantics. All functions operate within a verified repository context established by `VerifyGitRepo`.

### 1. Repository Verification & Git Operations (`git_tools.go`)

#### `VerifyGitRepo(repoPath string) error`

Validates that:
- The `git` binary is present on the system PATH.
- The specified directory is a recognized git working tree by executing `rev-parse --is-inside-work-tree`.

Returns an error if either precondition fails. Callers should invoke this before any repository-aware operation.

#### `RunGit(repoPath string, args ...string) (string, error)`

Executes arbitrary git commands scoped to the verified working tree:
- Captures stdout and trims trailing whitespace.
- Propagates non-zero exit codes as errors.

Used for lightweight queries such as resolving relative paths, checking branch state, or retrieving repository metadata without requiring manual shell invocation by callers.

### 2. Safe File I/O Operations (`file_tools.go`)

#### `ReadFileSafely(repoPath string) ([]byte, error)`

Resolves a virtual repository-relative path to its absolute filesystem location and returns raw byte content. Performs an error-checked open/read/close cycle; returns the complete file payload or an error if the operation cannot be completed within expected bounds.

#### `WriteFileSafely(repoPath string, content []byte) error`

Writes arbitrary content to a resolved repository path with three safety measures:
- **Directory creation**: Parent directories are created if absent (with appropriate mode preservation).
- **Symlink detection**: Existing symlinks at the target path are detected and rejected to prevent silent redirect attacks.
- **TOCTOU-safe truncation**: The file is opened in exclusive-truncate mode (`O_RDWR | O_CREATE | O_TRUNC`) to eliminate race conditions between read and write phases.

#### `IsBinaryFile(path string) bool`

Scans the first 1024 bytes of a file for null byte (`\x00`) presence. Returns `true` if binary content is detected within that boundary. Used as a quick heuristic filter during code discovery to exclude non-source artifacts (images, compiled binaries, etc.) without loading entire files into memory.

### 3. Code Discovery & Filtering Pipeline (`file_tools.go`)

#### `LoadGitignore(repoRoot string) ([]string, error)`

Reads `.gitignore` from the given repository root, strips comment lines (`#`) and blank lines, and returns the active pattern set. Returns `nil` if the file does not exist or cannot be parsed. Patterns are returned as-is for downstream evaluation; callers should match them against relative paths using standard gitignore matching semantics.

#### `ShouldIgnoreFile(relPath string) bool`

Determines whether a given relative path matches any exclusion rule by evaluating:
- Active `.gitignore` patterns from `LoadGitignore`.
- Dot-prefixed directory components (e.g., `.build`, `.cache`).
- Known binary file extensions (detected via `IsBinaryFile` or extension whitelist).
- Null-byte presence in the path string.

Returns `true` if any exclusion rule matches, indicating the file should be skipped during discovery.

#### `DiscoverCodeFiles(repoRoot string) ([]string, error)`

Recursively walks the codebase rooted at `repoRoot`, collecting source files while skipping:
- Build directories (e.g., `node_modules`, `.git`, `target`).
- Paths matching any active `.gitignore` pattern from `LoadGitignore`.
- Files flagged as binary by `IsBinaryFile`.

Returns a sorted slice of relative path strings representing the discovered codebase. The walk order is deterministic; results are suitable for direct consumption by analysis or transformation pipelines.