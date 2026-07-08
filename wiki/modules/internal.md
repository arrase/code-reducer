# `internal/engine` — Orchestration Layer

**Responsibility:** Manages document ingestion, BM25-based file ranking, directory tree construction, system prompt retrieval, recursive chunk batching, Ollama API client configuration, and JSON response parsing for LLM-driven code analysis workflows.

**Data Flow:** Raw repository contents → tokenization → frequency computation → BM25 scoring → XML-wrapped context assembly → LLM invocation via `LLMClient` → markdown fence stripping → `reduceInChunks` synthesis → parsed responses.

---

## Document Processing & Ranking (`context.go`)

### `Document` (struct)
File content representation with pre-computed term frequency statistics for BM25 indexing:

```go
type Document struct {
    Content       string
    TermFrequency map[string]int
}
```

### `Tokenize(input string) []string`
Splits raw input text into lowercase alphanumeric tokens via regex. Produces the token stream consumed by subsequent frequency calculation and BM25 scoring stages.

### `EstimateTokens(text string) int`
Approximates token count from character length under the assumption that one token ≈ 4 characters. Used for context size budgeting before LLM invocation.

### `FilterFilesBM25(query string, files []Document) []Document`
Ranks repository files by relevance to a query using BM25; returns top-K results ordered by descending score. Input: raw file paths + corpus content. Output: ranked slice of `Document`.

---

## Directory Tree Construction (`engine.go`)

### `DirNode` (struct)
Hierarchical directory tree node containing direct file paths and child subdirectory references:

```go
type DirNode struct {
    Files            []string
    Subdirectories   *DirNode
}
```

### `buildTree(filePaths []string) *DirNode`
Constructs a hierarchical directory tree from a list of file paths rooted at `"."`. Recursively partitions paths into parent directories and leaves. Used to structure context before chunked LLM synthesis.

---

## System Prompt Handling (`engine.go`)

### `GetDefaultSystemPrompt(phase string) (string, error)`
Returns task-specific system prompts for three workflow phases: file analysis, module synthesis, and architecture tasks. Selects the prompt based on current engine phase state.

### `LoadSystemPrompt() (string, error)`
Retrieves a default system prompt with error handling; always returns successfully under normal operation. Fallback when no task-specific prompt matches.

---

## LLM Client Management (`engine.go`)

### `LLMClient` (struct)
Holds configuration parameters required to connect to the Ollama API: model ID, base URL, context size limit. Serves as the transport layer singleton for all subsequent inference calls.

```go
type LLMClient struct {
    ModelID     string
    BaseURL     string
    NumCtx      int
}
```

### `NewLLMClient(modelID, baseURL string, numCtx int) (*LLMClient, error)`
Factory constructor initializing and returning a new `*LLMClient` instance with provided settings. Called once per engine lifecycle to establish the client handle.

---

## Recursive Chunk Batching & Markdown Fence Stripping (`engine.go`)

### `stripOuterMarkdownFence(response string) string`
Extracts content between triple-backtick markdown fences or JSON fences using regex matching. Strips surrounding markdown wrapper from raw LLM responses before further processing.

### `reduceInChunks(dirTree *DirNode, accumulated []byte) (string, error)`
Synthesizes architecture documentation by recursively batching items into LLM calls with markdown fence stripping applied post-response. Input: directory tree + accumulated results. Output: synthesized markdown document.

---

## Context Safety & Auto-scaling (`context.go`)

### `WrapInXmlDelimiter(content, filePath string) string`
Wraps file content in XML-like delimiters tagged with the file path to prevent prompt injection attacks. Each wrapped entry is prefixed/suffixed with `<file>` markers for safe LLM consumption:

```go
// <file>path/to/file.go</file>\n<content>\n...raw bytes...\n</content>
```

### `AutoScaleContext(maximum int) int`
Caps context size at a safe default of 8192 when the provided maximum is non-positive; otherwise returns input unchanged. Guards against oversized context submissions to the LLM provider.

---

## Response Parsing Utilities (`json_parser.go`)

