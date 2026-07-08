# Code-Reducer — Quickstart & Architecture Overview

## What Is Code-Reducer?

**Code-Reducer** is a CLI-driven code analysis and documentation generation tool. It ingests source code from a repository root, ranks candidate files via BM25 lexical scoring against user queries, synthesizes documentation through an LLM pipeline, and persists configuration with restricted permissions. The system operates as a **lock-serialized single-process engine**: concurrent invocations are prevented via POSIX file locking, the analysis boundary is enforced through path-traversal protection, and all I/O passes through TOCTOU-safe wrappers to reject symlink races.

## System Boundaries & Module Interaction

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐     ┌──────────────┐
│  CLI Entry   │────▶│ Security     │────▶│ Engine (BM25 +   │────▶│ Config       │
│  RootCmd     │     │ Subsystem    │     │ LLM Synthesis)   │     │ Persistence  │
└─────────────┘     └──────────────┘     └─────────────────┘     └──────────────┘
         │                │                    │                       │
         ▼                ▼                    ▼                       ▼
    ┌──────────┐   ┌──────────┐      ┌──────────┐           ┌──────────┐
    │ Tools    │   │ Config   │      │ LLM      │           │ Schema  │
    │ I/O,     │   │ Precedence│      │ Client   │           │ Loading │
    │ Git,     │   │ Resolution│      │ (Ollama) │           │ & Save  │
    │ Hashing  │   └──────────┘      └──────────┘           └──────────┘
    └──────────┘
```

**Data flow:** CLI invocation → `RootCmd` dispatches subcommand → configuration resolved via layered precedence (system defaults → YAML file → env vars → CLI flags) → security perimeter established (`AcquireLock`, `SafeResolve`) → repository traversal discovers source files (`DiscoverCodeFiles`) → BM25 ranking scores candidate documents against query terms → LLM synthesis consumes ranked content within bounded context window → documentation persisted to `DocsDir`.

## Quickstart — First Run

### 1. Initialize Configuration

```bash
code-reducer init
```

Follow the interactive prompts:
- **LLM Model ID** — Target inference model identifier (e.g., `qwen2.5-coder`)
- **Ollama Base URL** — Endpoint for local LLM hosting (default: `http://localhost:11434`)
- **Context Size** — Token limit configuration for the session window
- **Ignored Paths** — Filesystem patterns to exclude from indexing/processing
- **Documentation Directory** — Root path for generated documentation output

Upon collection, parameters are atomically persisted to a local configuration file (YAML or JSON). Failure during write-back triggers an error exit code without modifying state.

### 2. Verify Setup

```bash
code-reducer --help
# Should show all subcommands registered under root
```

The `NeedsCredentialSetup()` gate checks if mandatory setup has been completed. If `ModelID` is absent or empty, execution halts and the user must run the interactive wizard before further commands are permitted.

### 3. Run Analysis

```bash
code-reducer <subcommand> [flags]
```

The analysis boundary is enforced through path-traversal protection (`SafeResolve`). All filesystem I/O operations validate that external paths resolve strictly within the repository boundary defined by `repoRoot`. Absolute paths, relative traversals via `..`, and symlink targets pointing outside the repo are rejected.

## Module Architecture — Dense Overview

### CLI Bootstrap & Initialization Subsystem (`cmd/`)

| Component | File | Role |
|-----------|------|------|
| **`RootCmd`** | `cmd/root.go` | Singleton cobra.Command instance representing the top-level "code-reducer" binary. Initializes the command tree, registers all subcommands via `AddCommand`, and exposes the root execution context for downstream operations. |
| **`NeedsCredentialSetup()`** | `cmd/root.go` | Boolean state check against critical configuration required for model inference. Inspects the application's credential store (e.g., `ModelID`, API keys) to determine if mandatory setup has been completed. Returns `true` when `ModelID` is absent or empty, signaling that execution must halt. |
| **`RunSetupFlow()`** | `cmd/setup.go` | Orchestrates the full initialization sequence triggered by the validation gate. Performs sequential interactive prompts via stdin/stdout to collect LLM model ID, Ollama base URL, context size, ignored paths, and documentation directory. Persists all parameters atomically. |
| **`initCmd`** | `cmd/init.go` | Unexported `*cobra.Command` definition encapsulating the "init" subcommand logic. Registered via lowercase-named function during package initialization or explicit registration calls. |

