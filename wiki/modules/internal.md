# Internal Modules Architecture

## Module Responsibility Summary & Cross-Subsystem Data Flow

The internal module tree is organized around four subsystems: **security**, **tools**, **config**, and **engine**. The data flow follows a strict initialization order—`security` primitives guard all repository-accessing operations, `tools` provides low-level filesystem/Git/hash primitives consumed by other modules, `config` resolves runtime parameters from a four-tier precedence chain (defaults → YAML file → environment variables → CLI flags), and `engine` orchestrates the full documentation-generation pipeline.

---

## Subsystem: Security (`security.go`)

### Responsibility

The `security` package enforces **path confinement**, **concurrent access control**, and **state isolation** for the repository root. Every state-modifying or resource-accessing operation is gated by these primitives to prevent privilege escalation through path traversal, TOCTOU symlink hijacking, and git-tracking of shared resources.

### Path Confinement (`SafeResolve`)

`SafeResolve(inputPath string) error` validates that an input path resolves strictly within the repository root directory. It rejects **path traversal** (e.g., `..` components) at any stage of resolution and blocks access to **external symlinks**. This function is invoked before every filesystem operation targeting resources under the repository root, ensuring no escape from the confinement boundary.

### Lock Acquisition (`AcquireLock`)

`AcquireLock(lockfilePath string, lockMode int) error` acquires either an **exclusive** or **shared** file-lock on a specified lockfile path using `flock(2)` semantics for mutual exclusion across processes. TOCTOU-safe checks prevent symlink hijacking of the lock target: before truncation, the resolved filesystem target is verified not to be a symlink. The caller determines whether shared or exclusive access is required; `AcquireLock` opens the specified path, performs the validation, and acquires the requested lock. On failure, the function returns an error without leaving any partial state.

### State Isolation (`EnsureGitignoreHasLockfile`)

`EnsureGitignoreHasLockfile(repoRoot string) error` ensures that `.gitignore` exists in the repository root and contains an entry for the lock file so it is not tracked by git. This prevents version-control systems from tracking shared or transient state, which would otherwise introduce non-deterministic behavior across clones and forks.

### Module-Level Constant

| Name | Type | Description |
|------|------|-------------|
| `LockFileName` | `string` | Canonical name of the lock file used by flock-based locking in this package. All internal callers reference this constant when constructing paths to shared or exclusive lock targets, ensuring consistent naming across the repository's lifecycle. |

---

## Subsystem: Tools (`internal/tools`)

### Responsibility Overview

The `internal/tools` package provides low-level infrastructure primitives for repository-aware code operations. It handles safe filesystem I/O, recursive source discovery with exclusion predicates, Git state queries, and deterministic path hashing. Functions are grouped by operational domain: **file system access** (read/write/discover/filter), **Git interaction**, and **content integrity verification**.

### File System Operations & Discovery

#### Safe Read / Write Primitives

**`ReadFileSafely(path string) ([]byte, error)`** resolves a virtual repository-relative path against the repository root without exposing the underlying filesystem directly. Returns raw file bytes (`[]byte`) or an error if path resolution fails (e.g., missing symlink target, permission denied) or reading encounters I/O failure.

**`WriteFileSafely(path string, content []byte) error`** writes `content` to a virtual repository-relative path with a TOCTOU-safe guard: before truncation, the resolved filesystem target is verified not to be a symlink. This prevents writing into attacker-controlled symlink targets (symlink injection / race condition).

#### Source Discovery Pipeline

**`DiscoverCodeFiles(root string) ([]string, error)`** recursively walks `root` collecting relative paths of source files. It applies exclusion predicates in sequence:
1. **Build artifacts** — detected via extension or known directories (`node_modules`, `.git`, `dist`, etc.)
2. **Binary files** — detected by `IsBinaryFile(path)` (first 1024 bytes scanned for `0x00` null bytes)
3. **Lock/config suffixes** — filtered via `NameSuffixIgnored(name)` matching `-lock.json`, `.lock.yaml`, `pnpm-lock.yaml`, etc.

#### Path Filtering Predicates

Exclusion decisions are driven by two independent predicates:

- **`ShouldIgnorePath(rel string, ignores []string) bool`** evaluates whether a relative path matches any pattern in the provided ignore slice. Supports four matching modes:
  - Exact match (`path == pattern`)
  - Prefix match (`strings.HasPrefix(path, pattern)`)
  - Component-level match (any path component equals `pattern`)
  - Glob-style wildcard (`filepath.Match`)

