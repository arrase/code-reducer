# Internal Module Architecture Documentation

## Subsystem: `config` — Configuration Resolution and Persistence

### Module Responsibility

The `internal/config` package owns the complete lifecycle of application configuration: detection, loading, merging, resolution, and persistence. It provides a layered resolution strategy that merges system defaults → YAML file → environment variables → CLI flags into a single canonical `*Config`. All resolved values are also propagated to external tracing environment variables so downstream services observe them without separate injection logic.

### Constants

| Constant | Purpose |
|---|---|
| `ConfigFileName` | Canonical filename `.code-reducer.yaml` used for all disk read/write operations within the package. |
| `CodeReducerModelIdEnvKey` | Environment variable key that overrides the LLM model identifier at runtime. |
| `OllamaBaseUrlEnvKey` | Environment variable key that overrides the Ollama API base URL at runtime. |
| `OllamaNumCtxEnvKey` | Environment variable key that overrides the Ollama context size (window) at runtime. |
| `LangsmithApiKeyEnvKey` | Environment variable key for the LangSmith tracing API key. |
| `LangchainProjectEnvKey` | Environment variable key for the LangChain project identifier. |
| `LangchainTracingEnvKey` | Environment variable key that enables/disables LangChain v2 tracing. |
| `OllamaDefaultBaseURL` | Fallback base URL used when neither config nor environment overrides the Ollama endpoint. |
| `OllamaDefaultNumCtx` | Default context size (8192) applied when no explicit override is present in any layer. |

### Structs

#### `Config`

Schema for `.code-reducer.yaml`. Fields cover: LLM model ID, Ollama settings (base URL, context size), Langsmith/Langchain tracing configuration (API key, project, enabled flag), ignored file paths, and the docs directory path. This struct is the canonical in-memory representation of all configuration sources after resolution.

### Functions

#### `ConfigExists(dir string) bool`

Performs a filesystem check via `os.Stat` to determine whether `.code-reducer.yaml` exists within `dir`. Returns `true` only when the file is present; callers use this gate before attempting load operations to avoid silent failures.

```go
func ConfigExists(dir string) bool { ... }
```

#### `LoadConfig(dir string) (*Config, error)`

Reads `.code-reducer.yaml` from `dir`, parses it with `yaml.Unmarshal`, and returns a populated `*Config`. Returns an error if the file does not exist (caller should verify via `ConfigExists` first) or if parsing fails. This is the raw-disk loader; no merging, validation, or default injection occurs here.

```go
func LoadConfig(dir string) (*Config, error) { ... }
```

#### `SaveConfig(config *Config, dir string) error`

Serializes a `*Config` to YAML and writes it to `.code-reducer.yaml` in `dir`. The file is created with Unix mode `0600`, ensuring the configuration file remains world-unreadable on disk. This function handles both initial creation and updates; callers are responsible for providing a fully populated config.

```go
func SaveConfig(config *Config, dir string) error { ... }
```

#### `ResolveConfig(cliFlags, env vars) (*Config, error)`

Terminal resolution step that merges four precedence layers into a single canonical `*Config`:

1. **System defaults** — hardcoded fallbacks (`OllamaDefaultBaseURL`, `OllamaDefaultNumCtx`).
2. **YAML config** — loaded via `LoadConfig` from disk (if present).
3. **Environment variables** — read from the process environment using the defined env keys; overrides YAML values where applicable.
4. **CLI flags** — provided by the caller at startup; highest precedence.

After merging, the resolved struct is written back to external tracing environment variables so that downstream tracing infrastructure observes consistent configuration without separate injection calls. Returns the fully resolved `*Config` and any error encountered during resolution or serialization.

```go
func ResolveConfig(cliFlags, env vars) (*Config, error) { ... }
```

---

## Subsystem: `engine` — Hierarchical Map-Reduce Synthesis Pipeline

### Module Responsibility & Data Flow

The internal engine orchestrates a hierarchical Map-Reduce pipeline that synthesizes code repositories into structured documentation. The `Runner` coordinates all operations—lock acquisition, LLM client instantiation, and mode-based execution (init/update). On invocation, `Run(ctx, repoRoot, mode)` acquires a repository lock, instantiates an `LLMClient`, then dispatches to either the init or update pipeline based on the `mode` parameter.

The pipeline discovers code files, constructs a directory tree via `DirNode` nodes, and synthesizes modules hierarchically through `synthesizeNode`. Each node recursively processes children and files: file metadata is cached in `MetadataCache`, per-directory markdown documentation is written to disk at `.metadata.json`, and the synthesized output propagates upward. The root-level synthesis generates global architecture docs and quickstart documentation.

