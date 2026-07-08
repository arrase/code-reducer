# code-reducer — Architecture Overview

## System Boundaries & Module Interaction

`code-reducer` is a CLI-driven repository analysis tool written in Go 1.26. It ingests a source repository, ranks files via BM25 term-frequency scoring, constructs hierarchical directory trees from those results, synthesizes markdown documentation through recursive chunk batching against an Ollama LLM backend, and persists the output to disk. The application is driven by a Cobra CLI at `cmd/`, with all orchestration funneled through a root command that enforces sequential state transitions (setup → lock acquire → config load → engine run → stream).

The system boundary encompasses **five packages** organized into two layers:

- **Infrastructure layer** (`security`, `internal/tools`) — hardens path resolution, enforces repository isolation, serializes concurrent processes via flock-based locking, and provides safe file I/O primitives.
- **Engine layer** (`internal/engine`, `internal/config`) — ingests file corpus, computes relevance scores, assembles LLM prompts from directory trees, invokes the Ollama API, strips markdown fences from responses, and writes synthesized output.

The CLI package (`cmd`) sits at the entry point and delegates lifecycle management to the engine layer while gating execution on credential presence (the `MODEL_ID` environment variable). If credentials are missing, it redirects into an interactive setup flow that persists configuration to `.code-reducer.yaml`, then re-acquires the lock and reloads config before proceeding.

## Module Interaction Diagram

```
┌─────────────────────────────────────────────────┐
│              cmd/ (Cobra CLI)                     │
│  ┌──────────────┐  ┌────────────┐  ┌──────────┐ │
│  │ init subcmd  │  │ rootCmd    │  │ setup     │ │
│  └──────────────┘  └─────┬──────┘  └─────┬─────┘ │
│                          │               │       │
│                    executeCommand()      │       │
│                          │               │       │
│  ┌──────────────────────▼─────────────────────▼──┐│
│  │ security.SafeResolve(repoRoot)                 ││
│  │ security.AcquireLock(exclusive=true)           ││
│  │ internal/config.LoadConfig(cwd)                ││
│  │ internal/engine.reducesInChunks(dirTree)       ││
│  │ ┌─────────────────────────────────────────────┐││
│  │ │ LLMClient → Ollama API → markdown strip     │││
│  │ └─────────────────────────────────────────────┘││
│  │ internal/tools.WriteFileSafely(output)         ││
│  └────────────────────────────────────────────────┘│
└──────────────────────────────────────────────────┘
```

## Package Responsibilities & Interfaces

### `security` — Repository Isolation & Concurrency Control

Enforces three subsystems: path resolution integrity, file-based locking for process serialization, and VCS hygiene (lockfile excluded from Git tracking). The package anchor is `.code-reducer.lock`, a repository-root coordination file. All path operations resolve through `SafeResolve(repoRoot, inputPath)` which validates strict containment within `repoRoot` and rejects dangling symlinks or escape attempts.

Key functions:
- `SafeResolve(repoRoot, inputPath) (string, error)` — canonical absolute path resolution with traversal rejection
- `AcquireLock(repoRoot string, exclusive bool) (*flock.Flock, error)` — flock acquisition with PID metadata recording; shared lock for reads, exclusive for writes
- `EnsureGitignoreHasLockfile(repoRoot string) error` — guarantees `.gitignore` includes the lockfile pattern

### `internal/tools` — Low-Level Repository Operations

Provides safe file I/O, git command execution, source-file discovery, and SHA-256 path hashing. All filesystem operations resolve absolute paths once per operation via `filepath.Abs` before any disk interaction to prevent path-traversal vulnerabilities.

Key functions:
- `ReadFileSafely(path) []byte` — inbound read with security-layer resolution
- `WriteFileSafely(relPath string, content []byte) error` — outbound write; creates parents as needed, verifies no symlink injection before persisting
- `DiscoverCodeFiles(root string) ([]string, error)` — recursive walk collecting source files while excluding build artifacts, binaries (null-byte detection), and dependency directories (`vendor`, `node_modules`)
- `IsBinaryFile(path string) (bool, error)` — null-byte scan of first 1024 bytes; O(1) regardless of file size
- `RunGit(root string, args ...string) (string, error)` — git subcommand execution returning combined stdout/stderr without panicking on non-zero exit codes
- `GetGitHead(root string) (string, error)` — HEAD commit hash via `git rev-parse HEAD`
- `HashRepoRoot(path string) (string, error)` — SHA-256 hex of resolved path for content-addressable keys