- **`NameSuffixIgnored(name string) bool`** returns true if the filename ends with common lock/configuration suffixes. This is a fast-path shortcut for the glob matching layer; it does not inspect directory components—only the final filename.

### Data Flow Diagram: `DiscoverCodeFiles` Pipeline

```
DiscoverCodeFiles(root) ──► recursive walk ──► relPath slice
    │                                                      │
    ▼                                                      ▼
ShouldIgnorePath(relPath, ignores)      IsBinaryFile(relPath)  NameSuffixIgnored(filename)
    │                                       │                        │
    ├── exclude (lock/config suffixes)──► binary detection ──► exclusion decision
    └── include (source file)          └── include (text file)
```

### Git Operations & Repository Validation

**`RunGit(repoDir string, args []string) (string, error)`** executes a git command with the `--no-pager` flag appended to suppress pager invocation in non-terminal contexts. Returns combined stdout+stderr as a single string or an error message if execution fails (nonzero exit code).

- **`GetGitHead(repoDir string) (string, error)`** delegates to `RunGit(repoDir, []string{"rev-parse", "HEAD"})` and strips the leading newline/whitespace from output. Returns the 40-character commit SHA or an error if HEAD is detached/unavailable.

### Repository Validation

**`VerifyGitRepo(repoDir string) bool`** performs two checks:
1. Probes for a `git` executable on PATH (returns false if unavailable).
2. Runs `git rev-parse --is-inside-work-tree` and returns true only when the target directory is inside a valid git working tree.

### Content Integrity Hashing

**`HashRepoRoot(path string) (string, error)`** computes a hex-encoded SHA-256 hash of `path`. It first attempts to resolve `path` to an absolute filesystem path via `filepath.Abs`; if resolution fails it returns the error directly. The resulting absolute path is then hashed in UTF-8 encoding and returned as lowercase hexadecimal. This function exists independently but integrates with file tools: a `DiscoverCodeFiles` pipeline can hash each retained source path for deterministic change detection or integrity verification.

---

## Subsystem: Config (`internal/config`)

### Responsibility

This module owns the application's runtime configuration lifecycle: loading defaults, merging user-provided overrides from multiple sources (CLI flags, environment variables, YAML file), persisting state, and injecting resolved values into the OS process environment for downstream components. It is a single source of truth for model identity (`CodeReducerModelIdEnvKey`), Ollama server connection parameters, tracing instrumentation options, and ignored-path/last-commit metadata stored in `.code-reducer.yaml`.

### Data Flow: Configuration Resolution Pipeline

The core operation is `ResolveConfig(repoRoot string, modelIdFlag string, numCtxFlag string) *Config`, which orchestrates a **four-tier resolution strategy**:

1. **System Defaults** — Hard-coded fallbacks (`OllamaDefaultBaseURL = "http://localhost:11434"`, `OllamaDefaultNumCtx = 8192`) serve as the base layer.
2. **YAML Config File** — If present (detected via `ConfigExists(cwd string) bool`), `LoadConfig(cwd string) (*Config, error)` unmarshals `.code-reducer.yaml` from `repoRoot`. Missing or unreadable files yield an empty struct rather than returning errors.
3. **Environment Variables** — Each tier-specific override key (`CodeReducerModelIdEnvKey`, `OllamaBaseUrlEnvKey`, `OllamaNumCtxEnvKey`, `LangsmithApiKeyEnvKey`, `LangchainProjectEnvKey`, `LangchainTracingEnvKey`) is read from `os.Getenv` and applied to the resolved config when non-empty.
4. **CLI Flags** — Explicit positional arguments (`modelIdFlag`, `numCtxFlag`) take precedence over environment variables, providing the highest-priority runtime override for model identity and context window size respectively.

The final resolved `*Config` value is returned with all tracing-related environment variables propagated to the OS process via `os.Setenv`, ensuring downstream packages (e.g., LangSmith/Tracing client initializers) observe consistent configuration without requiring explicit dependency on this module.

### Configuration Types and Schema

