# Code Reducer — Architecture Reference

## Module: `cmd` — CLI Entry Point & Subcommand Registry

**Responsibility:** Provides a cobra-based command-line interface exposing two subcommands (`setup`, `update`) registered against a shared `RootCmd`. All commands are wired via package-level initialization in `init.go`; no exported types exist beyond the cobra.Command instances themselves.

### Data Flow

1. **Invocation:** User process invokes CLI binary → `main()` delegates to `cmd.RootCmd.Execute()`; non-zero exit codes propagate as status 1.
2. **`setup` flow:** Interactive prompts collect LLM model ID, Ollama URL with optional context size, ignore lists (comma-separated), and docs directory path from stdin; values merge against defaults or existing list state; aggregated configuration is persisted via `config.SaveConfig`.
3. **`update` flow:** Detects changed files since the last documented commit using git diff semantics; triggers incremental wiki page regeneration against current source tree.

### Components

| Component | Description |
|---|---|
| `main()` — Entry point in `main.go` | Delegates to `cmd.RootCmd.Execute()`, exits with status 1 on error |
| `RootCmd` (unexported cobra.Command) — Defined in `root.go` | Top-level command instance configured with usage text and completion options; serves as the dispatch root for all CLI invocations via cobra's standard tree traversal |
| `NeedsCredentialSetup()` (package-level func, `root.go`) | Inspects resolved configuration file state; returns `true` when `ModelID` is absent from config, signaling that credential setup has not yet been completed |
| `setupCmd` (unexported cobra.Command) — Defined in `setup.go` | Cobra command definition for the `setup` subcommand; wired into `RootCmd` during package initialization |
| `updateCmd` (unexported cobra.Command) — Defined in `update.go` | Cobra command definition for the `update` subcommand; handles incremental wiki page updates by detecting changed files since last documented commit and triggering regeneration against current source tree |

### Setup Subcommand Detail (`setup.go`)

#### Orchestrator: `RunSetupFlow`
- Prompts interactively for LLM model ID and Ollama URL with optional context size parameter.
- Collects ignore lists via comma-separated input; merges user-supplied values against existing defaults or prior list state.
- Accepts docs directory path from stdin, falling back to an existing value on empty/failed reads.
- Persists the aggregated configuration through `config.SaveConfig`.

#### Prompt Utilities (`setup.go`)
| Function | Behavior |
|---|---|
| `promptAndAppend` | Reads comma-separated input from stdin; appends tokens to either a default slice or an existing list passed as parameter; returns merged `[]string` |
| `promptString` | Reads single-line text from stdin, trims whitespace, falls back to provided existing value when input is empty or read fails |

---

## Module: `config` — Configuration Resolution Pipeline

**Responsibility:** Centralizes all configuration sources and resolves them into a single authoritative `Config` value through a deterministic merge pipeline. Sources are layered from lowest to highest priority: system defaults → YAML file → environment variables → CLI flags. All operations are idempotent where applicable; side-effecting writes enforce restricted permissions (`0600`).

### Constants

| Constant | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Runtime key for overriding the LLM model identifier |
| `OllamaBaseUrlEnvKey` | Runtime key for overriding the Ollama server address |
| `OllamaNumCtxEnvKey` | Runtime key for overriding the context/token count |
| `LangsmithApiKeyEnvKey` | LangSmith API tracing credential key |
| `LangchainProjectEnvKey` | LangChain project name setting |
| `LangchainTracingEnvKey` | Enables/disables LangChain v2 tracing |
| `OllamaDefaultBaseURL` | Fallback Ollama address when no env/config override is present |
| `OllamaDefaultNumCtx` | Default context size of 8192 tokens applied in the absence of overrides |
| `ConfigFileName` | Filesystem name `.code-reducer.yaml` used to locate the project configuration file |

### Types & Defaults

#### `Config`
Core application configuration schema aggregating: LLM model ID, Ollama server/connection settings (base URL, context size), LangSmith/LangChain tracing flags, ignored filesystem paths and extensions, and the docs directory path. Serves as the canonical resolved state consumed downstream by the analyzer pipeline.

