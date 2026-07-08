# code-reducer — Architecture Documentation

## Module Overview

`code-reducer` is a CLI-driven repository analysis and architecture synthesis tool built in Go 1.26. The application ingests source repositories, ranks files via BM25 term-frequency scoring, constructs hierarchical directory trees, synthesizes markdown documentation through recursive chunk batching against an Ollama LLM backend, and persists results to disk. Configuration is loaded from `.code-reducer.yaml` with environment variable and CLI flag overrides; runtime concurrency is serialized via `flock`-based file locking; all filesystem operations are hardened against path traversal and symlink injection.

---

## Data Flow Architecture

```
Repository Root (string)
    │
    ▼  [security.SafeResolve] — canonical absolute path resolution
    │
    ├── internal/tools/
    │   ├── DiscoverCodeFiles(root) → []string           // source file discovery
    │   ├── ReadFileSafely(path) → []byte               // safe inbound read
    │   ├── WriteFileSafely(relPath, content) error      // safe outbound write
    │   └── IsBinaryFile(path) bool                     // null-byte binary detection
    │
    ├── internal/engine/
    │   ├── Tokenize(input string) []string              // regex-based tokenization
    │   ├── EstimateTokens(text string) int              // 4-char-per-token approximation
    │   └── FilterFilesBM25(query, files []Document) []Document // BM25 ranking
    │
    └── internal/config/
        └── LoadConfig(cwd string) (*Config, error)     // YAML → memory load

    ▼  [internal/engine/context.go] — Document struct assembly
        Raw file content + TermFrequency map[string]int
        │
        ▼  XML-wrapped context: <file>path</file><content>...</content>
        │
        ▼  [internal/engine/reduceInChunks] — recursive chunk batching
            DirNode tree → LLMClient.Chats() → markdown fence stripping
            │
            ▼  Parsed JSON responses via json_parser.go utilities
```

---

## Package: `security` — Repository Isolation & Concurrency Control

**Responsibility:** Enforces repository isolation, process-level concurrency control, and VCS hygiene. Manages three subsystems: path resolution integrity (`SafeResolve`), file-based locking (`AcquireLock`/`flock.Flock`), and lockfile exclusion from Git tracking (`EnsureGitignoreHasLockfile`).

### Constants

```go
const LockFileName = ".code-reducer.lock"
```

Absolute path for the process coordination file at repository root. Anchor point for flock acquisition and git exclusion logic.

### Path Safety

```go
func SafeResolve(repoRoot, inputPath string) (string, error)
```

Validates user-supplied paths to ensure strict containment within `repoRoot`. Resolves relative components against `repoRoot`, rejects targets that escape the root directory or reference dangling symlinks. Returns canonical absolute path on success; errors if resolution fails.

### Concurrency Control

```go
func AcquireLock(repoRoot string, exclusive bool) (*flock.Flock, error)
```

Acquires a file-based flock on `.code-reducer.lock` to serialize concurrent analyzer processes. When `exclusive=true`, obtains an exclusive (write) lock; otherwise shared (read) lock. Records current process PID within lockfile metadata for diagnostic identification of active owners. Returns `*flock.Flock` handle; errors if locking fails or file is unreadable.

### Repository Integrity

```go
func EnsureGitignoreHasLockfile(repoRoot string) error
```

Guarantees `.gitignore` exists at `repoRoot` and contains an entry for the lockfile path. Initializes file if absent; appends exclusion pattern if present but missing the specific entry. Ensures coordination file remains outside Git tracking history to prevent accidental inclusion in repositories or merge conflicts during VCS operations.

---

## Package: `internal/tools` — Low-Level Repository Operations

**Responsibility:** Provides low-level utility primitives for repository-aware operations: safe file I/O, git command execution, source-file discovery, and cryptographic hashing of resolved paths. All functions operate against an absolute repository root resolved once per operation via `filepath.Abs` before any filesystem or git interaction to prevent path-traversal vulnerabilities.

### Filesystem Operations (`file_tools.go`)

| Function | Direction | Responsibility |
|---|---|---|
| **ReadFileSafely(path)** | Inbound | Resolves absolute path via security layer; reads repository-relative file; returns byte content |
| **WriteFileSafely(relPath string, content []byte) error** | Outbound | Accepts bytes + repo-relative path; creates parent directories as needed; verifies target is not a symlink before writing; persists data to disk |
| **DiscoverCodeFiles(root string) ([]string, error)** | Inbound | Recursively walks repository root collecting relative paths of source files while excluding: build artifacts (compiled binaries, object files), binary files detected via null-byte scanning, dependency directories (`vendor`, `node_modules`), and user-configured ignore patterns. Each candidate passes through `IsBinaryFile`. Returns paths relative to `root` |
| **IsBinaryFile(path string) (bool, error)** | Inbound | Reads file and inspects first 1024 bytes for null byte presence; returns `true` if any null byte found (binary classification); O(1) disk-read cost regardless of file size |

Both `ReadFileSafely` and `WriteFileSafely` delegate to the security layer for absolute-path resolution. `WriteFileSafely` additionally enforces a symlink check on target before write operation to prevent privilege escalation through symlink injection.

