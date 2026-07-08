# internal — Architecture Documentation

## Overview & Cross-Subsystem Data Flow

The `internal` package orchestrates four subsystems: **security** (path sanitization, file locking), **config** (YAML-backed persistence with env-var precedence), **tools** (file I/O, git operations, SHA-256 hashing), and the **engine** (BM25 ranking, XML wrapping, LLM client, JSON deserialization). Data flows linearly: callers resolve virtual paths through `security.SafeResolve`, sanitize configuration via `config.LoadAndApplyConfig`, discover source files with `tools.DiscoverCodeFiles`, rank them by BM25 relevance in the engine, wrap content in XML delimiters, and serialize requests to an Ollama-compatible LLM client. All exported functions operate on absolute paths after sanitization to prevent directory-traversal attacks and TOCTOU race conditions.

---

## Security Subsystem (`security.go`)

### Responsibility
Encapsulates filesystem integrity checks, concurrency control primitives, and version-control hygiene for repository-level lock management. Enforces safe path resolution before any I/O operation occurs and serializes access to shared resources via `flock`-based locking.

### Data Flow
1. **Input Sanitization:** External paths pass through `SafeResolve`, which resolves symlinks relative to the repository root and validates that the resolved target does not escape the intended scope, returning a canonical absolute path or an error state if traversal is detected.
2. **Concurrency Control:** Concurrent processes request access via `AcquireLock`. The function accepts an exclusive or shared intent flag. If exclusive, it atomically acquires the lock and persists the current process identifier (PID) into `.code-reducer.lock` to enable traceability of lock ownership. Shared locks are acquired without PID persistence.
3. **Persistence Hygiene:** `EnsureGitignoreHasLockfile` ensures the `.gitignore` file exists and contains an entry for `.code-reducer.lock`. If missing, it is created; if present but lacking the entry, the entry is appended.

### SafeResolve

```go
func SafeResolve(inputPath string) (string, error)
```

Performs a security-critical path resolution operation designed to mitigate path traversal attacks. Evaluates `inputPath` by resolving all symbolic links relative to the repository root directory (`rootDir`). Compares the canonical absolute path of the resolved target against the parent directory boundary of the repository root. Failure modes: **Traversal Detection** (resolved symlink points outside repository root → error), **Symlink Evaluation** (OS-level `syscall.Lstat`/`Readlink` cycle dereferences symlinks transparently before comparison).

### AcquireLock

```go
func AcquireLock(lockfile string, exclusive bool) error
```

Implements file-based locking using `syscall.Flock` (POSIX advisory lock). Manages the lifecycle of `.code-reducer.lock`. **Exclusive Mode** (`exclusive == true`): attempts to acquire an exclusive lock; if successful, writes current process PID into the lockfile via safe writer to prevent partial writes. **Shared Mode** (`exclusive == false`): acquires a shared lock without persisting state to disk. Data flow: open/create `.code-reducer.lock` → `syscall.Flock(fd, LOCK_EX | LOCK_NB)` for exclusive or `LOCK_SH | LOCK_NB` for shared (non-blocking) → on success (exclusive): write PID integer to file descriptor.

### EnsureGitignoreHasLockfile

```go
func EnsureGitignoreHasLockfile() error
```

Ensures that the lockfile is excluded from version control tracking. Operates on `.gitignore`. **Missing Ignore File:** if `.gitignore` does not exist at repository root, it is created (with appropriate permissions `0644`). **Existing Ignore File without Entry:** parses or searches for an existing entry corresponding to `.code-reducer.lock`; if absent, appends a new line containing the lockfile path. Data flow: check existence of `.gitignore` via `os.Stat` → read file content if present; write empty string (or default template) otherwise → search for substring `.code-reducer.lock` → if not found, append `\n.code-reducer.lock\n` to the file and return success.

---

## Configuration Management (`config.go`)