#### Default Exclusions
- `DefaultIgnores` — Directory name exclusions (e.g., `.git`, `node_modules`, build artifacts)
- `DefaultIgnoredExtensions` — File extension exclusions (e.g., images, binaries, lock files)

### I/O Operations (`config.go`)

| Function | Behavior |
|---|---|
| `ConfigExists(cwd string) bool` | Returns `true` when `.code-reducer.yaml` exists under the given working directory; gate for conditional config loading in the resolution pipeline |
| `LoadConfig(cwd string) (*Config, error)` | Reads and parses `.code-reducer.yaml` from `cwd` into a populated `Config` struct; returns an error on parse failure or missing file |
| `SaveConfig(cwd string, cfg *Config) error` | Serializes the provided configuration to `.code-reducer.yaml` under `cwd`; writes with restricted permissions (`0600`) to prevent unintended access by other users on shared filesystems |

### Merge Utility (`config.go`)

#### `MergeAndDeduplicate[T comparable](a, b []T) []T`
Generic slice concatenation and deduplication preserving insertion order. Applied when merging default ignore lists with user-provided overrides; preserves first-seen ordering to avoid reordering side effects in downstream path matching logic.

### Resolution Pipeline (`config.go`)

#### `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config`
Orchestrates the full configuration resolution pipeline: merges CLI flags (`modelIdFlag`, `numCtxFlag`), environment variables (via key constants above), YAML config (loaded by `LoadConfig`), and system defaults into a single resolved `Config`. Execution order enforces priority from lowest to highest: system defaults → YAML file → environment variables → CLI flags. The returned value is authoritative; downstream components read only this struct, never individual sources.

---

## Module: `engine` — AI-Powered Documentation Synthesis System

**Responsibility:** Implements an end-to-end pipeline for generating and maintaining Markdown architecture documentation from code repositories using LLM inference. Performs recursive directory traversal with SHA256-based caching, incremental change detection via metadata persistence, and LLM-driven synthesis of per-node summaries aggregated into global architecture files.

### Data Flow
Configuration → `Runner` instantiation → repository discovery → `DirNode` tree construction → recursive `synthesizeNode` (LLM extraction + cache lookup) → root summary → standard docs generation → metadata serialization via `MetadataCache`.

### LLM Client Abstraction (`client.go`)

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

| Function | Behavior |
|---|---|
| `NewLLMClient(modelID, baseURL string, contextLength int) *LLMClient` | Constructs and returns a new `*LLMClient` instance initialized with provided model identifier, API endpoint, and maximum context size |
| `CallLLM(messages []Message) (string, error)` | Sends a non-streaming request to the language model via HTTP; returns the full text response or an error upon failure |
| `StreamLLM(messages []Message, handler func(string)) error` | Initiates a streaming session; processes incoming chunks by calling a user-defined function for each content segment until the stream completes |
| `GetDefaultSystemPrompt(mode string) string` | Returns a contextualized system prompt string based on current operation mode, supporting tasks like code extraction, module synthesis, and architecture documentation |

### Caching Layer (`cache.go`)

#### `FileCacheEntry`
Stores the SHA256 hash and facts string for individual files within the cache. Enables detection of content changes without re-reading source files during update cycles.

#### `MetadataCache`
Aggregates global cache state including: last documented commit reference, file entries (path → `FileCacheEntry` mapping), and module mappings.

| Function | Behavior |
|---|---|
| `loadMetadataCache(metadataPath string) (*MetadataCache, error)` | Loads or creates an empty `MetadataCache` by reading from a `.metadata.json` file path relative to the docs directory |
| `saveMetadataCache(metadataPath string, cache *MetadataCache) error` | Serializes the provided `MetadataCache` into indented JSON and writes it to the specified metadata cache path |
| `computeSHA256(fileContent []byte) string` | Computes the SHA256 hex digest of content read from a virtual file path relative to the repository root |

### Directory Tree Representation (`tree.go`)

#### `DirNode`
Tree node struct containing: directory path, list of files (with their SHA256 hashes), and child nodes keyed by subdirectory name.

