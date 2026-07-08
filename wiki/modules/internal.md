# Code Reducer: Repository Architecture & Data Flow

---

## 1. Configuration Subsystem (`internal/config`)

### Responsibility

Owns the application's runtime configuration lifecycle: loading defaults, merging user-provided overrides from multiple sources (CLI flags, environment variables, YAML file), persisting state, and injecting resolved values into the OS process environment for downstream components. Single source of truth for model identity (`CodeReducerModelIdEnvKey`), Ollama server connection parameters, tracing instrumentation options, and ignored-path/last-commit metadata stored in `.code-reducer.yaml`.

### Configuration Types & Schema

```go
type Config struct {
    // Model identity — overridden by CLI/env when present.
    CodeReducerModelId string `yaml:"model_id"`

    // Ollama connection parameters.
    OllamaBaseUrl  string `yaml:"base_url"`
    OllamaNumCtx   int    `yaml:"num_ctx"`

    // LangSmith tracing configuration.
    LangsmithTracingEnabled bool   `yaml:"langsmith_tracing_enabled,omitempty"`
    LangsmithApiKey         string `yaml:"langsmith_api_key,omitempty"`

    // LangChain v2 tracing configuration.
    LangchainTracingEnabled bool   `yaml:"langchain_tracing_enabled,omitempty"`
    LangchainProjectName    string `yaml:"langchain_project_name,omitempty"`

    // Operational metadata.
    IgnoredPaths  []string `yaml:"ignored_paths,omitempty"`
    DocDirectory  string   `yaml:"doc_directory,omitempty"`
    LastCommitSHA string   `yaml:"last_commit_sha,omitempty"`
}
```

### Persistence Operations

| Function | Responsibility |
|---|---|
| `ConfigExists(cwd string) bool` | Returns true if `.code-reducer.yaml` exists in the given working directory via `os.Stat`. Enables conditional loading. |
| `LoadConfig(cwd string) (*Config, error)` | Reads and unmarshals the YAML file into a `*Config`. On failure (missing/unreadable), returns an initialized empty struct with no error; callers must inspect the returned value for defaults rather than treating nil as "not loaded". |
| `SaveConfig(cwd string, cfg *Config) error` | Marshals the configuration to YAML and writes it atomically with `os.FileMode(0600)` permissions. Marshaling or write failures are wrapped in a single error value; callers should handle uniformly without distinguishing cause unless necessary for observability. |

### Configuration Resolution Pipeline: `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) *Config`

Orchestrates a four-tier resolution strategy with strict precedence ordering (lower tier overrides higher):

1. **System Defaults** — Hard-coded fallbacks (`OllamaDefaultBaseURL = "http://localhost:11434"`, `OllamaDefaultNumCtx = 8192`) serve as the base layer.
2. **YAML Config File** — If present (detected via `ConfigExists(cwd string) bool`), `LoadConfig(cwd string)` unmarshals `.code-reducer.yaml` from `repoRoot`. Missing or unreadable files yield an empty struct rather than returning errors.
3. **Environment Variables** — Each tier-specific override key (`CodeReducerModelIdEnvKey`, `OllamaBaseUrlEnvKey`, `OllamaNumCtxEnvKey`, `LangsmithApiKeyEnvKey`, `LangchainProjectEnvKey`, `LangchainTracingEnvKey`) is read from `os.Getenv` and applied to the resolved config when non-empty.
4. **CLI Flags** — Explicit positional arguments (`modelIdFlag`, `numCtxFlag`) take precedence over environment variables, providing the highest-priority runtime override for model identity and context window size respectively.

The final resolved `*Config` value is returned with all tracing-related environment variables propagated to the OS process via `os.Setenv`, ensuring downstream packages (e.g., LangSmith/Tracing client initializers) observe consistent configuration without requiring explicit dependency on this module.

### Environment Variable Keys (Process Propagation)

| Key | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Runtime model identifier override |
| `OllamaBaseUrlEnvKey` | Ollama server endpoint override |
| `OllamaNumCtxEnvKey` | Context window token count override |
| `LangsmithApiKeyEnvKey` | LangSmith tracing API key injection |
| `LangchainProjectEnvKey` | LangChain v2 project name injection |
| `LangchainTracingEnvKey` | LangChain v2 tracing enable/disable flag |

---

## 2. Engine Subsystem (`internal/engine`)

### Responsibility

Implements a deterministic code reduction pipeline that ranks repository files via BM25 relevance scoring, caches file fingerprints to avoid redundant reprocessing on subsequent runs, detects affected directories from parsed git diffs, and orchestrates LLM-powered code generation through a configurable chat-completion client. The runner entry point branches execution into initialization or update modes based on the supplied mode parameter.

### Data Model & BM25 Ranking (`context.go`)

#### File Content Representation — `Document`

```go
type Document struct {
    // Content holds the raw file text.
    Content string
    // TermFrequencies maps token → occurrence count for BM25 scoring.
    TermFrequencies map[string]int
    // TokenCount is the total number of tokens in the document.
    TokenCount int
}
```

#### Text Tokenization — `Tokenize`