### Responsibility
Centralized configuration persistence abstracting three concerns: **file-backed YAML schema**, **process-environment variable propagation**, and **runtime parameter resolution** (CLI flags → environment variables → host-derived defaults). All configuration is persisted to `.code-reducer.yaml` in the working directory with `0600` permissions; an optional SQLite database (`code-reducer.sqlite`) is referenced by name only. Configuration loading follows a precedence chain: CLI flag overrides apply first, then environment variables are consulted per key, and finally host-derived values (e.g., total memory) scale defaults like Ollama context size. The `LoadAndApplyConfig` function ties the two storage backends together: it loads from disk and conditionally mirrors selected fields into the parent process's environment only when absent, avoiding clobbering user-specified env vars.

### Constants

```go
// Environment variable keys
CodeReducerModelIdEnvKey   // Model identifier for Code Reducer
OllamaBaseUrlEnvKey        // Ollama HTTP base URL
OllamaNumCtxEnvKey         // Ollama context size (token count)
LangsmithApiKeyEnvKey      // LangSmith API authentication token
LangchainProjectEnvKey     // LangChain project name
LangchainTracingEnvKey     // Enable/disable LangChain v2 tracing

// Default values
OllamaDefaultBaseURL       // Fallback HTTP endpoint when user does not set OLLAMA_BASE_URL
OllamaDefaultNumCtx        // 8192 — applied when neither flag nor env var specifies a context limit

// Filesystem identifiers
ConfigFileName             // `.code-reducer.yaml` — on-disk configuration schema name
```

### Configuration Schema (`Config`)

```go
type Config struct {
    ModelId         string   `yaml:"model_id"`
    OllamaBaseUrl   string   `yaml:"ollama_base_url"`
    OllamaNumCtx    int      `yaml:"ollama_num_ctx"`
    
    LangsmithApiKey  string `yaml:"langsmith_api_key"`
    LangchainProject string `yaml:"langchain_project_name"`
    LangchainTracing bool   `yaml:"langchain_tracing_enabled"`
    
    IgnoreList      []string `yaml:"ignore_list,omitempty"`
    DocsDir         string   `yaml:"docs_directory"`
}
```

Struct is flattened from a YAML document via standard unmarshalling; zero-valued fields are preserved as defaults during load.

### ConfigExists(dir string) bool

Performs an OS stat check on `<dir>/.code-reducer.yaml`; returns `true` if the file is present and readable. Used by callers that need to gate configuration loading or detect first-run state without error handling overhead.

### LoadConfig(dir string) (cfg Config, err error)

Reads `.code-reducer.yaml` from `dir`, parses via YAML unmarshaler, returns populated struct or an error if the file is missing/malformed. Primary entry point for deserialization.

### SaveConfig(cfg Config) error

Marshals a `Config` to YAML using standard encoder settings and writes to `<working-dir>/.code-reducer.yaml`. File permissions are explicitly set to `0600` via `os.FileMode(0600)` after write; no other process can read the file.

### LoadAndApplyConfig() error

Composite operation: 1) Calls `LoadConfig(os.Getwd())` to populate a `Config`. 2) Iterates over subset of fields that have corresponding environment keys (`ModelId`, `OllamaBaseUrl`, `OllamaNumCtx`, `LangsmithApiKey`, `LangchainProject`, `LangchainTracing`). 3) For each field, reads matching `os.Getenv(key)` value; if non-empty and the process env does not already contain it, writes the value via `os.Setenv`. Ensures disk state is authoritative while allowing users to override individual settings through environment variables without losing them on next load.

### GetOllamaContextSize(flags []string) (int, error)

Determines Ollama context size by consulting precedence chain: 1) **CLI flag** — scans provided flags for `-num-ctx` / `--num-ctx`; if found, returns that value. 2) **Environment variable** — reads `OllamaNumCtxEnvKey`; if non-empty and valid integer, returns it. 3) **Host-derived scaling** — falls back to a dynamic calculation based on the host's total memory; scales context size proportionally so larger machines receive higher token budgets without explicit user configuration. Returns resolved count or an error if no source yields a usable value.

### GetDatabasePath() (string, error)

Returns canonical SQLite database filename (`code-reducer.sqlite`) relative to working directory. Intended for callers that need only the filename; no filesystem access occurs and no error path exists.

