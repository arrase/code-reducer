# Code-Reducer Architecture Documentation

## Module Overview & Data Flow

The **Code-Reducer** module implements an interactive CLI tool for generating structured technical documentation from code repositories using large-language-model inference via Ollama-compatible endpoints. The application follows a linear pipeline: CLI command registration → configuration persistence (with env-var precedence) → security-hardened file I/O → BM25 relevance ranking of source files → XML-delimited content wrapping → LLM API serialization → JSON response deserialization.

**Data Flow Architecture:**
```
User Input ──→ RootCmd ──→ Config Load/Apply ──→ Security.SafeResolve
                                                    ↓
Repository Files ──→ DiscoverCodeFiles ──→ FilterFilesBM25 (BM25 Ranking)
                                                         ↓
Selected Content ──→ WrapInXmlDelimiter ──→ AutoScaleContext ──→ LLMClient
                                                                 ↓
API Request ──→ Message Serialization ──→ Ollama Endpoint ──→ JSON Response
                                                                      ↓
Response Handling ──→ CleanJSONResponse ──→ UnmarshalJSONResponse ──→ Output
```

All exported functions operate on absolute paths after sanitization to prevent directory-traversal attacks and TOCTOU race conditions. Configuration is persisted to `.code-reducer.yaml` with `0600` permissions; concurrent access is serialized via `flock`-based locking in `.code-reducer.lock`.

---

## CLI Command Layer (`cmd`)

### Responsibility
Implements the Cobra command hierarchy for Code-Reducer's CLI application. Defines the root entry point (`RootCmd`) and registers three subcommands—**init**, **setup**, **update**—each responsible for a distinct phase of the tool lifecycle: initial registration, interactive configuration generation, and runtime updates respectively. Package-level `init()` functions wire all subcommands into the command tree during import.

### Root Command Registration

```go
func init() { /* registers "init" subcommand with RootCmd */ } // cmd/init.go
func init() { /* registers "update" subcommand with RootCmd */ } // cmd/update.go
```

Both `init()` functions execute during package initialization, registering their respective cobra commands under `RootCmd` before any user invocation. The command tree is populated automatically on import.

### Setup Flow Orchestration

#### RunSetupFlow

**Function Signature:**
```go
func RunSetupFlow(repoRoot string) { /* orchestrates interactive CLI setup */ } // cmd/setup.go
```

Orchestrates an interactive configuration flow that prompts sequentially for:

1. **LLM model ID** — identifies the target large-language-model endpoint.
2. **Ollama connection parameters** — host, port, and any TLS flags required to reach the local Ollama server.
3. **Context size** — token budget passed to the model during inference.
4. **Ignored paths** — glob patterns for directories/files excluded from analysis.
5. **Documentation directory** — location where generated documentation is written.

The resulting configuration is persisted as `.code-reducer.yaml` at `repoRoot`. This step is a prerequisite when `NeedsCredentialSetup()` returns `true`.

#### NeedsCredentialSetup

**Function Signature:**
```go
func NeedsCredentialSetup() bool
```

Inspects the current process environment for critical credential variables (e.g., `OLLAMA_HOST`, `LLM_MODEL_ID`). Returns `true` when one or more are absent, signaling that `setup` must be invoked before any authenticated operation.

### Update Subcommand Registration

**Function Signature:**
```go
func init() { /* registers "update" subcommand with RootCmd */ } // cmd/update.go
```

Registers the `update` subcommand under `RootCmd` during package initialization. The cobra command is bound to its run function and becomes available at the top level alongside `init` and `setup`.

---

## Security Subsystem (`security`)

### Responsibility
Encapsulates filesystem integrity checks, concurrency control primitives, and version-control hygiene for repository-level lock management. Enforces safe path resolution before any I/O operation occurs and serializes access to shared resources via `flock`-based locking.

### SafeResolve

**Function Signature:**
```go
func SafeResolve(inputPath string) (string, error)
```

Performs a security-critical path resolution operation designed to mitigate path traversal attacks. Evaluates `inputPath` by resolving all symbolic links relative to the repository root directory (`rootDir`). Compares the canonical absolute path of the resolved target against the parent directory boundary of the repository root.

**Failure Modes:**
- **Traversal Detection**: Resolved symlink points outside repository root → error returned.
- **Symlink Evaluation**: OS-level `syscall.Lstat`/`Readlink` cycle dereferences symlinks transparently before comparison.

Returns a canonical absolute path or an error state if traversal is detected.

### AcquireLock

**Function Signature:**
```go
func AcquireLock(lockfile string, exclusive bool) error
```

Implements file-based locking using `syscall.Flock` (POSIX advisory lock). Manages the lifecycle of `.code-reducer.lock`.

**Exclusive Mode (`exclusive == true`):**
- Attempts to acquire an exclusive lock.
- If successful, writes current process PID into the lockfile via safe writer to prevent partial writes.

**Shared Mode (`exclusive == false`):**
- Acquires a shared lock without persisting state to disk.

**Data Flow:**
```
Open/Create `.code-reducer.lock` → syscall.Flock(fd, LOCK_EX | LOCK_NB) for exclusive or LOCK_SH | LOCK_NB for shared (non-blocking) → on success (exclusive): write PID integer to file descriptor
```

