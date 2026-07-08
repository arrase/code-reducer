# Internal Modules — Architecture & Specification

---

## Subsystem: security (`security.go`)

**Responsibility:** Enforces sandbox isolation, concurrency safety, and version-control hygiene for repository operations. All functions operate relative to the canonical `repoRoot`, establishing a consistent security perimeter around filesystem I/O and lock management.

### Path Traversal Protection — `SafeResolve`

```go
func SafeResolve(repoRoot, inputPath string) (string, error)
```

Validates that an external `inputPath` resolves strictly within the repository boundary defined by `repoRoot`. Rejects absolute paths, relative traversals via `..`, and symlink targets pointing outside the repo. Returns a normalized absolute path on success or a sentinel error on failure. Acts as the mandatory entry point for all filesystem I/O operations to prevent escape vectors.

### Concurrency Control — `AcquireLock`

```go
func AcquireLock(repoRoot string, exclusive bool) (*flock.Flock, error)
```

Acquires a POSIX advisory file lock using `syscall.Flock`. Accepts an `exclusive bool` flag selecting between exclusive (write-serialize) and shared (read-multiple-writer) modes. Detects and rejects TOCTOU symlink races on the lockfile itself before acquiring, ensuring the target inode has not been swapped post-resolution. Returns a `*flock.Flock` handle that must be released by the caller to prevent resource leaks.

### Version Control Hygiene — `EnsureGitignoreHasLockfile` & Constants

```go
func EnsureGitignoreHasLockfile(repoRoot string) error
const LockFileName = ".code-reducer.lock"
```

`LockFileName` defines the canonical path for the process-level lockfile used consistently across all functions in this module.

`EnsureGitignoreHasLockfile` ensures `.gitignore` contains an entry for `LockFileName`. If `.gitignore` is missing, it creates one with the lockfile entry; if existing, appends the entry to prevent duplicate tracking or corruption of git state. Prevents accidental modification of the lockfile via `git` operations while preserving repository integrity.

---

## Subsystem: tools (`internal/tools`)

**Responsibility:** Encapsulates low-level primitives for filesystem I/O, repository traversal, Git interaction, and deterministic content hashing. Sits between the application layer and OS/subprocess abstractions, shielding consumers from path resolution failures, symlink race conditions, build artifact noise, and non-deterministic subprocess behavior. Data flows originate at either a virtual file system path or a repository root string; both converge on byte-level operations (read/write/hash) before being consumed by higher-order tooling modules.

### File I/O & Classification

#### `ReadFileSafely`

```go
func ReadFileSafely(path string) ([]byte, error)
```

Resolves a virtual path through the embedded filesystem abstraction and reads underlying content into memory. Returns raw bytes (`[]byte`) or an error upon resolution failure (missing parent directory, permission denial) or read failure (I/O error, truncated stream). **Contract:** nil on success is not permitted—zero-length files return an empty slice with `nil` error.

#### `WriteFileSafely`

```go
func WriteFileSafely(path string, content []byte) error
```

Writes content to a virtual path using a TOCTOU-safe pattern designed to detect and reject symlink targets before truncation. Performs a stat call equivalent to the target inode immediately prior to opening for write; if the target is not a regular file (i.e., a symlink or directory), the operation aborts with an error, preventing `os.SYMLINK` race conditions where a dangling symlink could cause data loss on truncation. **Contract:** Error if the target is not a regular file or permission denied; otherwise writes `content` atomically where possible (or in a single truncation+write pass).

#### `IsBinaryFile`

```go
func IsBinaryFile(path string) bool
```

Determines binary classification by scanning the first 1024 bytes of an opened file descriptor for null byte (`\x00`) characters. Presence of any null byte within the scanned window classifies content as binary; absence implies text/ASCII. **Contract:** Boolean output—`true` if null byte detected within the first 1024 bytes.

### Repository Discovery — `DiscoverCodeFiles`

```go
func DiscoverCodeFiles(root string) ([]string, error)
```

Recursively walks the repository root, collecting high-signal source file paths while filtering out build artifacts (e.g., `.o`, `.class`, `node_modules/`), third-party dependencies, and custom ignore patterns defined in a configurable exclusion list. Uses standard library path-walkers with explicit pruning logic to skip directories matching known artifact signatures. **Contract:** Sorted slice of high-signal source file paths; error if the walk encounters unreadable directories or permission errors that cannot be recovered from.

### Git Operations — `RunGit` & `GetGitHead`