---

## Repository Introspection Tools (`tools.go`)

### Responsibility
Supplies hardened low-level primitives for reading, writing, discovering, and hashing files within a Git repository. All exported functions operate on absolute paths after sanitization to prevent directory-traversal attacks and TOCTOU race conditions.

### Data Flow (Linear)
Callers resolve virtual paths through the filesystem layer (`file_tools.go`), capture version-control state via `git_commands` (`git_tools.go`), or compute a stable root hash (`registry.go`).

### ReadFileSafely

```go
func ReadFileSafely(virtualPath string) ([]byte, error)
```

Resolves a virtual path inside the repository, performs security validation (path prefix checks against known safe directories), then reads and returns its contents. Returns an error if the file does not exist or is outside the allowed root boundary. Data flow: `virtualPath` → path sanitization → root-boundary check → `os.ReadFile` → return contents.

### WriteFileSafely

```go
func WriteFileSafely(virtualPath string, data []byte) error
```

Resolves a virtual path, creates parent directories (`os.MkdirAll`) if absent, writes content to the file descriptor after opening, then verifies no TOCTOU symlink was created between open and write completion by re-checking the resolved target. Returns an error on any failure mode including race-condition detection. Data flow: `virtualPath` + `data` → parent directory creation → `os.OpenFile(O_CREATE|O_WRONLY)` → post-write symlink check → return success/error.

### DiscoverCodeFiles

```go
func DiscoverCodeFiles(ignorePatterns []string) ([]string, error)
```

Recursively walks the codebase to collect high-signal source files while skipping build artifacts, dependency trees (`vendor/`, `node_modules/`), caches (`.git/`, `.hg/`, `__pycache__/`), and paths matching user-supplied ignore patterns. Returns a sorted slice of absolute paths. Data flow: root directory → recursive walk (`filepath.Walk`) → filter against skip-set + pattern matcher → deduplicate → return sorted path list.

### IsBinaryFile

```go
func IsBinaryFile(path string) (bool, error)
```

Determines whether a file is binary by scanning its first 1024 bytes for null byte (`\x00`) characters. Returns `true` if any null byte is encountered within the scan window; otherwise returns `false`. Data flow: `path` → open file → read 1024 bytes → scan for `\x00` → return boolean.

### RunGit

```go
func RunGit(args ...string) ([]byte, error)
```

Executes a git command in the specified repository directory and returns its combined stdout/stderr output (or an error). Captures both streams and merges them before returning to allow full diagnostic information retrieval from a single call site. Data flow: `args` → `git exec -C repoDir args...` → capture stdout + stderr concurrently → merge → return combined bytes or error.

### GetGitHead

```go
func GetGitHead() (string, error)
```

Returns the current HEAD commit hash by delegating to `git rev-parse HEAD`. Returns an empty string and non-nil error if the repository is not in a detached state or no commits exist. Data flow: → `RunGit("rev-parse", "HEAD")` → trim whitespace → return hash string.

### CreateGitSummary

```go
func CreateGitSummary() (string, error)
```

Builds a structured multi-section string of git status, head hash, log history (last N commits), and diff evidence for use as LLM prompt context. Sections are concatenated in deterministic order to produce reproducible output across identical repository states. Data flow: → `GetGitHead()` + `RunGit("status")` + `RunGit("log", "-n10")` + `RunGit("diff")` → format each section with delimiters → return single string.

### HashRepoRoot

```go
func HashRepoRoot(path string) (string, error)
```

Computes and returns a hexadecimal-encoded SHA-256 hash of the resolved absolute file system path provided by the caller. Input is canonicalized (trailing slashes removed, symlinks followed via `filepath.EvalSymlinks`) to ensure deterministic output regardless of how the path was constructed. Data flow: `path` → `EvalSymlinks` + canonicalization → `io.ReadAll` on path bytes → SHA-256 digest → hex encoding → return 64-character hex string.

---

## Code Retrieval Engine (`engine.go`)

