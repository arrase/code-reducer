# Architecture Overview — Code Reducer

## System Boundaries

Code Reducer is a CLI tooling system that generates and maintains Markdown documentation (architecture, quickstart) from code repositories using LLM inference. The system enforces security boundaries around filesystem operations and repository integrity at every layer.

### Module Interaction Map

```
┌───────────────┐     ┌───────────────┐     ┌───────────────┐
│   cmd          │────▶│    config     │────▶│   engine       │
│  (CLI Entry)   │◀────│(Resolution)   │◀────│(Documentation) │
└───────────────┘     └───────────────┘     └───────────────┘
       │                    │                      │
       ▼                    ▼                      ▼
┌───────────────┐     ┌───────────────┐     ┌───────────────┐
│   security     │◀────│    tools      │────▶│  (git/file)   │
│(Isolation &    │     │ (Repo I/O)   │     │  utilities)   │
│ Locking)       │     └───────────────┘     └───────────────┘
└───────────────┘
```

### Data Flow Summary

1. **`cmd`** receives CLI invocation → resolves configuration via `config.ResolveConfig()` → instantiates `engine.Runner` with resolved config
2. **`config`** loads `.code-reducer.yaml`, merges env vars and CLI flags in priority order (file < env < flag) → authoritative `*Config` struct consumed by downstream modules
3. **`security.SafeResolve()`** gates every filesystem operation — enforces that all paths remain within the verified repository root boundary
4. **`tools`** provides foundational git/file utilities (`VerifyGitRepo`, `DiscoverCodeFiles`, safe read/write) used by both `cmd` and `engine` for repository verification and code discovery

### Security Model

- All file operations pass through `security.SafeResolve(repoRoot, inputPath)` which validates paths stay within the absolute repo root
- Process-level locking via atomic file-based locks (PID-pinned `.code-reducer.lock`) prevents concurrent access to lock state
- Lock artifact is registered in `.gitignore` automatically
- Path traversal attacks are neutralized by eliminating `..` and single-dot references before OS-level resolution

---

## Module Overview

### `cmd` — CLI Entry Point & Subcommand Registry

Exposes a cobra-based CLI with two subcommands:

| Command | Purpose |
|---|---|
| `setup` | Interactive configuration collection (LLM model, Ollama URL, ignore lists, docs dir) → persisted via `config.SaveConfig` |
| `update` | Incremental wiki regeneration — detects changed files since last documented commit using git diff semantics → triggers engine pipeline |

Entry point: `main()` delegates to `cmd.RootCmd.Execute()`. Non-zero exit codes propagate as status 1. No exported types exist beyond the cobra commands themselves.

**Setup flow:** Prompts collect values from stdin; merges against defaults or existing config state; persisted with restricted permissions (`0600`).

**Update flow:** Detects changed files since last documented commit using git diff → triggers incremental wiki page regeneration against current source tree.

### `config` — Configuration Resolution Pipeline

Centralizes all configuration sources into a single authoritative `*Config` value through a deterministic merge pipeline:

```
system defaults ──▶ .code-reducer.yaml ──▶ env vars ──▶ CLI flags
     (lowest)         (file)                 (medium)    (highest)
```

**Constants:** Env var keys (`CodeReducerModelIdEnvKey`, `OllamaBaseUrlEnvKey`, etc.), fallback values (`OllamaDefaultBaseURL` = default Ollama address, `OllamaDefaultNumCtx` = 8192 tokens), config filename (`.code-reducer.yaml`).

**Resolution pipeline:** `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag)` orchestrates the full merge — system defaults → YAML file → env vars → CLI flags. Downstream components read only the resolved struct, never individual sources.

**I/O operations:**
- `LoadConfig(cwd)` reads `.code-reducer.yaml` into a populated `Config` struct; errors on parse failure or missing file
- `SaveConfig(cwd, cfg)` serializes to `.code-reducer.yaml` with restricted permissions (`0600`)

**Merge utility:** `MergeAndDeduplicate[T comparable](a, b []T) []T` — generic slice concatenation preserving insertion order. Applied when merging default ignore lists with user overrides.

### `engine` — AI-Powered Documentation Synthesis System

End-to-end pipeline for generating and maintaining Markdown architecture documentation from code repositories using LLM inference:

**Data flow:** Configuration → `Runner` instantiation → repository discovery → `DirNode` tree construction → recursive `synthesizeNode` (LLM extraction + cache lookup) → root summary → standard docs generation → metadata serialization via `MetadataCache`.