### EnsureGitignoreHasLockfile

**Function Signature:**
```go
func EnsureGitignoreHasLockfile() error
```

Ensures that the lockfile is excluded from version control tracking. Operates on `.gitignore`.

**Failure Modes:**
- **Missing Ignore File**: If `.gitignore` does not exist at repository root, it is created (with appropriate permissions `0644`).
- **Existing Ignore File without Entry**: Parses or searches for an existing entry corresponding to `.code-reducer.lock`; if absent, appends a new line containing the lockfile path.

**Data Flow:**
```
Check existence of `.gitignore` via os.Stat → read file content if present; write empty string (or default template) otherwise → search for substring `.code-reducer.lock` → if not found, append `\n.code-reducer.lock\n` to the file and return success.
```

---

## Configuration Management (`config`)

### Responsibility
Centralized configuration persistence abstracting three concerns: **file-backed YAML schema**, **process-environment variable propagation**, and **runtime parameter resolution** (CLI flags → environment variables → host-derived defaults). All configuration is persisted to `.code-reducer.yaml` in the working directory with `0600` permissions; an optional SQLite database (`code-reducer.sqlite`) is referenced by name only.

Configuration loading follows a precedence chain: CLI flag overrides apply first, then environment variables are consulted per key, and finally host-derived values (e.g., total memory) scale defaults like Ollama context size. The `LoadAndApplyConfig` function ties the two storage backends together: it loads from disk and conditionally mirrors selected fields into the parent process's environment only when absent, avoiding clobbering user-specified env vars.

### Constants