#### `FileChange`
A struct holding `Path` and `Status` fields representing a single file change operation ("Added", "Modified", or "Deleted"). Used during incremental update cycles to report detected differences.

### Synthesis Engine (`synthesize.go`)

#### `synthesizeNode(dirNode *DirNode) (string, error)`
Unexported recursive function that processes child directories and files with LLM extraction, caching results via SHA256 hashing to avoid redundant work on unchanged nodes.

### JSON Response Parsing (`json_parser.go`)

| Function | Behavior |
|---|---|
| `StripOuterMarkdownFence(input string) string` | Strips surrounding markdown or JSON code fences from input strings; returns the inner content |
| `CleanJSONResponse(raw string) (string, error)` | Removes markdown code fences, then uses brace/bracket matching to extract a complete JSON object or array from remaining text |
| `UnmarshalJSONResponse(raw string, v interface{}) error` | Cleans raw response string with `CleanJSONResponse` and deserializes it into provided target interface value |

### Pipeline Orchestrator (`orchestrator.go`)

#### `Event` Struct
Lightweight struct carrying `Type` (string) and `Message` (string), used to pass pipeline status updates back through callback interfaces.

| Function | Behavior |
|---|---|
| `GenerateStandardDocs(summary string, client *LLMClient) error` | Exported method on `*LLMClient`; generates global architecture and quickstart Markdown files from a root summary via LLM calls; writes results safely to disk |
| `RunInit(client *LLMClient) error` | Exported method executing the full Map-Reduce pipeline for repository initialization: discovers code → builds directory tree (`DirNode`) → synthesizes root summary through hierarchical merging → regenerates standard docs → updates AGENTS.md with AI agent guidelines |
| `RunUpdate(client *LLMClient, repoRoot string) error` | Exported method for incremental operation: detects file changes (added/modified/deleted) by SHA256 comparison against metadata cache → determines affected directories via tree traversal → re-synthesizes summaries only where needed → conditionally regenerates architecture or quickstart docs |

### Configuration and Runner (`runner.go`)

#### `NewRunner(cfg *Config) (*Runner, error)`
Factory constructor that instantiates and returns a new `*Runner` with provided configuration pointer. Serves as the entry point for initializing the documentation generation system.

---

## Module: `security` — Filesystem Isolation & Process Synchronization

**Responsibility:** Enforces filesystem isolation and process synchronization primitives for repository-level operations. Centralizes path validation against an absolute root boundary, manages concurrent access via atomic file-based locking (PID-pinned), and maintains version control hygiene by integrating lock artifacts into `.gitignore`. All state transitions revolve around the `repoRoot` absolute path as a constant security boundary.

### Path Validation & Traversal Protection (`security.go`)

#### `SafeResolve(repoRoot, inputPath string) (string, error)`
Resolves an arbitrary user-supplied `inputPath` against the canonical absolute `repoRoot`. Performs cleanup of the input string to neutralize relative references (`..`, single-dot), then resolves the resulting path against the repository root. Returns a fully qualified absolute path if the resolved target remains within the `repoRoot` boundary; otherwise, returns an error signaling a traversal attempt or invalidation.

**Data Flow:**
1. **Input:** `inputPath` (untrusted string) + `repoRoot` (absolute, trusted constant)
2. **Sanitization:** `inputPath` is normalized to eliminate relative components and redundant separators
3. **Resolution:** OS-level path resolution joins the sanitized input with `repoRoot`
4. **Boundary Check:** The resolved string is compared against the prefix of `repoRoot`
5. **Output:** Validated absolute path or error indicating traversal/invalidation failure

### File-Based Process Locking (`security.go`)

#### `SimpleLock` Struct
Encapsulates the state required to manage a single process-level lock:
- **LockFilePath** — Absolute filesystem location of the lockfile (typically `<repoRoot>/.code-reducer.lock`)
- **FileHandle** — OS file descriptor representing an acquired exclusive hold on the lockfile