```go
type Config struct {
    // Model identity — overridden by CLI/env when present
    CodeReducerModelId string `yaml:"model_id"`

    // Ollama connection parameters
    OllamaBaseUrl  string `yaml:"base_url"`
    OllamaNumCtx   int    `yaml:"num_ctx"`

    // LangSmith tracing configuration
    LangsmithTracingEnabled bool   `yaml:"langsmith_tracing_enabled,omitempty"`
    LangsmithApiKey         string `yaml:"langsmith_api_key,omitempty"`

    // LangChain v2 tracing configuration
    LangchainTracingEnabled bool   `yaml:"langchain_tracing_enabled,omitempty"`
    LangchainProjectName    string `yaml:"langchain_project_name,omitempty"`

    // Operational metadata
    IgnoredPaths []string `yaml:"ignored_paths,omitempty"`
    DocDirectory  string   `yaml:"doc_directory,omitempty"`
    LastCommitSHA string   `yaml:"last_commit_sha,omitempty"`
}
```

### Persistence Operations

- **`ConfigExists(cwd string) bool`** — Returns true if `.code-reducer.yaml` exists in the given working directory via `os.Stat`, enabling conditional loading.
- **`LoadConfig(cwd string) (*Config, error)`** — Reads and unmarshals the YAML file into a `*Config`. On failure (missing/unreadable), returns an initialized empty struct with no error; callers must inspect the returned value for defaults rather than treating nil as "not loaded".
- **`SaveConfig(cwd string, cfg *Config) error`** — Marshals the configuration to YAML and writes it atomically with `os.FileMode(0600)` permissions. Marshaling or write failures are wrapped in a single error value; callers should handle the returned error uniformly without distinguishing cause unless necessary for observability.

### Environment Variable Keys (Process Propagation)

The following keys are injected into `os.Environ` during resolution when their corresponding values are non-empty:

| Key | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Runtime model identifier override |
| `OllamaBaseUrlEnvKey` | Ollama server endpoint override |
| `OllamaNumCtxEnvKey` | Context window token count override |
| `LangsmithApiKeyEnvKey` | LangSmith tracing API key injection |
| `LangchainProjectEnvKey` | LangChain v2 project name injection |
| `LangchainTracingEnvKey` | LangChain v2 tracing enable/disable flag |

This propagation pattern ensures that any process spawned from this application inherits the resolved configuration without requiring explicit re-configuration in downstream binaries.

---

## Subsystem: Engine (`internal/engine`)

### Responsibility & Data Flow

The `internal/engine` package orchestrates the full lifecycle of automated documentation generation. It bridges raw repository state (Git diffs, file metadata) with LLM inference capabilities through a four-stage pipeline: **Indexing** (BM25 ranking and context preparation), **State Tracking** (diff parsing, cache persistence, affected node detection), **Inference** (LLM client abstraction with retry logic), and **Response Handling** (JSON extraction and deserialization).

The execution flow terminates at the `Runner` orchestrator, which acquires a lock to serialize repository writes, dispatches either an initialization or update mode based on input, and commits the resulting Git HEAD SHA upon success. All operations are gated by the `security` subsystem's path confinement and locking primitives before any filesystem access occurs.

### Context & Indexing (`context.go`)

#### Data Structures

- **Document**: Encapsulates file content alongside term frequency (TF) statistics required for BM25 scoring calculations.
- **FileCacheEntry**: Stores a single cached entry's SHA256 hash and associated facts, utilized by the metadata cache to avoid redundant recomputation.

#### Functions

**`Tokenize(input string) []string`** splits an input string into lowercase alphanumeric tokens via a compiled regular expression. It provides the foundational token stream used for subsequent frequency analysis without character-level noise.

**`EstimateTokens(content string) int`** approximates LLM token counts by dividing character length by four. This heuristic avoids expensive tokenizer overhead during context budget calculations while maintaining sufficient accuracy for clamping logic.

**`WrapInXmlDelimiter(content, filePath string) string`** encapsulates provided file content in strict XML-like tags with the embedded file path. This structural wrapper mitigates prompt injection risks by isolating user-controlled data from system instructions within the LLM context window.

**`AutoScaleContext(maxSize int) int`** validates and clamps the maximum context size for an LLM operation. If the input is non-positive, it returns a safe default of 8192 characters.

**`FilterFilesBM25(query string, files []string) ([]string, error)`** ranks repository files by relevance to a query using the BM25 ranking algorithm. It accepts a query string and returns a slice of top-K file paths ordered by inverse document frequency weighted term scores.