**Environment Variable Keys:**
```go
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

**Struct Definition:**
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

### Configuration Operations

#### ConfigExists

**Function Signature:**
```go
func ConfigExists(dir string) bool
```

Performs an OS stat check on `<dir>/.code-reducer.yaml`; returns `true` if the file is present and readable. Used by callers that need to gate configuration loading or detect first-run state without error handling overhead.

#### LoadConfig

**Function Signature:**
```go
func LoadConfig(dir string) (cfg Config, err error)
```

Reads `.code-reducer.yaml` from `dir`, parses via YAML unmarshaler, returns populated struct or an error if the file is missing/malformed. Primary entry point for deserialization.

#### SaveConfig

**Function Signature:**
```go
func SaveConfig(cfg Config) error
```

Marshals a `Config` to YAML using standard encoder settings and writes to `<working-dir>/.code-reducer.yaml`. File permissions are explicitly set to `0600` via `os.FileMode(0600)` after write; no other process can read the file.

#### LoadAndApplyConfig

**Function Signature:**
```go
func LoadAndApplyConfig() error
```

Composite operation: 1) Calls `LoadConfig(os.Getwd())` to populate a `Config`. 2) Iterates over subset of fields that have corresponding environment keys (`ModelId`, `OllamaBaseUrl`, `OllamaNumCtx`, `LangsmithApiKey`, `LangchainProject`, `LangchainTracing`). 3) For each field, reads matching `os.Getenv(key)` value; if non-empty and the process env does not already contain it, writes the value via `os.Setenv`. Ensures disk state is authoritative while allowing users to override individual settings through environment variables without losing them on next load.

#### GetOllamaContextSize

**Function Signature:**
```go
func GetOllamaContextSize(flags []string) (int, error)
```

Determines Ollama context size by consulting precedence chain: 1) **CLI flag** — scans provided flags for `-num-ctx` / `--num-ctx`; if found, returns that value. 2) **Environment variable** — reads `OllamaNumCtxEnvKey`; if non-empty and valid integer, returns it. 3) **Host-derived scaling** — falls back to a dynamic calculation based on the host's total memory; scales context size proportionally so larger machines receive higher token budgets without explicit user configuration. Returns resolved count or an error if no source yields a usable value.

#### GetDatabasePath

**Function Signature:**
```go
func GetDatabasePath() (string, error)
```

Returns canonical SQLite database filename (`code-reducer.sqlite`) relative to working directory. Intended for callers that need only the filename; no filesystem access occurs and no error path exists.

---

## Repository Introspection Tools (`tools`)

### Responsibility
Supplies hardened low-level primitives for reading, writing, discovering, and hashing files within a Git repository. All exported functions operate on absolute paths after sanitization to prevent directory-traversal attacks and TOCTOU race conditions.

### Data Flow (Linear)
Callers resolve virtual paths through the filesystem layer (`file_tools.go`), capture version-control state via `git_commands` (`git_tools.go`), or compute a stable root hash (`registry.go`).

### ReadFileSafely

**Function Signature:**
```go
func ReadFileSafely(virtualPath string) ([]byte, error)
```

Resolves a virtual path inside the repository, performs security validation (path prefix checks against known safe directories), then reads and returns its contents. Returns an error if the file does not exist or is outside the allowed root boundary.

**Data Flow:** `virtualPath` → path sanitization → root-boundary check → `os.ReadFile` → return contents.

### WriteFileSafely

**Function Signature:**
```go
func WriteFileSafely(virtualPath string, data []byte) error
```

Resolves a virtual path, creates parent directories (`os.MkdirAll`) if absent, writes content to the file descriptor after opening, then verifies no TOCTOU symlink was created between open and write completion by re-checking the resolved target. Returns an error on any failure mode including race-condition detection.

**Data Flow:** `virtualPath` + `data` → parent directory creation → `os.OpenFile(O_CREATE|O_WRONLY)` → post-write symlink check → return success/error.

### DiscoverCodeFiles

**Function Signature:**
```go
func DiscoverCodeFiles(ignorePatterns []string) ([]string, error)
```

Recursively walks the codebase to collect high-signal source files while skipping build artifacts, dependency trees (`vendor/`, `node_modules/`), caches (`.git/`, `.hg/`, `__pycache__/`), and paths matching user-supplied ignore patterns. Returns a sorted slice of absolute paths.

**Data Flow:** root directory → recursive walk (`filepath.Walk`) → filter against skip-set + pattern matcher → deduplicate → return sorted path list.

### IsBinaryFile

**Function Signature:**
```go
func IsBinaryFile(path string) (bool, error)
```

Determines whether a file is binary by scanning its first 1024 bytes for null byte (`\x00`) characters. Returns `true` if any null byte is encountered within the scan window; otherwise returns `false`.

**Data Flow:** `path` → open file → read 1024 bytes → scan for `\x00` → return boolean.

### RunGit

**Function Signature:**
```go
func RunGit(args ...string) ([]byte, error)
```

Executes a git command in the specified repository directory and returns its combined stdout/stderr output (or an error). Captures both streams and merges them before returning to allow full diagnostic information retrieval from a single call site.

**Data Flow:** `args` → `git exec -C repoDir args...` → capture stdout + stderr concurrently → merge → return combined bytes or error.

### GetGitHead

**Function Signature:**
```go
func GetGitHead() (string, error)
```

Returns the current HEAD commit hash by delegating to `git rev-parse HEAD`. Returns an empty string and non-nil error if the repository is not in a detached state or no commits exist.

**Data Flow:** → `RunGit("rev-parse", "HEAD")` → trim whitespace → return hash string.

### CreateGitSummary

**Function Signature:**
```go
func CreateGitSummary() (string, error)
```

Builds a structured multi-section string of git status, head hash, log history (last N commits), and diff evidence for use as LLM prompt context. Sections are concatenated in deterministic order to produce reproducible output across identical repository states.

**Data Flow:** → `GetGitHead()` + `RunGit("status")` + `RunGit("log", "-n10")` + `RunGit("diff")` → format each section with delimiters → return single string.

### HashRepoRoot

**Function Signature:**
```go
func HashRepoRoot(path string) (string, error)
```

Computes and returns a hexadecimal-encoded SHA-256 hash of the resolved absolute file system path provided by the caller. Input is canonicalized (trailing slashes removed, symlinks followed via `filepath.EvalSymlinks`) to ensure deterministic output regardless of how the path was constructed.

**Data Flow:** `path` → `EvalSymlinks` + canonicalization → `io.ReadAll` on path bytes → SHA-256 digest → hex encoding → return 64-character hex string.

---

## Code Retrieval Engine (`engine`)

### Responsibility & Data Flow
Orchestrates a code-retrieval pipeline: ingests repository files, ranks them by BM25 relevance to user queries, wraps selected context in XML-delimited containers for injection safety, serializes the request into an LLM call via an Ollama-compatible client, and deserializes the resulting JSON response.

**Data Flow:** `Query String` → `FilterFilesBM25` (rank files) → `WrapInXmlDelimiter` (sanitize content) + `AutoScaleContext` (cap context length) → `LLMClient` (serialize request via `Message`) → LLM API → `CleanJSONResponse` / `UnmarshalJSONResponse` (deserialize response).

### Core Data Structures & Configuration

#### Message

**Struct Definition:**
```go
type Message struct {
    Role    string
    Content string
}
```

Represents a single turn in an LLM conversation with JSON-serializable `Role` and `Content` fields. Used to construct the prompt payload sent to the model via `LLMClient`.

#### LLMClient

**Struct Definition:**
```go
type LLMClient struct {
    ModelID  string
    BaseURL  string
    NumCtx   int
}
```

Holds Ollama-compatible API credentials: target model identifier, base URL for the inference endpoint, and maximum context window size. Constructed via `NewLLMClient(modelID, baseURL, numCtx)`.

#### Event

**Struct Definition:**
```go
type Event struct {
    Type    string
    Message *Message
}
```

Carries an event type label and optional payload message. Used to route internal processing events (e.g., completion signals, error flags).

#### DirNode

**Struct Definition:**
```go
type DirNode struct {
    Path      string
    Files     []string
    Children  []*DirNode
}
```

Recursive directory tree node representing the repository layout: `Path` stores the absolute path, `Files` holds a list of files at this level (with relative paths), and `Children` contains nested `DirNode` slices for subdirectories. Used to represent file system structure consumed by BM25 retrieval.

### Context Processing Pipeline

#### Document

**Struct Definition:**
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

### LLM Response Handling

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