### Internal Modules — Security Subsystem (`security.go`)

Enforces sandbox isolation, concurrency safety, and version-control hygiene for repository operations. All functions operate relative to the canonical `repoRoot`, establishing a consistent security perimeter around filesystem I/O and lock management.

| Function | Signature | Behavior |
|----------|-----------|----------|
| **`SafeResolve`** | `(repoRoot, inputPath string) (string, error)` | Validates that an external `inputPath` resolves strictly within the repository boundary defined by `repoRoot`. Rejects absolute paths, relative traversals via `..`, and symlink targets pointing outside the repo. Returns a normalized absolute path on success or a sentinel error on failure. Acts as the mandatory entry point for all filesystem I/O operations to prevent escape vectors. |
| **`AcquireLock`** | `(repoRoot string, exclusive bool) (*flock.Flock, error)` | Acquires a POSIX advisory file lock using `syscall.Flock`. Accepts an `exclusive bool` flag selecting between exclusive (write-serialize) and shared (read-multiple-writer) modes. Detects and rejects TOCTOU symlink races on the lockfile itself before acquiring. Returns a `*flock.Flock` handle that must be released by the caller to prevent resource leaks. |
| **`EnsureGitignoreHasLockfile`** | `(repoRoot string) error` | Ensures `.gitignore` contains an entry for `LockFileName`. If `.gitignore` is missing, it creates one with the lockfile entry; if existing, appends the entry to prevent duplicate tracking or corruption of git state. Prevents accidental modification of the lockfile via `git` operations while preserving repository integrity. |
| **Constants** | `LockFileName = ".code-reducer.lock"` | Defines the canonical path for the process-level lockfile used consistently across all functions in this module. |

### Internal Modules — Tools Subsystem (`internal/tools`)

Encapsulates low-level primitives for filesystem I/O, repository traversal, Git interaction, and deterministic content hashing. Sits between the application layer and OS/subprocess abstractions, shielding consumers from path resolution failures, symlink race conditions, build artifact noise, and non-deterministic subprocess behavior.

| Function | Signature | Behavior |
|----------|-----------|----------|
| **`ReadFileSafely`** | `(path string) ([]byte, error)` | Resolves a virtual path through the embedded filesystem abstraction and reads underlying content into memory. Returns raw bytes (`[]byte`) or an error upon resolution failure (missing parent directory, permission denial) or read failure (I/O error, truncated stream). **Contract:** nil on success is not permitted—zero-length files return an empty slice with `nil` error. |
| **`WriteFileSafely`** | `(path string, content []byte) error` | Writes content to a virtual path using a TOCTOU-safe pattern designed to detect and reject symlink targets before truncation. Performs a stat call equivalent to the target inode immediately prior to opening for write; if the target is not a regular file (i.e., a symlink or directory), the operation aborts with an error, preventing `os.SYMLINK` race conditions where a dangling symlink could cause data loss on truncation. |
| **`IsBinaryFile`** | `(path string) bool` | Determines binary classification by scanning the first 1024 bytes of an opened file descriptor for null byte (`\x00`) characters. Presence of any null byte within the scanned window classifies content as binary; absence implies text/ASCII. **Contract:** Boolean output—`true` if null byte detected within the first 1024 bytes. |
| **`DiscoverCodeFiles`** | `(root string) ([]string, error)` | Recursively walks the repository root, collecting high-signal source file paths while filtering out build artifacts (e.g., `.o`, `.class`, `node_modules/`), third-party dependencies, and custom ignore patterns defined in a configurable exclusion list. Uses standard library path-walkers with explicit pruning logic to skip directories matching known artifact signatures. **Contract:** Sorted slice of high-signal source file paths; error if the walk encounters unreadable directories or permission errors that cannot be recovered from. |
| **`RunGit`** | `(repoDir string, args ...string) (string, error)` | Executes a git command within the specified repository directory, capturing both stdout and stderr streams. Returns the combined output string along with an exit code status, handling both successful execution (exit 0) and failure cases (non-zero exit codes). Subprocess spawned via `os/exec.Command`, inheriting parent environment variables unless explicitly overridden. |
| **`GetGitHead`** | `(repoDir string) (string, error)` | Returns the current HEAD commit hash for the given repository root by delegating to `RunGit` with arguments equivalent to `git rev-parse --short=80 HEAD`. Returned string is trimmed of whitespace and validated against a hex character set; if validation fails, an error is propagated indicating an invalid or detached state. **Contract:** 80-character hex SHA hash of the current HEAD; error if HEAD is not reachable or git execution fails. |
| **`HashRepoRoot`** | `(path string) (string, error)` | Computes and returns the hexadecimal SHA-256 hash of the resolved absolute path provided as input. Resolves symlinks to obtain a canonical absolute path before hashing, ensuring deterministic output regardless of filesystem aliasing. Typically used for indexing repository states or verifying integrity in registry operations. **Contract:** 64-character hex SHA-256 digest; error if the path cannot be resolved to a canonical form. |