### `internal/engine` — Orchestration Layer

Manages document ingestion, BM25 ranking, directory tree construction, system prompt retrieval, chunk batching, Ollama client configuration, and JSON response parsing. The data flow is: raw corpus → tokenization → frequency computation → BM25 scoring → XML-wrapped context assembly → LLM invocation → markdown fence stripping → synthesis output.

Key functions:
- `Tokenize(input string) []string` — regex-based lowercase alphanumeric tokenization
- `EstimateTokens(text string) int` — 4-char-per-token approximation for context budgeting
- `FilterFilesBM25(query, files []Document) []Document` — BM25 relevance ranking returning top-K by descending score
- `buildTree(filePaths []string) *DirNode` — constructs hierarchical directory tree from file paths rooted at `"."`
- `GetDefaultSystemPrompt(phase string) (string, error)` — task-specific prompts for analysis/synthesis/architecture phases
- `NewLLMClient(modelID, baseURL string, numCtx int) (*LLMClient, error)` — factory for Ollama transport singleton holding model ID, base URL, and context size limit
- `stripOuterMarkdownFence(response string) string` — extracts content between triple-backtick fences via regex
- `reduceInChunks(dirTree *DirNode, accumulated []byte) (string, error)` — recursive chunk batching with post-response markdown stripping; input is directory tree + accumulated results, output is synthesized markdown
- `WrapInXmlDelimiter(content, filePath string) string` — wraps file content in `<file>path</file><content>...</content>` XML-like delimiters to prevent prompt injection
- `AutoScaleContext(maximum int) int` — caps context at 8192 when maximum is non-positive; otherwise passes through

### `internal/config` — Configuration Management

Centralizes application configuration resolution with a load → parse → apply → persist data flow and short-circuit semantics (silent skip on missing file for `LoadAndApplyConfig`). Schema lives in `.code-reducer.yaml` with 0600 permissions.

Key functions:
- `ConfigExists(cwd string) bool` — checks for `.code-reducer.yaml` presence
- `LoadConfig(cwd string) (*Config, error)` — parses YAML into `*Config`; holds model/Ollama/LangSmith/LangChain fields plus optional ignore lists and docs directory path
- `SaveConfig(cwd string, cfg *Config) error` — marshals to YAML with 0600 mode
- `LoadAndApplyConfig(cwd string) error` — loads config (ignoring missing file), sets environment variables only when absent from parent process
- `GetOllamaContextSize(flagVal string) int` — resolves context size by checking CLI flag → env var → dynamic host RAM scaling → configured default

Environment keys defined: `CodeReducerModelIdEnvKey`, `OllamaBaseUrlEnvKey`, `OllamaNumCtxEnvKey`, `LangsmithApiKeyEnvKey`, `LangchainProjectEnvKey`, `LangchainTracingEnvKey`. Defaults: Ollama base URL is `http://localhost:11434`, default context size 8192.

### `cmd` — CLI Entry Point & Lifecycle Management

Implements Cobra-based CLI with sequential state transitions enforced by the root command. Registers subcommands at package init time and delegates orchestration to a root command that gates execution on credential presence.

Key functions:
- RootCmd → `executeCommand()` orchestrates: credential check → lock acquire → config load → LLM engine invocation + live event streaming
- `NeedsCredentialSetup() bool` — returns true when `MODEL_ID` env var is absent, redirecting into setup mode
- `RunSetupFlow() error` — guides interactive configuration session; prompts for missing values and writes `.code-reducer.yaml`; returns control to root command which re-acquires lock and reloads config before engine execution

## Configuration Hierarchy

The application resolves configuration in this order:

1. **CLI flags** (highest priority)
2. **Environment variables** (`MODEL_ID`, `OLLAMA_BASE_URL`, etc.)
3. **`.code-reducer.yaml` file** (persisted, 0600 mode)
4. **Host RAM-based auto-scaling** (lowest, for context size only)

The config file is written only during setup flow or manual invocation; it remains outside Git tracking via `EnsureGitignoreHasLockfile`. The lockfile itself (`security.LockFileName = ".code-reducer.lock"`) is also excluded from `.gitignore` to prevent accidental inclusion in repositories.