### Tree Construction & File Change Tracking

#### `DirNode`

Represents a hierarchical directory node containing a path identifier, an array of associated files, and a map of child nodes for tree construction.

```go
type DirNode struct { ... }
```

#### `FileChange`

Stores a file change entry consisting of a relative path string and a status field indicating whether the file was added, modified, or deleted.

```go
type FileChange struct { ... }
```

### LLM Client Abstraction

#### `Message`

Struct representing a chat message with `Role` and `Content` fields, used for LLM request/response payloads.

```go
type Message struct { ... }
```

#### `LLMClient`

Configurable client struct holding model ID, base URL, context size, and HTTP transport for Ollama API interactions.

##### `NewLLMClient`

Factory function that initializes an `LLMClient` with the provided configuration and a 10-minute timeout on the default HTTP client.

```go
func NewLLMClient(cfg *config.Config) *LLMClient { ... }
```

##### `CallLLM`

Synchronous method that sends a non-streaming chat request to the configured Ollama endpoint and returns the model's response text or an error.

```go
func (c *LLMClient) CallLLM(messages []Message) (string, error) { ... }
```

##### `StreamLLM`

Asynchronous method that streams real-time chunks from the LLM via the streaming API, invoking a user-supplied callback for each content chunk until completion.

```go
func (c *LLMClient) StreamLLM(messages []Message, fn func(string)) error { ... }
```

##### `GetDefaultSystemPrompt`

Returns a system prompt string tailored to one of three commands (`extract_file`, `module_synthesis`, `architecture`) or a default fallback, with Code-Reducer role definition embedded in all variants.

```go
func GetDefaultSystemPrompt(command string) string { ... }
```

##### `LoadSystemPrompt`

Delegates directly to `GetDefaultSystemPrompt` and returns the resulting prompt paired with a nil error for use by callers that require a typed result.

```go
func LoadSystemPrompt(command string) (string, error) { ... }
```

### JSON Response Parsing & Cleaning

#### `CleanJSONResponse`

Extracts valid JSON content from a raw response string by stripping markdown code fences and locating the first opening brace or bracket through the last closing brace or bracket.

```go
func CleanJSONResponse(raw string) (string, error) { ... }
```

#### `UnmarshalJSONResponse`

Cleans the raw JSON response string using `CleanJSONResponse` and unmarshals it into the provided target interface value, returning an error if parsing fails.

```go
func UnmarshalJSONResponse(raw string, v any) error { ... }
```

### Metadata Caching Layer

#### `FileCacheEntry`

Stores per-file cache data consisting of the file's SHA256 hash and an associated facts string.

```go
type FileCacheEntry struct { ... }
```

#### `MetadataCache`

Aggregates repository-wide metadata including the last documented commit, a map of per-file entries, and module-to-path mappings.

##### `loadMetadataCache`

Deserializes the on-disk `.metadata.json` into memory; if the file is absent or malformed it returns a freshly initialized empty `MetadataCache`.

```go
func loadMetadataCache() (*MetadataCache, error) { ... }
```

##### `saveMetadataCache`

Marshals an in-memory `MetadataCache` to formatted JSON and writes it safely to disk at `.metadata.json`.

```go
func saveMetadataCache(cache *MetadataCache) error { ... }
```

##### `computeSHA256`

Reads the contents of the given virtual path and returns its SHA256 hash as a hex-encoded string.

```go
func computeSHA256(path string) (string, error) { ... }
```

### Core Synthesis Engine

#### `Event`

Struct holding a `Type` string and `Message` string to represent status or progress events emitted during processing.

```go
type Event struct { ... }
```

##### `reduceInChunks(ctx, c, nodePath, items, logEvent) error`

Recursively chunks code file paths into batches that fit the LLM context window, then synthesizes them by calling the LLM to produce a combined description.

```go
func reduceInChunks(ctx context.Context, c *LLMClient, nodePath string, items []string, logEvent func(Event) error) error { ... }
```

##### `synthesizeNode(ctx, c, node, repoRoot, docsDir, cache, affectedDirs, logEvent) error`

Performs hierarchical tree synthesis for a directory by recursively processing children and files, caching file metadata, and writing per-directory markdown documentation.

```go
func synthesizeNode(ctx context.Context, c *LLMClient, node *DirNode, repoRoot string, docsDir string, cache *MetadataCache, affectedDirs map[string]bool, logEvent func(Event) error) error { ... }
```

### Runner & Pipeline Orchestration