### Git Operations (`git_tools.go`)

```go
func RunGit(root string, args ...string) (string, error)
```

Executes arbitrary git subcommand within specified repository root directory; returns combined stdout/stderr output. Handles both success and failure cases without panicking on non-zero exit codes. Caller responsible for interpreting return values.

```go
func GetGitHead(root string) (string, error)
```

Delegates to `RunGit` with subcommand `rev-parse HEAD`; returns current HEAD commit hash. Operates against resolved absolute path.

### Path Hashing (`registry.go`)

```go
func HashRepoRoot(path string) (string, error)
```

Returns SHA-256 hex-encoded hash of the resolved absolute path passed as argument. If `filepath.Abs` fails during resolution, falls back to returning raw input string unchanged. Used for deterministic identification of repository states across operations requiring a content-addressable key derived from root location.

---

## Package: `internal/engine` — Orchestration Layer

**Responsibility:** Manages document ingestion, BM25-based file ranking, directory tree construction, system prompt retrieval, recursive chunk batching, Ollama API client configuration, and JSON response parsing for LLM-driven code analysis workflows.

### Data Flow

```
Raw repository contents → tokenization → frequency computation → BM25 scoring → XML-wrapped context assembly → LLM invocation via `LLMClient` → markdown fence stripping → `reduceInChunks` synthesis → parsed responses
```

---

### Document Processing & Ranking (`context.go`)

#### `Document` (struct)

File content representation with pre-computed term frequency statistics for BM25 indexing:

```go
type Document struct {
    Content       string
    TermFrequency map[string]int
}
```

#### Tokenization

```go
func Tokenize(input string) []string
```

Splits raw input text into lowercase alphanumeric tokens via regex. Produces the token stream consumed by subsequent frequency calculation and BM25 scoring stages.

#### Estimate Tokens

```go
func EstimateTokens(text string) int
```

Approximates token count from character length under assumption that one token ≈ 4 characters. Used for context size budgeting before LLM invocation.

#### BM25 Ranking

```go
func FilterFilesBM25(query string, files []Document) []Document
```

Ranks repository files by relevance to a query using BM25; returns top-K results ordered by descending score. Input: raw file paths + corpus content. Output: ranked slice of `Document`.

---

### Directory Tree Construction (`engine.go`)

#### `DirNode` (struct)

Hierarchical directory tree node containing direct file paths and child subdirectory references:

```go
type DirNode struct {
    Files            []string
    Subdirectories   *DirNode
}
```

#### Build Tree

```go
func buildTree(filePaths []string) *DirNode
```

Constructs hierarchical directory tree from list of file paths rooted at `"."`. Recursively partitions paths into parent directories and leaves. Used to structure context before chunked LLM synthesis.

---

### System Prompt Handling (`engine.go`)

#### Default Prompts

```go
func GetDefaultSystemPrompt(phase string) (string, error)
```

Returns task-specific system prompts for three workflow phases: file analysis, module synthesis, and architecture tasks. Selects prompt based on current engine phase state.

#### Load System Prompt

```go
func LoadSystemPrompt() (string, error)
```

Retrieves a default system prompt with error handling; always returns successfully under normal operation. Fallback when no task-specific prompt matches.

---

### LLM Client Management (`engine.go`)

#### `LLMClient` (struct)

Holds configuration parameters required to connect to Ollama API: model ID, base URL, context size limit. Serves as transport layer singleton for all subsequent inference calls:

```go
type LLMClient struct {
    ModelID     string
    BaseURL     string
    NumCtx      int
}
```

#### New LLM Client

```go
func NewLLMClient(modelID, baseURL string, numCtx int) (*LLMClient, error)
```

Factory constructor initializing and returning new `*LLMClient` instance with provided settings. Called once per engine lifecycle to establish the client handle.

---

### Recursive Chunk Batching & Markdown Fence Stripping (`engine.go`)

#### Strip Outer Markdown Fence

```go
func stripOuterMarkdownFence(response string) string
```

Extracts content between triple-backtick markdown fences or JSON fences using regex matching. Strips surrounding markdown wrapper from raw LLM responses before further processing.

#### Reduce In Chunks

```go
func reduceInChunks(dirTree *DirNode, accumulated []byte) (string, error)
```

Synthesizes architecture documentation by recursively batching items into LLM calls with markdown fence stripping applied post-response. Input: directory tree + accumulated results. Output: synthesized markdown document.

---

### Context Safety & Auto-scaling (`context.go`)

#### Wrap in XML Delimiter

```go
func WrapInXmlDelimiter(content, filePath string) string
```

Wraps file content in XML-like delimiters tagged with file path to prevent prompt injection attacks. Each wrapped entry prefixed/suffixed with `<file>` markers for safe LLM consumption:

```xml
<file>path/to/file.go</file>
<content>
...raw bytes...
</content>
```

#### Auto Scale Context

```go
func AutoScaleContext(maximum int) int
```

Caps context size at safe default of 8192 when provided maximum is non-positive; otherwise returns input unchanged. Guards against oversized context submissions to the LLM provider.

---