```go
func RunGit(repoDir string, args ...string) (string, error)
func GetGitHead(repoDir string) (string, error)
```

**`RunGit`:** Executes a git command within the specified repository directory, capturing both stdout and stderr streams. Returns the combined output string along with an exit code status, handling both successful execution (exit 0) and failure cases (non-zero exit codes). Subprocess spawned via `os/exec.Command`, inheriting parent environment variables unless explicitly overridden.

**`GetGitHead`:** Returns the current HEAD commit hash for the given repository root by delegating to `RunGit` with arguments equivalent to `git rev-parse --short=80 HEAD`. Returned string is trimmed of whitespace and validated against a hex character set; if validation fails, an error is propagated indicating an invalid or detached state. **Contract:** 80-character hex SHA hash of the current HEAD; error if HEAD is not reachable or git execution fails.

### Hashing & Registry — `HashRepoRoot`

```go
func HashRepoRoot(path string) (string, error)
```

Computes and returns the hexadecimal SHA-256 hash of the resolved absolute path provided as input. Resolves symlinks to obtain a canonical absolute path before hashing, ensuring deterministic output regardless of filesystem aliasing. Typically used for indexing repository states or verifying integrity in registry operations. **Contract:** 64-character hex SHA-256 digest; error if the path cannot be resolved to a canonical form.

---

## Subsystem: config (`internal/config`)

**Responsibility:** Owns the entire configuration lifecycle for Code-Reducer: schema definition, filesystem persistence with restricted permissions, and multi-layer precedence resolution. Produces a single resolved `Config` instance consumed by the pipeline runner.

### Config Schema — `config.Config`

```go
type Config struct {
    ModelID string
    OllamaSettings OllamaSettings
    LangchainTracing bool
    LangsmithTracing bool
    IgnoredPaths []string
    DocsDir string
}
```

Canonical schema for `.code-reducer.yaml`. All downstream consumers treat it as immutable once resolved.

### Filesystem Operations — `ConfigExists` & `LoadConfig` & `SaveConfig`

#### `ConfigExists`

```go
func ConfigExists(path string) (bool, error)
```

Pre-flight check before attempting to read configuration. Returns `false, nil` when absent and a non-empty error on filesystem-level failures (e.g., permission denied). Used by CLI entrypoints to gate help messages and configuration prompts.

#### `LoadConfig`

```go
func LoadConfig(path string) (*config.Config, error)
```

Reads raw YAML bytes from disk and unmarshals into a populated `Config`. Returns an empty `Config{}` with no error when the file is absent (caller must check `ConfigExists` first). On parse failure, returns the zero value with a non-nil error. Unmarshaling uses reflection-safe YAML parsing that tolerates field order differences between schema versions; unknown fields are silently dropped to prevent breaking upgrades.

**Data flow:** `disk (.code-reducer.yaml) → io.ReadFile → yaml.Unmarshal → *config.Config`

#### `SaveConfig`

```go
func SaveConfig(cfg *config.Config, path string) error
```

Marshals a `Config` into YAML and persists it with mode `0600`. The restrictive permission prevents other local users from reading sensitive configuration (model IDs, API keys embedded in Ollama settings). Atomic write semantics: the file is created at mode 0644, then `chmod`'d to 0600. This avoids a race window where the intermediate world-readable state could be observed by concurrent processes. If the parent directory does not exist, the function returns a clear error rather than silently failing.

### Precedence Resolution — `ResolveConfig`

**Responsibility:** Produce a single resolved `Config` from four sources layered in strict precedence order: **system defaults → YAML file → environment variables → CLI flags**. The final value is used by every downstream component without any caller needing to know which layer supplied it.

**Precedence contract:**

| Layer | Override scope |
| --- | --- |
| System defaults | `Config` zero values (used when no file exists) |
| YAML file | Full struct override where fields are non-empty |
| Environment variables | Per-field env mapping (`CODE_REDUCER_MODEL_ID`, etc.) |
| CLI flags | Final override; wins over everything above |

**Implementation notes:** Each layer is a pure function returning `(partial *Config, error)`. The resolver composes them left-to-right. Partial configs are merged field-by-field with the higher-precedence layer winning on any non-zero value. This prevents partial overrides from corrupting defaults (e.g., an empty string in YAML does not reset a previously-set CLI flag). The resolver returns a copy of the resolved struct to prevent accidental mutation by callers.