| Function | Behavior |
|---|---|
| `AcquireLock(repoRoot string) (*SimpleLock, error)` | Establishes a new process-level lock using atomic file semantics: opens `<repoRoot>/.code-reducer.lock` with `O_EXCL` (exclusive creation); writes the current process PID (`os.Getpid()`) to the newly created file handle; returns a pointer to the resulting `*SimpleLock`, holding the open file handle for subsequent lifecycle management |
| `Unlock(lock *SimpleLock) error` | Releases the acquired lock: flushes and closes the underlying OS file handle associated with the `*SimpleLock`; removes the lockfile from disk via filesystem delete operation; returns an error if removal fails (e.g., permission denied, path not found) |

### Repository Configuration Maintenance (`security.go`)

#### `EnsureGitignoreHasLockfile(repoRoot string) error`
Ensures the repository's `.gitignore` file tracks the lock artifact to prevent accidental version control inclusion: locates `<repoRoot>/.gitignore`, checks for existing content containing `.code-reducer.lock`, and if absent, appends the entry to the file (creating it if nonexistent).

---

## Module: `tools` — Repository Verification & Safe File I/O

**Responsibility:** Provides foundational utilities for interacting with git repositories and handling filesystem operations with security-focused semantics. All functions operate within a verified repository context established by `VerifyGitRepo`.

### Repository Verification & Git Operations (`git_tools.go`)

#### `VerifyGitRepo(repoPath string) error`
Validates that: the `git` binary is present on the system PATH; the specified directory is a recognized git working tree by executing `rev-parse --is-inside-work-tree`. Returns an error if either precondition fails. Callers should invoke this before any repository-aware operation.

#### `RunGit(repoPath string, args ...string) (string, error)`
Executes arbitrary git commands scoped to the verified working tree: captures stdout and trims trailing whitespace; propagates non-zero exit codes as errors. Used for lightweight queries such as resolving relative paths, checking branch state, or retrieving repository metadata without requiring manual shell invocation by callers.

### Safe File I/O Operations (`file_tools.go`)

#### `ReadFileSafely(repoPath string) ([]byte, error)`
Resolves a virtual repository-relative path to its absolute filesystem location and returns raw byte content. Performs an error-checked open/read/close cycle; returns the complete file payload or an error if the operation cannot be completed within expected bounds.

#### `WriteFileSafely(repoPath string, content []byte) error`
Writes arbitrary content to a resolved repository path with three safety measures: **directory creation** (parent directories created if absent with appropriate mode preservation), **symlink detection** (existing symlinks at the target path detected and rejected to prevent silent redirect attacks), **TOCTOU-safe truncation** (file opened in exclusive-truncate mode `O_RDWR | O_CREATE | O_TRUNC` to eliminate race conditions between read and write phases).

#### `IsBinaryFile(path string) bool`
Scans the first 1024 bytes of a file for null byte (`\x00`) presence. Returns `true` if binary content is detected within that boundary. Used as a quick heuristic filter during code discovery to exclude non-source artifacts (images, compiled binaries, etc.) without loading entire files into memory.

### Code Discovery & Filtering Pipeline (`file_tools.go`)

#### `LoadGitignore(repoRoot string) ([]string, error)`
Reads `.gitignore` from the given repository root, strips comment lines (`#`) and blank lines, returns the active pattern set. Returns `nil` if the file does not exist or cannot be parsed. Patterns are returned as-is for downstream evaluation; callers should match them against relative paths using standard gitignore matching semantics.

#### `ShouldIgnoreFile(relPath string) bool`
Determines whether a given relative path matches any exclusion rule by evaluating: active `.gitignore` patterns from `LoadGitignore`, dot-prefixed directory components (e.g., `.build`, `.cache`), known binary file extensions, null-byte presence in the path string. Returns `true` if any exclusion rule matches, indicating the file should be skipped during discovery.

#### `DiscoverCodeFiles(repoRoot string) ([]string, error)`
Recursively walks the codebase rooted at `repoRoot`, collecting source files while skipping: build directories (e.g., `node_modules`, `.git`, `target`), paths matching any active `.gitignore` pattern from `LoadGitignore`, files flagged as binary by `IsBinaryFile`. Returns a sorted slice of relative path strings representing the discovered codebase. The walk order is deterministic; results are suitable for direct consumption by analysis or transformation pipelines.