#### `Runner`

Coordinates repository operations including lock acquisition, gitignore management, LLM client instantiation, and mode-based execution of init or update pipelines.

##### `NewRunner(cfg *config.Config) *Runner`

Factory function that initializes a new `Runner` instance with the provided configuration.

```go
func NewRunner(cfg *config.Config) *Runner { ... }
```

##### `Run(ctx context.Context, repoRoot string, mode string, onEvent func(Event)) error`

Main entry point that ensures lockfile is gitignored, acquires a repository lock, instantiates an `LLMClient`, and executes either the init or update pipeline based on the `mode` parameter.

```go
func (r *Runner) Run(ctx context.Context, repoRoot string, mode string, onEvent func(Event)) error { ... }
```

##### `RunInit(ctx, repoRoot, cfg, onEvent) error`

Orchestrates the full Map-Reduce pipeline: discovers code files, builds a directory tree, synthesizes all modules hierarchically, generates global architecture docs, and writes quickstart documentation.

```go
func RunInit(ctx context.Context, repoRoot string, cfg *config.Config, onEvent func(Event)) error { ... }
```

---

## Subsystem: `security` — Path Isolation & Process Locking

### Module Responsibility & Data Flow

This module enforces filesystem isolation and concurrent access control for the code reduction process. Its primary responsibility is twofold: bounding all user-supplied or derived paths strictly within the repository root to prevent path traversal, and managing exclusive process locks via a persistent file-based mechanism to serialize stateful operations (e.g., parallel reductions) and ensure lock state hygiene relative to version control tracking.

**Data Flow:**
1. **Input Sanitization:** User input is passed through `SafeResolve`, which canonicalizes the path and verifies it resolves strictly within the repository root (`repo.Root`). Any deviation triggers an immediate error return, preventing directory traversal.
2. **Lock Lifecycle:** Concurrent processes attempt to acquire a lock via `AcquireLock`. This writes the current process PID into `.code-reducer.lock` atomically. If acquisition fails (file exists or I/O error), the caller is returned with an error. Subsequent operations require holding this lock (`*SimpleLock`). Upon completion, `Unlock()` closes the underlying file handle and removes the lockfile from disk to prevent stale state accumulation.
3. **Gitignore Enforcement:** To ensure the lockfile does not enter version control history or clutter diffs, `EnsureGitignoreHasLockfile` verifies `.gitignore` exists, appends the lockfile path if missing, and ensures directory structure integrity for the ignore file itself.

### Path Sanitization: `SafeResolve`

**Responsibility:** Cleans an input string representing a filesystem path and validates it resolves strictly inside the given repository root (`repo.Root`). Returns an error immediately upon detecting any traversal attempt (e.g., `../../`, symlink escapes, or absolute paths outside the root).

```go
func SafeResolve(input string) (string, error) { ... }
```

**Behavior:**
*   Resolves the canonical absolute path of `input`.
*   Compares against the canonical absolute path of `repo.Root`.
*   If `resolved != rootPrefix`, returns a traversal error.
*   Returns the cleaned absolute path on success.

### Process Locking: `SimpleLock`, `AcquireLock`, `Unlock`

**Responsibility:** Provides a file-based process lock mechanism using `.code-reducer.lock`. Stores the PID of the holding process in the lockfile to identify the owner for potential manual cleanup or debugging. Ensures mutual exclusion during parallel code reduction runs.

#### Lock Acquisition (`AcquireLock`)
*   Attempts to create an exclusive lockfile containing the current process PID.
*   Returns an error if another process already holds the lock (non-zero file size on creation) or if file system operations fail.

```go
func AcquireLock() (*SimpleLock, error) { ... }
```

#### Lock Representation (`*SimpleLock`)

The `SimpleLock` struct wraps the underlying file handle and associated state for a held process lock.

```go
type SimpleLock struct { ... }
```

#### Lock Release (`Unlock`)
*   Releases the lock by closing its underlying file handle to flush buffered I/O.
*   Removes the `.code-reducer.lock` file from disk via `os.Remove`.
*   Returns an error if removal fails (e.g., permission denied or OS-level constraint).

```go
func (l *SimpleLock) Unlock() error { ... }
```

### Configuration Hygiene: `EnsureGitignoreHasLockfile`

**Responsibility:** Ensures `.code-reducer.lock` is listed in `.gitignore`, preventing accidental version control tracking of the runtime lock state. Creates the ignore file or appends the entry as needed, handling missing parent directories for the gitignore path itself.

```go
func EnsureGitignoreHasLockfile() error { ... }
```