### `CleanJSONResponse(response string) string`
Extracts JSON content from markdown code fences, optionally trimming surrounding whitespace and text before or after the first opening brace/bracket to last closing brace/bracket. Handles raw Ollama responses that may embed JSON within markdown blocks:

```go
// Input:  ```json\n{"key":"value"}\n```
// Output: {"key":"value"}
```

### `UnmarshalJSONResponse(rawResponse string, target interface{}) error`
Pipelines response cleanup through `CleanJSONResponse`, then unmarshals the resulting JSON string into the provided `target` value via `encoding/json.Unmarshal`. Used to deserialize LLM-returned structured data into typed Go objects.

---

# `internal/tools` — Low-level Repository Operations

**Responsibility:** Provides low-level utility primitives for repository-aware operations: safe file I/O, git command execution, source-file discovery, and cryptographic hashing of resolved paths. All functions operate against an absolute repository root that is resolved once per operation to prevent path-traversal vulnerabilities. Data flows consistently from a caller-supplied string through `filepath.Abs` resolution into the security layer before any filesystem or git interaction occurs.

---

## Filesystem Operations (`file_tools.go`)

### Safe Read / Write Interface

| Function | Direction | Responsibility |
|---|---|---|
| **ReadFileSafely(path)** | Inbound | Resolves an absolute path via the security layer, then reads the repository-relative file and returns its byte content. |
| **WriteFileSafely(relPath string, content []byte) error** | Outbound | Accepts bytes + a repository-relative path, creates parent directories as needed, verifies the target is not a symlink before writing, and persists the data. |

Both functions delegate to the security layer for absolute-path resolution. `WriteFileSafely` additionally enforces a symlink check on the target before any write operation to prevent privilege escalation through symlink injection.

### Source-File Discovery (`DiscoverCodeFiles(root string) ([]string, error)`)
Recursively walks the repository root, collecting relative paths of source files while excluding: build artifacts (compiled binaries, object files), binary files detected via null-byte scanning, dependency directories (vendor, node_modules, etc.), and user-configured ignore patterns. The walk is bounded to the repository root. Each discovered candidate passes through `IsBinaryFile` before inclusion. Returns paths relative to `root`.

### Binary Detection (`IsBinaryFile(path string) (bool, error)`)
Reads a file and inspects its first 1024 bytes for null byte presence. If any null byte is found, returns `true`, classifying the file as binary rather than text source material. O(1) disk-read cost regardless of file size.

---

## Git Operations (`git_tools.go`)

### Generic Git Execution (`RunGit(root string, args ...string) (string, error)`)
Executes an arbitrary git subcommand within a specified repository root directory and returns the combined stdout/stderr output. Handles both success and failure cases without panicking on non-zero exit codes. The caller is responsible for interpreting return values.

### `GetGitHead(root string) (string, error)`
Delegates to `RunGit` with the subcommand `rev-parse HEAD`, returning the current HEAD commit hash. Operates against a resolved absolute path.

---

## Path Hashing (`registry.go`)

### `HashRepoRoot(path string) (string, error)`
Returns a SHA-256 hex-encoded hash of the resolved absolute path passed as argument. If `filepath.Abs` fails during resolution, falls back to returning the raw input string unchanged. Used for deterministic identification of repository states across operations that require a content-addressable key derived from the root location.

---

# `security` — Repository Isolation & Concurrency Control

**Responsibility:** Enforces repository isolation, process-level concurrency control, and VCS hygiene for the code-reducer runtime environment. Manages three critical subsystems: path resolution integrity, file-based locking for concurrent analyzer invocations, and lockfile exclusion from Git tracking. Data flow proceeds as follows: user-supplied paths are sanitized via **path safety** primitives before any filesystem operation; concurrent access is governed by a `flock.Flock` handle acquired with PID registration to prevent overlapping reduction runs; finally, repository state is preserved by ensuring the lockfile remains untracked in `.gitignore`.

---

## Constants

### `LockFileName`
Defines the absolute path for the process coordination file stored at the repository root:

```go
const LockFileName = ".code-reducer.lock"
```

Serves as the anchor point for both flock acquisition and git exclusion logic.