### Internal Modules — Config Subsystem (`internal/config`)

Owns the entire configuration lifecycle for Code-Reducer: schema definition, filesystem persistence with restricted permissions, and multi-layer precedence resolution. Produces a single resolved `Config` instance consumed by the pipeline runner.

| Component | Role |
|-----------|------|
| **`config.Config`** | Canonical schema for `.code-reducer.yaml`. All downstream consumers treat it as immutable once resolved. |
| **`ConfigExists`** | Pre-flight check before attempting to read configuration. Returns `false, nil` when absent and a non-empty error on filesystem-level failures (e.g., permission denied). Used by CLI entrypoints to gate help messages and configuration prompts. |
| **`LoadConfig`** | Reads raw YAML bytes from disk and unmarshals into a populated `Config`. Returns an empty `Config{}` with no error when the file is absent (caller must check `ConfigExists` first). On parse failure, returns the zero value with a non-nil error. Unmarshaling uses reflection-safe YAML parsing that tolerates field order differences between schema versions; unknown fields are silently dropped to prevent breaking upgrades. |
| **`SaveConfig`** | Marshals a `Config` into YAML and persists it with mode `0600`. The restrictive permission prevents other local users from reading sensitive configuration (model IDs, API keys embedded in Ollama settings). Atomic write semantics: the file is created at mode 0644, then `chmod`'d to 0600. This avoids a race window where the intermediate world-readable state could be observed by concurrent processes. If the parent directory does not exist, the function returns a clear error rather than silently failing. |
| **`ResolveConfig`** | Produce a single resolved `Config` from four sources layered in strict precedence order: **system defaults → YAML file → environment variables → CLI flags**. The final value is used by every downstream component without any caller needing to know which layer supplied it. Each layer is a pure function returning `(partial *Config, error)`. The resolver composes them left-to-right. Partial configs are merged field-by-field with the higher-precedence layer winning on any non-zero value. This prevents partial overrides from corrupting defaults (e.g., an empty string in YAML does not reset a previously-set CLI flag). The resolver returns a copy of the resolved struct to prevent accidental mutation by callers. |

**Precedence contract:**

| Layer | Override scope |
|-------|---------------|
| System defaults | `Config` zero values (used when no file exists) |
| YAML file | Full struct override where fields are non-empty |
| Environment variables | Per-field env mapping (`CODE_REDUCER_MODEL_ID`, etc.) |
| CLI flags | Final override; wins over everything above |

**Failure modes:** If the YAML parse fails during `LoadConfig`, the resolver treats it as "no config file present" and falls through to env/CLI layers only. This allows users to ship partial configurations without being blocked by malformed files.

### Internal Modules — Engine Subsystem (`internal/engine`)

Implements a two-stage retrieval–generation pipeline: **BM25 lexical scoring** of candidate files followed by **LLM-driven synthesis** that consumes ranked content. The module ingests raw file paths, tokenizes their contents, ranks them against user queries using the BM25 formula, and streams LLM responses with retry logic for transient HTTP failures (429/500–504). A `Runner` orchestrates end-to-end execution: it acquires a lock, instantiates an `LLMClient`, and drives documentation generation within a bounded context window.