Splits input text into lowercase alphanumeric tokens using a compiled regex tokenizer. Non-alphanumeric characters are discarded; whitespace and punctuation yield no tokens.

#### Relevance Ranking — `FilterFilesBM25`

Ranks repository files by relevance to an arbitrary query string using BM25 (Okapi). Returns the top-K file paths after computing term frequencies over each document's tokenized content. The ranking formula incorporates normalized IDF weighting against the total corpus size and a length normalization factor proportional to `avgdl`.

#### Prompt Injection Mitigation — `WrapInXmlDelimiter`

Wraps file content in strict XML-like tags with the file path attribute: `<file path="...">content</file>`. This prevents prompt injection attacks by constraining LLM input to syntactically delimited regions.

### Caching Layer (`engine.go`)

#### Cached File Entry — `FileCacheEntry`

Stores a cached file's SHA256 hash and associated facts for retrieval without re-processing:

```go
type FileCacheEntry struct {
    Hash   string      // SHA256 hex digest of the original content.
    Facts  interface{} // Arbitrary metadata keyed by filename (e.g., code reduction results).
}
```

#### Metadata Cache — `MetadataCache`

Aggregates per-file cache entries, the last documented commit hash, and module mappings into a single JSON-deserializable structure:

```go
type MetadataCache struct {
    LastDocumentedCommit string               // Commit hash of most recent successful documentation pass.
    FileCache             map[string]FileCacheEntry `json:"file_cache"`
    ModuleMappings        map[string]string         `json:"module_mappings"`
}
```

#### Cache I/O — `loadMetadataCache` / `saveMetadataCache`

- **`loadMetadataCache(repoRoot, docsDir) (*MetadataCache, error)`** reads the metadata cache from disk at `<repoRoot>/<docsDir>/metadata_cache.json`. Returns an empty default cache if the file is missing or malformed.
- **`saveMetadataCache(repoRoot, docsDir, cache *MetadataCache) error`** serializes the metadata cache to JSON and writes it safely to disk atomically (write-to-temp + rename).

#### File Fingerprinting — `computeSHA256`

Reads a file at the given path and returns its SHA256 hex digest. Used for change detection between runs.

### Change Detection & Affected Directory Propagation (`engine.go`)

#### Git Diff Parsing — `parseGitDiff`

Parses raw git diff output into structured `[]FileChange` entries, categorizing adds, deletes, renames (via rename detection logic), copies, modifications, and type changes. Each entry carries the relative file path and change classification.

#### File Filtering — `isAllowedFile`

Filters files by:
- Ignore lists (explicit path patterns)
- Directory/component name whitelists
- File extension allowlists (excludes binaries like `.exe`, `.dll`)
- Safe path resolution to prevent traversal attacks

Returns `true` if the file passes all filters.

#### Affected Region Detection — `determineAffected` / `propagateAffected`

- **`determineAffected(node *DirNode, repoRoot, docsDir string, cache *MetadataCache, filteredChanges []FileChange, affectedDirs map[string]bool)`** identifies which directories need updating by checking for changed files and verifying module existence against the metadata cache.
- **`propagateAffected(node *DirNode, affectedDirs map[string]bool) bool`** recursively marks a directory tree as affected when any descendant is marked. Returns `true` if propagation occurred.

### LLM Client (`engine.go`)

#### Message — `Message`

Represents an LLM role-content pair used in chat completions with JSON serialization support:

```go
type Message struct {
    Role   string // "system", "user", "assistant"
    Content string
}
```

#### Client Configuration — `LLMClient` / `NewLLMClient`

- **`LLMClient`** holds configuration for an LLM provider including model ID, base URL, context size limit, and HTTP client instance.
- **`NewLLMClient(modelID, baseURL string, numCtx int) *LLMClient`** initializes a new `LLMClient` with the given parameters and a 10-minute default timeout for HTTP requests.

### Response Parsing (`json_parser.go`)

#### JSON Extraction — `CleanJSONResponse`

Extracts JSON content from surrounding markdown code fences (`` ```json `` or `` ``` ``) or locates the first opening brace/bracket and last closing brace/bracket in raw text to return a trimmed JSON string. Handles malformed responses gracefully by returning an empty string if no valid JSON is found.

#### Deserialization — `UnmarshalJSONResponse`

Cleans the raw response using `CleanJSONResponse`, then unmarshals the resulting JSON into the provided target interface value (e.g., `*struct`). Returns an error if deserialization fails or the cleaned text is empty.

### Execution Orchestration (`runner.go`)

#### Runner — `Runner` / `NewRunner` / `Run`

- **`Runner`**: A struct holding a reference to the engine configuration, serving as the primary entry point for executing code reduction workflows.
- **`NewRunner(cfg *Config)`** initializes and returns a new `Runner` instance bound to a specific configuration pointer.
- **`Run(mode string) error`** executes the core workflow by:

  1. Acquiring repository locks (mutex or file-based, depending on environment).
  2. Creating an LLM client via `NewLLMClient`.
  3. Branching into initialization mode (`mode == "init"`) or update mode based on the provided mode parameter.
  4. Loading the metadata cache from disk (or creating a fresh one for init).
  5. Running git diff, filtering changes, and determining affected directories.
  6. Re-processing only affected files through BM25 ranking followed by LLM code generation.
  7. Updating the metadata cache with new SHA256 hashes and facts after completion.