---

## Path Safety (`SafeResolve(repoRoot, inputPath string) (string, error)`)
Validates and sanitizes user-supplied paths to ensure they resolve strictly within the repository boundary. Prevents symlink attacks and path traversal attempts by resolving relative components against `repoRoot` and rejecting any target that escapes the root directory or references dangling symlinks. Returns the canonical absolute path within the repo on success, or an error if resolution fails.

---

## Concurrency Control (`AcquireLock(repoRoot string, exclusive bool) (*flock.Flock, error)`)
Acquires a file-based flock on `.code-reducer.lock` to serialize access across concurrent analyzer processes. When `exclusive` is true, obtains an exclusive (write) lock; otherwise acquires a shared (read) lock. Records the current process PID within the lockfile metadata to identify active owners for diagnostic purposes. Returns a pointer to the acquired `*flock.Flock` handle for subsequent release operations and errors if locking fails or the file is unreadable.

---

## Repository Integrity (`EnsureGitignoreHasLockfile(repoRoot string) error`)
Guarantees that the repository's `.gitignore` file exists at `repoRoot` and contains an entry for the lockfile path defined by `LockFileName`. If the file is absent, it is initialized; if present but missing the specific exclusion pattern, the entry is appended. Ensures the coordination file remains outside Git tracking history to prevent accidental inclusion in repositories or merge conflicts during VCS operations.

---

# `internal/config` — Configuration Management

**Responsibility & Data Flow:** Centralizes application configuration resolution. Abstracts three concerns: persistent config file I/O (`*.yaml`, 0600 mode), environment variable injection, and runtime parameter discovery (CLI flags → env vars → dynamic host resource scaling). Canonical data flow is **load → parse → apply → persist**, with explicit short-circuit semantics at each stage (silent skip on missing file for `LoadAndApplyConfig`; fixed return value for `GetDatabasePath`).

---

## Configuration Schema & File I/O (`config.go`)

### Structs

- **`Config`** — schema for `.code-reducer.yaml`. Holds model, Ollama, LangSmith, and LangChain configuration fields plus optional ignore lists and docs directory path.

### Functions

| Function | Behavior |
|---|---|
| `ConfigExists(cwd string) bool` | Returns `true` if a `.code-reducer.yaml` file exists in the specified working directory. |
| `LoadConfig(cwd string) (*Config, error)` | Reads and parses `.code-reducer.yaml` from the given directory into a `*Config` value. |
| `SaveConfig(cwd string, cfg *Config) error` | Marshals the `Config` to YAML and writes it to `.code-reducer.yaml` with mode 0600 in the specified directory. |
| `LoadAndApplyConfig(cwd string) error` | Loads config (silently ignoring missing files), then sets corresponding environment variables only when they are not already present from the parent process. |

### Constants

- **`ConfigFileName`** — `.code-reducer.yaml`. Persisted configuration filename.

---

## Environment Variable Resolution (`env.go`)

The module defines explicit env keys and defaults for downstream consumers:

| Constant | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Code Reducer model identifier |
| `OllamaBaseUrlEnvKey` | Ollama base URL |
| `OllamaNumCtxEnvKey` | Ollama context size |
| `LangsmithApiKeyEnvKey` | LangSmith API key |
| `LangchainProjectEnvKey` | LangChain project name |
| `LangchainTracingEnvKey` | LangChain tracing v2 enable/disable |

### Defaults

- **`OllamaDefaultBaseURL`** — `http://localhost:11434`. Default Ollama base URL.
- **`OllamaDefaultNumCtx`** — 8192 tokens. Default Ollama context size.

---

## Database Path Resolution (`database.go`)

### `GetDatabasePath() (string, error)`
Returns the fixed database file path `code-reducer.sqlite` with no error. Used for deterministic location of persistent state across engine invocations.

---

## Ollama Context Size Resolution (`ollama.go`)

### `GetOllamaContextSize(flagVal string) int`
Resolves the Ollama context size by checking a CLI flag first, then an environment variable, then dynamically scaling based on host RAM. Falls back to the configured default when all sources are absent or invalid.