### Repository State & Diff Engine (`engine.go`)

#### Data Structures

- **MetadataCache**: Holds the last documented commit string plus maps for files and modules loaded from `.metadata.json`. Persists state across runs to track which entities have been processed.
- **FileChange**: Represents a single path/status pair (Added, Modified, or Deleted) parsed from raw git diff output.

#### Functions

**`loadMetadataCache(repoRoot string) (*MetadataCache, error)`** deserializes the metadata cache from `.metadata.json`, returning an empty default cache on any I/O error to ensure forward compatibility with stale or corrupted state files.

**`saveMetadataCache(repoRoot string, cache *MetadataCache) error`** serializes the current `MetadataCache` instance to `.metadata.json` using indented JSON formatting for human-readable diffing during debugging.

**`computeSHA25(path string) (string, error)`** computes the SHA256 hex digest of a file's contents at a given virtual path relative to repo root, ensuring content-addressable integrity checks independent of filesystem location changes.

**`parseGitDiff(diff string) ([]FileChange, error)`** parses raw git diff output into a `[]FileChange` slice. It handles Added, Deleted, Modified, Renamed, Copied, and Type-changed entries by normalizing rename/copy operations into distinct change records for accurate state tracking.

**`isAllowedFile(path string) bool`** validates file eligibility based on ignore rules (e.g., `.git`, `node_modules`), directory filters, binary detection heuristics, and path safety resolution to prevent processing of unsafe or irrelevant paths.

**`determineAffected(repoRoot string) map[string]bool`** walks the directory tree to identify nodes marked as affected by checking changed files against cache module state and physical file existence. Returns a boolean map keyed by path.

**`propagateAffected(dirNode interface{}, repoRoot string) map[string]bool`** recursively walks a `DirNode` tree, marking parent directories as "affected" when any child reports an affected status. This ensures that structural changes (e.g., adding a new directory) invalidate all ancestor documentation entries automatically.

### LLM Interaction Layer (`engine.go`)

#### Data Structures

- **LLMClient**: Represents the abstraction over an HTTP-based chat API, holding configuration for model identity and transport parameters.

#### Functions

**`NewLLMClient(modelId, baseURL string, numCtx int) (*LLMClient, error)`** constructs a new `*LLMClient` with the provided model ID, base URL, context size, and an HTTP client configured with a 10-minute timeout to prevent hangs on slow providers.

**(LLMClient) CallLLM() (string, error)** invokes the LLM via an HTTP chat API endpoint with retry logic for transient errors (connection resets, rate limits). It returns the streamed response string accumulated from successful chunks, discarding partial responses only after exhausting the retry budget.

### Response Parsing (`json_parser.go`)

#### Functions

**`CleanJSONResponse(raw string) (string, error)`** extracts JSON content from raw LLM output by either stripping surrounding markdown code fences or locating the first opening brace/bracket and last closing brace/bracket pair in the text, returning a trimmed JSON string. This handles common model behaviors where structured output is wrapped in conversational scaffolding.

**`UnmarshalJSONResponse(raw string, target interface{}) error`** chains `CleanJSONResponse` followed by standard Go deserialization into the provided target interface value (`interface{}`). Returns an error if the cleaned payload fails to unmarshal, ensuring type-safe consumption of LLM outputs downstream.

### Pipeline Orchestration (`runner.go`)

#### Data Structures

- **Runner**: Holds a configuration pointer and orchestrates repository operations including locking, LLM client instantiation, and documentation pipeline execution. Acts as the entry point for all write operations to the documentation store.

#### Functions

**`NewRunner(cfg *Config) (*Runner, error)`** exports a constructor that initializes a new `Runner` instance with the provided configuration, establishing initial state dependencies (lock file paths, cache locations) required for subsequent execution. The runner acquires an exclusive lock on its state file to serialize concurrent invocations before dispatching to either `init` or `update` modes based on input arguments.

**`Run(repoRoot string, mode string) error`** is the primary execution method. It acquires an exclusive lock on the runner's state file to serialize concurrent invocations, dispatches to either `init` or `update` modes based on input arguments, and updates the Git HEAD SHA in the config upon successful completion. All filesystem operations within this method are pre-gated by `security.SafeResolve` and `security.AcquireLock`.