---

## 3. Security Subsystem (`security.go`)

### Responsibility

Provides repository-integrity primitives that enforce **path confinement**, **concurrent access control**, and **state isolation** for the repository root. These guards are invoked before any state-modifying or resource-accessing operation to prevent privilege escalation through path traversal, TOCTOU symlink hijacking, and git-tracking of shared resources.

### Path Confinement — `SafeResolve`

Validates that a given input path resolves strictly within the repository root directory. Returns an error if **path traversal** (e.g., `..` components) or **external symlinks** are detected at any stage of resolution. Invoked before any filesystem operation that targets resources under the repository root, ensuring no escape from the confinement boundary.

| Parameter | Type | Description |
|-----------|------|-------------|
| input path | `string` | Absolute or relative path to validate against the repository root |

### Lock Acquisition — `AcquireLock`

Acquires either an **exclusive** or **shared** file-lock on a specified lockfile path, with TOCTOU-safe checks to prevent symlink hijacking of the lock target. The lock mechanism uses `flock(2)` semantics for mutual exclusion across processes.

| Parameter | Type | Description |
|-----------|------|-------------|
| lockfile path | `string` | Path to the lock file on which to acquire the lock |
| lock mode | (exclusive/shared) | Determines whether other processes may concurrently hold a lock of the same type |

**Data flow:** The caller determines whether shared or exclusive access is required; `AcquireLock` then opens the specified path, performs TOCTOU-safe validation, and acquires the requested lock. If the operation fails, the function returns an error without leaving any partial state.

### State Isolation — `EnsureGitignoreHasLockfile`

Ensures that the `.gitignore` file exists in the repository root and contains an entry for the lock file so it is not tracked by git. Prevents version-control systems from tracking shared or transient state, which would otherwise introduce non-deterministic behavior across clones and forks.

| Parameter | Type | Description |
|-----------|------|-------------|
| (implicit) repository root | `string` | Root directory of the repository; `.gitignore` is expected at this path |

### Module-Level Constant — `LockFileName` (`string`)

Stores the canonical name of the lock file used by flock-based locking in this package. All internal callers reference this constant when constructing paths to shared or exclusive lock targets, ensuring consistent naming across the repository's lifecycle.

---

## 4. Tools Subsystem (`internal/tools`)

### Responsibility and Data Flow

Provides low-level primitives for resolving repository state: filesystem reads/writes with security checks, recursive source-file discovery, git command execution, and deterministic path hashing. All functions operate on a virtualized (logical) root that maps to an absolute disk path at resolution time. The design is intentionally TOCTOU-safe where write paths are involved—`WriteFileSafely` verifies the resolved target is not a symlink before truncating.

Data flow: `HashRepoRoot` hashes a logical path string; `ReadFileSafely` / `WriteFileSafely` resolve virtual paths to absolute filesystem locations, then read/write raw bytes with error propagation on failure; `DiscoverCodeFiles` walks the root recursively and filters by exclusion rules; git operations execute in-process via `RunGit`, which returns combined stdout/stderr or an error message.

### File System Utilities (`file_tools.go`)

#### Path Resolution and Safe Read/Write

- **`ReadFileSafely`** — Resolves a virtual path through the repository root (absolute filesystem resolution), reads raw file content, and returns `[]byte` + error on resolution/read failure.
- **`WriteFileSafely`** — Writes bytes to a virtual path with TOCTOU-safe checks: verifies the resolved target is not a symlink before truncating and writing.

#### Source Discovery and Filtering

- **`DiscoverCodeFiles`** — Recursively walks the repository root, collecting relative paths of source files while excluding build artifacts, binary files, lock files, and custom ignore patterns.
- **`ShouldIgnorePath`** — Evaluates whether a given relative file path matches any pattern in the provided `ignores` slice using four matching strategies: exact match, prefix matching, component-level matching, and glob-style wildcard matching (`*`, `?`, `[...]`).
- **`IsBinaryFile`** — Opens a file and scans its first 1024 bytes for null byte (`0x00`) characters; returns true if any detected.
- **`NameSuffixIgnored`** — Returns true when the given filename ends with common lock/configuration suffixes: `-lock.json`, `.lock.yaml`, `pnpm-lock.yaml`.

### Git Operations (`git_tools.go`)

#### Command Execution and Repository Validation

- **`RunGit`** — Executes a git command in the specified repository directory with the `--no-pager` flag; returns combined output or an error message.
- **`GetGitHead`** — Retrieves the current HEAD commit hash by delegating to `RunGit`.
- **`VerifyGitRepo`** — Validates that the system has a git executable available and confirms the target directory is inside a valid git working tree.

### Path Hashing (`registry.go`)

#### Deterministic Identifier Computation — `HashRepoRoot`

Computes and returns a hex-encoded SHA-256 hash of the provided path string after attempting to resolve it to an absolute filesystem path.