**Behavior:**
1.  Checks if `.gitignore` exists at the repository root.
2.  If missing, creates an empty `.gitignore`.
3.  Reads existing content and appends `*.code-reducer.lock` (or exact path) only if not present to avoid duplicate entries.
4.  Returns error on I/O failure during creation or append operations.

---

## Subsystem: `tools` — Repository Utilities Module

### Overview

This module provides deterministic, safety-hardened utilities for repository introspection: safe file I/O with TOCTOU protection against symlink races, recursive source-file discovery that filters build artifacts/dependencies/cache/binary files, git command execution wrappers, and filesystem-root SHA-256 hashing. All exported functions accept a virtual path or repository root as input; internal helpers handle path normalization, pattern matching, and binary detection before the primary operation executes.

### Safe File I/O (`file_tools.go`)

#### `ReadFileSafely`

Resolves a virtual (relative) path to an absolute filesystem location under sandboxed conditions and reads its content into memory. Returns `nil` on any failure — no partial results are exposed. The resolver prevents traversal beyond the repository boundary; the reader returns zero-length or empty bytes only when the target is absent rather than an error value, allowing callers a uniform success path for "file not present" scenarios.

```go
func ReadFileSafely(path string) ([]byte, error) { ... }
```

#### `WriteFileSally`

Writes content to the resolved absolute path with TOCTOU-safe checks: before opening the file descriptor, it verifies that no symlink race can intercept the write by checking that the target inode is unchanged between resolution and open. If the destination is a symlink, truncation of the followed target is prevented — the function detects the symlink state pre-write and refuses to truncate the underlying real file.

```go
func WriteFileSally(path string, content []byte) error { ... }
```

**Data Flow:**
```
virtual_path → resolve_absolute → TOCTOU check (symlink race guard) → open O_RDWR|O_CREATE|O_TRUNC → write content → close
```

### File Discovery (`file_tools.go`)

#### `DiscoverCodeFiles`

Recursively walks the repository root directory tree to collect relative paths of source files. Filters exclude: build artifacts, dependency directories, cache directories, binary files, and user-supplied ignore patterns. Returns a sorted slice of cleaned relative paths for deterministic iteration across platforms.

```go
func DiscoverCodeFiles(root string) ([]string, error) { ... }
```

#### `ShouldIgnorePath` (internal)

Determines whether a cleaned relative path matches any entry in the provided ignore list using four match modes: exact string equality, prefix matching, component-level substring matching (any single path separator-delimited segment), and glob pattern matching (`filepath.Match`). Returns `true` on first match.

#### `IsBinaryFile` (internal)

Reads up to 1024 bytes from a file handle; returns `true` if any byte in the prefix contains a null byte (`\x00`), indicating a binary rather than text source file. Used as an exclusion predicate during discovery walks.

#### `NameSuffixIgnored` (internal)

Returns `true` when the final path component ends with known lockfile suffixes: `.lock.json`, `.lock.yaml`, `pnpm-lock.yaml`. Prevents lockfile entries from appearing in discovered code sets.

### Git Integration (`git_tools.go`)

#### `RunGit`

Executes a specified git command (as an argument slice) in the provided repository root with `os/exec.CommandContext`-style error propagation. Returns combined stdout/stderr as a single string and any non-zero exit status mapped to an `error`. The process is spawned with inherited environment variables; working directory is explicitly set to the supplied root, preventing drift from parent processes.

```go
func RunGit(args []string) (string, error) { ... }
```

#### `GetGitHead`

Returns the current HEAD commit hash for the repository rooted at the provided directory by delegating to `RunGit` with arguments `[git rev-parse HEAD]`. Returns an empty string on error rather than a partial hash.

```go
func GetGitHead() (string, error) { ... }
```

#### `VerifyGitRepo`

Confirms two preconditions: (1) that the git executable exists on the system path, and (2) that the supplied directory is inside a valid git working tree (`git rev-parse --is-inside-work-tree`). Returns an error describing which precondition failed if either check does not pass. Used as an admission gate before invoking other git-dependent operations.

```go
func VerifyGitRepo() error { ... }
```

### Content Hashing (`registry.go`)

#### `HashRepoRoot` (exported)

Computes a SHA-256 hash of the resolved absolute filesystem path of the repository root and returns its hexadecimal representation as a string. The hash is deterministic for a given commit state — identical file contents produce identical hashes regardless of walk order, symlink state, or metadata differences. Used to fingerprint repository snapshots without requiring full tree traversal.

```go
func HashRepoRoot() (string, error) { ... }
```