#### BM25 Ranking & Indexing (`context.go`)

| Constant | Purpose |
|----------|---------|
| **`k1`** | BM25 term-frequency saturation parameter (default 1.5). Controls the asymptotic growth of TF contribution per occurrence. |
| **`b`** | BM25 document-length normalization factor (default 0.75). Reduces the weight assigned to documents as their length approaches the corpus mean. |

| Function | Signature | Behavior |
|----------|-----------|----------|
| **`Tokenize`** | `(text string) ([]string)` | Splits input text into lowercase alphanumeric word tokens via regular expression matching (`[a-z0-9]+`). Returns a slice of trimmed token strings. |
| **`FilterFilesBM25`** | `(files []string, query string, topK int) ([]DocScore, error)` | Orchestrates the full scoring pipeline: loads each candidate file, tokenizes content, computes smoothed IDF for query terms, scores every document with `TF · IDF`, sorts results by descending relevance, and returns the top *K* paths. |
| **`EstimateTokens`** | `(text string) int` | Approximates token count by dividing character length by 4 (assumes ~4 characters per average English word). Used for context-budget estimation when exact counting is unavailable. |
| **`WrapInXmlDelimiter`** | `(path, content string) string` | Encapsulates file path and content inside strict `<file_content>` XML-like tags to mitigate prompt-injection risks during LLM consumption. |
| **`AutoScaleContext`** | `(limit int) int` | Returns a safe default of 8192 characters when the caller provides zero or negative context limits; otherwise passes through unchanged. |

#### LLM Client & Interaction (`engine.go`)

| Function | Signature | Behavior |
|----------|-----------|----------|
| **`NewLLMClient`** | `(cfg *Config) (*LLMClient, error)` | Constructs an `LLMClient` instance from a configuration pointer containing model ID, base URL, context length, and timeout parameters. |
| **`CallLLM`** | `(client *LLMClient, messages []Message) (string, error)` | Invokes a synchronous LLM call with retry logic for transient HTTP errors (status 429 Too Many Requests; 500–504 server/transport-level failures). Returns the parsed response or an error. |
| **`StreamLLM`** | `(client *LLMClient, messages []Message, callback func(string)) error` | Streams the LLM response in real time by reading line-by-line from the HTTP stream and invoking a callback function per chunk received. |
| **`GetDefaultSystemPrompt`** | `(command string) string` | Returns a system prompt tailored to one of four commands: `extract_file`, `module_synthesis`, `architecture`, or `default`. |

#### JSON Parsing Utilities (`json_parser.go`)

| Function | Signature | Behavior |
|----------|-----------|----------|
| **`CleanJSONResponse`** | `(resp string) (string, error)` | Extracts raw JSON content from a response string by either stripping markdown code fences (`` ``` ``) or locating the first opening brace/bracket to the last closing brace/bracket in the payload. |
| **`UnmarshalJSONResponse`** | `(resp string, target interface{}) (interface{}, error)` | Chains `CleanJSONResponse` with `json.Unmarshal`, returning an error if parsing fails into the provided target interface value. |

#### Runner Orchestrator (`runner.go`)

| Function | Signature | Behavior |
|----------|-----------|----------|
| **`NewRunner`** | `(cfg *Config) (*Runner, error)` | Constructs a new `Runner` instance by encapsulating the provided configuration pointer. |
| **`Run`** | `(r *Runner) (string, error)` | Executes the full pipeline lifecycle: acquires a lock to serialize concurrent invocations, instantiates an `LLMClient`, and drives documentation generation within a bounded context window. |

## Key Design Principles

1. **Security First** — All filesystem operations are TOCTOU-safe; path traversal is blocked at entry points.
2. **Single-Process Serialization** — POSIX advisory locking prevents concurrent invocations from corrupting state.
3. **Layered Configuration** — Four precedence layers allow flexible setup without breaking existing configurations.
4. **Fail-Safe Defaults** — Malformed YAML files, missing config entries, and transient LLM errors are handled gracefully with clear error propagation.
5. **Deterministic Output** — Git operations, hashing, and path resolution produce consistent results regardless of filesystem aliasing or symlink targets.