## Package: `internal/config` — Configuration Management

**Responsibility & Data Flow:** Centralizes application configuration resolution. Abstracts three concerns: persistent config file I/O (`*.yaml`, 0600 mode), environment variable injection, and runtime parameter discovery (CLI flags → env vars → dynamic host resource scaling). Canonical data flow is **load → parse → apply → persist**, with explicit short-circuit semantics at each stage (silent skip on missing file for `LoadAndApplyConfig`; fixed return value for `GetDatabasePath`).

### Configuration Schema & File I/O (`config.go`)

#### Structs

- **`Config`** — schema for `.code-reducer.yaml`. Holds model, Ollama, LangSmith, and LangChain configuration fields plus optional ignore lists and docs directory path.

#### Functions

| Function | Behavior |
|---|---|
| `ConfigExists(cwd string) bool` | Returns `true` if a `.code-reducer.yaml` file exists in the specified working directory |
| `LoadConfig(cwd string) (*Config, error)` | Reads and parses `.code-reducer.yaml` from given directory into `*Config` value |
| `SaveConfig(cwd string, cfg *Config) error` | Marshals `Config` to YAML and writes it to `.code-reducer.yaml` with mode 0600 in specified directory |
| `LoadAndApplyConfig(cwd string) error` | Loads config (silently ignoring missing files), then sets corresponding environment variables only when they are not already present from the parent process |

#### Constants

- **`ConfigFileName`** — `.code-reducer.yaml`. Persisted configuration filename.

### Environment Variable Resolution (`env.go`)

Module defines explicit env keys and defaults for downstream consumers:

| Constant | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Code Reducer model identifier |
| `OllamaBaseUrlEnvKey` | Ollama base URL |
| `OllamaNumCtxEnvKey` | Ollama context size |
| `LangsmithApiKeyEnvKey` | LangSmith API key |
| `LangchainProjectEnvKey` | LangChain project name |
| `LangchainTracingEnvKey` | LangChain tracing v2 enable/disable |

**Defaults:**

- **`OllamaDefaultBaseURL`** — `http://localhost:11434`. Default Ollama base URL.
- **`OllamaDefaultNumCtx`** — 8192 tokens. Default Ollama context size.

### Database Path Resolution (`database.go`)

```go
func GetDatabasePath() (string, error)
```

Returns fixed database file path `code-reducer.sqlite` with no error. Used for deterministic location of persistent state across engine invocations.

### Ollama Context Size Resolution (`ollama.go`)

```go
func GetOllamaContextSize(flagVal string) int
```

Resolves the Ollama context size by checking a CLI flag first, then an environment variable, then dynamically scaling based on host RAM. Falls back to configured default when all sources are absent or invalid.

---

## Package: `cmd` — CLI Entry Point & Command Lifecycle

**Responsibility:** The `cmd` package implements the Cobra-based command-line interface for **code-reducer**. It manages lifecycle from application bootstrap through credential validation, interactive setup, configuration loading, repository locking, and LLM engine execution with live event streaming. Module registers subcommands at package init time and delegates orchestration to a root command that enforces sequential state transitions.

### Data Flow

```
init.go (package init) 
    ↓ registers "init" subcommand
RootCmd (root.go — Cobra root)
    ↓ executeCommand()
NeedsCredentialSetup() → bool
    ├─ false ───→ acquire lock → load config → run LLM engine + stream events
    └─ true ────→ RunSetupFlow() → save .code-reducer.yaml → re-attempt executeCommand()
```

### Subcommand Registration

#### init (init.go)

Executes at package initialization. Registers the `init` subcommand on the root Cobra command, enabling a self-healing bootstrap path where users can invoke `code-reducer init` to trigger interactive setup if credentials are absent.

### Root Command & Execution Orchestrator (`root.go`)

#### RootCmd (struct)

Top-level Cobra command for `code-reducer` CLI. Owns application's command tree and serves as entry point for all user-invoked operations. Delegates to `executeCommand()` on invocation; does not perform work directly.

#### executeCommand

Orchestrates main execution flow:

1. **Credential gating** — calls `NeedsCredentialSetup()`. If no `MODEL_ID` environment variable is set, function returns `true`, signaling that critical credentials are missing.
2. **Lock acquisition** — acquires a repository-level lock to serialize concurrent invocations and prevent state corruption during setup or engine execution.
3. **Configuration loading** — reads `.code-reducer.yaml` from working directory. If file is absent, triggers `RunSetupFlow()` instead of proceeding.
4. **LLM engine invocation** — runs the model inference pipeline with live event streaming for real-time output consumption by caller.

#### NeedsCredentialSetup (root.go)

Boolean predicate that evaluates absence of required environment variable (`MODEL_ID`). Returns `true` when credentials are unconfigured, causing `executeCommand` to redirect into setup mode rather than execution.

### Interactive Configuration Flow (`setup.go`)

```go
func RunSetupFlow() error
```

Guides user through interactive configuration session. Prompts for missing values and writes a `.code-reducer.yaml` file to disk. Upon completion, returns control to `executeCommand`, which re-acquires the lock and reloads configuration before proceeding with engine execution.