**Components:**

| Component | Purpose |
|---|---|
| **LLM Client (`client.go`)** | Abstraction over Ollama backend — holds config params + HTTP client. Methods: `NewLLMClient`, `CallLLM` (non-streaming), `StreamLLM`, `GetDefaultSystemPrompt`. Returns full text or error on failure. |
| **Caching Layer (`cache.go`)** | Stores SHA256 hashes and facts per file; detects content changes without re-reading during update cycles. `MetadataCache` persists global state including last documented commit, file entries (path → hash mapping), and module mappings. Serialized as indented JSON to `.metadata.json`. |
| **Directory Tree (`tree.go`)** | `DirNode` — directory path + list of files (with SHA256 hashes) + child nodes keyed by subdirectory name. Used for hierarchical traversal during synthesis. |
| **Synthesis Engine (`synthesize.go`)** | Recursive `synthesizeNode(dirNode)` processes child directories and files with LLM extraction; caches results via SHA256 hashing to avoid redundant work on unchanged nodes. |
| **JSON Response Parsing (`json_parser.go`)** | Strips markdown/JSON code fences from input → extracts complete JSON object/array via brace/bracket matching → deserializes into target interface value. |
| **Pipeline Orchestrator (`orchestrator.go`)** | `RunInit(client)` — full Map-Reduce pipeline: discover code → build tree → synthesize root summary through hierarchical merging → regenerate standard docs → update AGENTS.md with AI agent guidelines. `RunUpdate(client, repoRoot)` — detects file changes by SHA256 comparison against metadata cache → determines affected directories → re-synthesizes summaries only where needed → conditionally regenerates architecture or quickstart docs. |
| **Configuration & Runner (`runner.go`)** | `NewRunner(cfg) (*Runner, error)` factory constructor; entry point for initializing the documentation generation system. |

### Pipeline Orchestrator Detail

**Event struct:** Lightweight `Type` + `Message` fields used to pass pipeline status updates back through callback interfaces.

**Standard docs generation (`GenerateStandardDocs`):** Generates global architecture and quickstart Markdown files from a root summary via LLM calls; writes results safely to disk.

### `security` — Filesystem Isolation & Process Synchronization

Enforces filesystem isolation and process synchronization primitives for repository-level operations:

| Component | Purpose |
|---|---|
| **Path Validation (`SafeResolve`)** | Resolves arbitrary user-supplied path against canonical absolute repo root; neutralizes relative references (`..`, single-dot); returns error if resolved target escapes `repoRoot` boundary. |
| **File-Based Locking (`SimpleLock`)** | Atomic file-based locking: opens `<repoRoot>/.code-reducer.lock` with `O_EXCL`; writes current PID to newly created file; releases by flushing/closing handle and removing lockfile from disk. |
| **Gitignore Maintenance** | Ensures `.gitignore` tracks the lock artifact to prevent accidental version control inclusion. |

### `tools` — Repository Verification & Safe File I/O

Foundational utilities for interacting with git repositories and handling filesystem operations:

| Component | Purpose |
|---|---|
| **Git Operations (`git_tools.go`)** | `VerifyGitRepo(repoPath)` validates git binary presence + working tree recognition via `rev-parse --is-inside-work-tree`. `RunGit(repoPath, args...)` executes arbitrary git commands scoped to verified working tree; captures stdout, trims whitespace, propagates non-zero exit codes as errors. |
| **Safe File I/O (`file_tools.go`)** | `ReadFileSafely` — resolves relative path to absolute location + error-checked open/read/close cycle. `WriteFileSafely` — directory creation (parent dirs created if absent), symlink detection and rejection, TOCTOU-safe truncation via `O_RDWR | O_CREATE | O_TRUNC`. `IsBinaryFile(path)` — scans first 1024 bytes for null byte; returns true if binary content detected. Used as quick heuristic filter during code discovery. |
| **Code Discovery Pipeline (`file_tools.go`)** | `LoadGitignore(repoRoot)` reads `.gitignore`, strips comments and blank lines, returns active pattern set. `ShouldIgnoreFile(relPath)` evaluates against gitignore patterns + dot-prefixed directories (`.build`, `.cache`) + binary extensions + null-byte presence in path string. `DiscoverCodeFiles(repoRoot)` recursively walks codebase collecting source files while skipping build directories (`node_modules`, `.git`, `target`), paths matching active gitignore patterns, and files flagged as binary. Returns sorted slice of relative paths. |