### Responsibility & Data Flow
Orchestrates a code-retrieval pipeline: ingests repository files, ranks them by BM25 relevance to user queries, wraps selected context in XML-delimited containers for injection safety, serializes the request into an LLM call via an Ollama-compatible client, and deserializes the resulting JSON response. Data Flow: `Query String` → `FilterFilesBM25` (rank files) → `WrapInXmlDelimiter` (sanitize content) + `AutoScaleContext` (cap context length) → `LLMClient` (serialize request via `Message`) → LLM API → `CleanJSONResponse` / `UnmarshalJSONResponse` (deserialize response).

### Core Data Structures & Configuration (`engine.go`)

#### Message

```go
type Message struct {
    Role    string
    Content string
}
```

Represents a single turn in an LLM conversation with JSON-serializable `Role` and `Content` fields. Used to construct the prompt payload sent to the model via `LLMClient`.

#### LLMClient

```go
type LLMClient struct {
    ModelID  string
    BaseURL  string
    NumCtx   int
}
```

Holds Ollama-compatible API credentials: target model identifier, base URL for the inference endpoint, and maximum context window size. Constructed via `NewLLMClient(modelID, baseURL, numCtx)`.

#### Event

```go
type Event struct {
    Type    string
    Message *Message
}
```

Carries an event type label and optional payload message. Used to route internal processing events (e.g., completion signals, error flags).

#### DirNode

```go
type DirNode struct {
    Path      string
    Files     []string
    Children  []*DirNode
}
```

Recursive directory tree node representing the repository layout: `Path` stores the absolute path, `Files` holds a list of files at this level (with relative paths), and `Children` contains nested `DirNode` slices for subdirectories. Used to represent file system structure consumed by BM25 retrieval.

### Context Processing Pipeline (`context.go`)

#### Document

```go
type Document struct {
    Content       string
    Tokens        []string
    TermFrequency map[string]int
    TokenCount    int64
}
```

Encapsulates a parsed file's content along with its token list, term frequency table (for BM25 IDF computation), and total token count. Used as the internal representation for ranking and context window management.

#### EstimateTokens

Approximates the token count from raw character length assuming a 4-character-per-token heuristic: `TokenCount = int64(len(content)) / 4`. Used to pre-estimate budget before actual tokenization, avoiding unnecessary work on large files during ranking.

#### Tokenize

Splits input text into lowercase alphanumeric tokens using regex matching (`[a-z0-9]+`). Produces the `Tokens` slice and populates `TermFrequency`. Used by BM25 scoring to compute term frequencies per document.

#### FilterFilesBM25

Ranks repository files by BM25 relevance score against a query string and returns the top-K file paths: `Score = (tf(t,d) * IDF(t)) / (dl + b * avgdl)`. Returns `[]string` of ranked file paths. Drives the retrieval step that selects which files to include in the LLM prompt.

#### WrapInXmlDelimiter

Wraps selected file content in strict XML-like tags with the file path embedded:
```xml
<file path="src/main.go">
...content...
</file>
```
Prevents prompt injection by constraining user-controlled content within deterministic delimiters and embedding provenance metadata.

#### AutoScaleContext

Caps the maximum context size passed to an LLM, returning a safe default of **8192** when input is non-positive: `if cap <= 0 { return 8192 } else { return cap }`. Applied after concatenating wrapped file contexts and user query to ensure total prompt length does not exceed the model's context window.

### LLM Response Handling (`json_parser.go`)

#### CleanJSONResponse

Extracts raw JSON content from a model response by stripping markdown code fences (`` ` ``` ``) and trimming outer delimiters:
```go
response = regexReplace(response, "```", "")
response = trim(response)
```
Handles common LLM output patterns where models wrap responses in markdown-formatted code blocks.

#### UnmarshalJSONResponse

Cleans a raw string response using `CleanJSONResponse` and deserializes the resulting JSON into a provided target interface:
```go
var result interface{}
json.Unmarshal(cleaned, &result)
return result.(T) // T = concrete type from caller
```
Type-asserts the deserialized value to the expected concrete type. Caller must supply a pointer of the correct struct; misuse panics on assertion failure.