**Failure modes:** If the YAML parse fails during `LoadConfig`, the resolver treats it as "no config file present" and falls through to env/CLI layers only. This allows users to ship partial configurations without being blocked by malformed files.

---

## Subsystem: engine (`internal/engine`)

**Responsibility:** Implements a two-stage retrieval–generation pipeline: **BM25 lexical scoring** of candidate files followed by **LLM-driven synthesis** that consumes ranked content. The module ingests raw file paths, tokenizes their contents, ranks them against user queries using the BM25 formula, and streams LLM responses with retry logic for transient HTTP failures (429/500–504). A `Runner` orchestrates end-to-end execution: it acquires a lock, instantiates an `LLMClient`, and drives documentation generation within a bounded context window.

### BM25 Ranking & Indexing (`context.go`)

**Constants:**

| Constant | Purpose |
|----------|---------|
| `k1` | BM25 term-frequency saturation parameter (default 1.5). Controls the asymptotic growth of TF contribution per occurrence. |
| `b` | BM25 document-length normalization factor (default 0.75). Reduces the weight assigned to documents as their length approaches the corpus mean. |

**Types:**

- **`Document`** — Indexable unit holding a file path, raw content, tokenized tokens, per-term frequencies, and total token count (`n`). Populated before ingestion into the BM25 scorer.
- **`DocScore`** — Temporary key-value pair pairing each candidate path with its computed BM25 relevance score during ranking phase.

**Functions:**

- **`Tokenize`** — Splits input text into lowercase alphanumeric word tokens via regular expression matching (`[a-z0-9]+`). Returns a slice of trimmed token strings.
- **`FilterFilesBM25`** — Orchestrates the full scoring pipeline: loads each candidate file, tokenizes content, computes smoothed IDF for query terms, scores every document with `TF · IDF`, sorts results by descending relevance, and returns the top *K* paths.
- **`EstimateTokens`** — Approximates token count by dividing character length by 4 (assumes ~4 characters per average English word). Used for context-budget estimation when exact counting is unavailable.
- **`WrapInXmlDelimiter`** — Encapsulates file path and content inside strict `<file_content>` XML-like tags to mitigate prompt-injection risks during LLM consumption.
- **`AutoScaleContext`** — Returns a safe default of 8192 characters when the caller provides zero or negative context limits; otherwise passes through unchanged.

### LLM Client & Interaction (`engine.go`)

**Types:**

- **`Message`** — Represents an LLM message with `role` and `content` fields for chat interaction (system, user, assistant).
- **`LLMClient`** — HTTP-based client struct configured with model ID, base URL, context length, and timeout. Wraps synchronous (`CallLLM`) and streaming (`StreamLLM`) invocation methods.

**Functions:**

- **`NewLLMClient`** — Constructs an `LLMClient` instance from a configuration pointer containing model ID, base URL, context length, and timeout parameters.
- **`CallLLM`** — Invokes a synchronous LLM call with retry logic for transient HTTP errors (status 429 Too Many Requests; 500–504 server/transport-level failures). Returns the parsed response or an error.
- **`StreamLLM`** — Streams the LLM response in real time by reading line-by-line from the HTTP stream and invoking a callback function per chunk received.

**Prompt Loading:**

- **`Event`** — Represents an event occurrence with a type identifier and associated message string (used for logging pipeline events).
- **`GetDefaultSystemPrompt`** — Returns a system prompt tailored to one of four commands: `extract_file`, `module_synthesis`, `architecture`, or `default`.
- **`LoadSystemPrompt`** — Delegates directly to `GetDefaultSystemPrompt`; provides aliasing for external consumers.

### JSON Parsing Utilities (`json_parser.go`)

**Functions:**

- **`CleanJSONResponse`** — Extracts raw JSON content from a response string by either stripping markdown code fences (`` ``` ``) or locating the first opening brace/bracket to the last closing brace/bracket in the payload.
- **`UnmarshalJSONResponse`** — Chains `CleanJSONResponse` with `json.Unmarshal`, returning an error if parsing fails into the provided target interface value.

### Runner Orchestrator (`runner.go`)

**Types:**

- **`Runner`** — Struct storing engine configuration and orchestrating repository processing workflows across BM25 ranking, LLM streaming, and documentation generation phases.

**Functions:**

- **`NewRunner`** — Constructs a new `Runner` instance by encapsulating the provided configuration pointer.
- **`Run`** — Executes the full pipeline lifecycle: acquires a lock to serialize concurrent invocations, instantiates an `LLMClient`, and drives documentation generation within a bounded context window.