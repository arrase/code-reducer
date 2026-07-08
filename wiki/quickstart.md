# Code Reducer — Wiki/Quickstart

## System Architecture Overview

**Code Reducer** is a CLI-driven tool that generates and maintains Markdown architecture documentation from Go code repositories using LLM inference via Ollama. The system operates on three core boundaries: the **command-line interface**, the **configuration resolution pipeline**, the **AI synthesis engine**, and the **security/file I/O layer**. All modules converge through a shared `repoRoot` path boundary enforced by the security module, which validates all filesystem operations against an absolute root to prevent traversal attacks.

### Module Interaction Map

```
User CLI Invocation (cmd)
    │
    ├─► config.ResolveConfig() ───────────────┐
    │     │                                    │
    │     ▼                                    ▼
    │  System Defaults ◄──────────────────────────────────┐
    │      YAML File (.code-reducer.yaml)                  │
    │      Env Variables                                   │
    │      CLI Flags                                       │
    │                                                       ▼
    │                                              *Config struct
    │                                                      │
    ▼                                                     ▼
Engine (engine) ◄───────────────── Security (security)
│                   │                    │
│  Runner           │   SimpleLock       │
│  DirNode Tree     │   SafeResolve      │
│  synthesizeNode() │                    │
│  LLMClient        │    ReadFileSafely  │
│  MetadataCache    │    WriteFileSafely │
└───────────────────┴────────────────────┘
         │
         ▼
tools (git_tools / file_tools) ── git verify, ignore patterns, binary detection
```

**Data Flow Summary:** `cmd` parses CLI arguments and persists user config → `config` resolves the canonical `*Config` through a priority merge pipeline → `engine.Runner` consumes `*Config`, builds a `DirNode` tree via recursive traversal, synthesizes per-node summaries through LLM calls with SHA256 caching, then generates global architecture/quickstart docs → `security` enforces path boundaries and process locks for all filesystem operations throughout the pipeline.

---

## Developer Quick Reference

### cmd — CLI Entry Point & Subcommand Registry

**Boundary:** All user-facing commands flow through a cobra-based CLI rooted at `RootCmd`. Non-zero exit codes propagate as status 1 from `main()`.

| Component | Behavior |
|-----------|----------|
| `main()` / `root.go` | Delegates to `cmd.RootCmd.Execute()`, enforces exit-on-error |
| `setupCmd` (`setup.go`) | Interactive prompts collect LLM model ID, Ollama URL + context size, ignore lists (comma-separated), docs dir path; persists via `config.SaveConfig` |
| `updateCmd` (`update.go`) | Detects changed files since last documented commit via git diff semantics; triggers incremental wiki page regeneration |

**Setup Flow Detail:** `RunSetupFlow` orchestrates interactive prompts. `promptAndAppend` merges comma-separated stdin input against defaults or existing lists (preserves first-seen order). `promptString` reads single-line text, trims whitespace, falls back on empty/failed reads. All aggregated config persists via `config.SaveConfig`.

### config — Configuration Resolution Pipeline

**Boundary:** Single authoritative `*Config` value consumed by all downstream components. Never read individual sources after resolution.

| Source (lowest → highest priority) | Override Key / Mechanism |
|---|---|
| System defaults (`OllamaDefaultBaseURL`, `OllamaDefaultNumCtx`) | Hardcoded fallbacks |
| YAML file (`.code-reducer.yaml`) | `LoadConfig` parses into populated struct; errors on missing/invalid file |
| Environment variables | `CodeReducerModelIdEnvKey`, `OllamaBaseUrlEnvKey`, `OllamaNumCtxEnvKey`, `LangsmithApiKeyEnvKey`, `LangchainProjectEnvKey`, `LangchainTracingEnvKey` |
| CLI flags (`modelIdFlag`, `numCtxFlag`) | Highest priority; overrides env + file + defaults |

**Constants:** Filesystem name `.code-reducer.yaml`; write permissions enforced at `0600`.

**Resolution Pipeline:** `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag) *Config` orchestrates the full merge. Generic `MergeAndDeduplicate[T comparable]` preserves insertion order when combining default ignore lists with user overrides.

### engine — AI-Powered Documentation Synthesis System

**Boundary:** End-to-end pipeline: Configuration → Runner instantiation → repository discovery → DirNode tree construction → recursive `synthesizeNode` (LLM extraction + SHA256 cache lookup) → root summary → standard docs generation → metadata serialization via `MetadataCache`.

#### LLM Client Abstraction (`client.go`)
- `NewLLMClient(modelID, baseURL string, contextLength int)` — constructs client with model identifier, API endpoint, max context size
- `CallLLM(messages []Message) (string, error)` — non-streaming HTTP request to Ollama backend; returns full text or error
- `StreamLLM(messages []Message, handler func(string)) error` — streaming session; calls user-supplied function per content segment until complete
- `GetDefaultSystemPrompt(mode string) string` — contextualized system prompt based on operation mode (code extraction, module synthesis, architecture docs)

#### Caching Layer (`cache.go`)
- `FileCacheEntry` stores SHA256 hash + facts string for individual files; enables change detection without re-reading source during updates
- `MetadataCache` aggregates global state: last documented commit reference, file entries (path → FileCacheEntry mapping), module mappings
- `loadMetadataCache(metadataPath)` — reads `.metadata.json` relative to docs dir or creates empty cache
- `saveMetadataCache(metadataPath, cache)` — serializes as indented JSON to metadata path
- `computeSHA256(fileContent []byte) string` — SHA256 hex digest for content from virtual file paths relative to repo root

#### Directory Tree Representation (`tree.go`)
- `DirNode` contains directory path, list of files (with SHA256 hashes), child nodes keyed by subdirectory name
- `FileChange` holds Path + Status ("Added", "Modified", "Deleted") for incremental update cycles

#### JSON Response Parsing (`json_parser.go`)
- `StripOuterMarkdownFence(input)` — strips surrounding markdown/JSON code fences
- `CleanJSONResponse(raw) (string, error)` — removes fences, uses brace/bracket matching to extract complete JSON object/array
- `UnmarshalJSONResponse(raw, v interface{}) error` — cleans then deserializes into target

#### Pipeline Orchestrator (`orchestrator.go`)
- `Event` struct carries Type + Message strings for pipeline status callbacks
- `GenerateStandardDocs(summary string, client *LLMClient) error` — generates global architecture and quickstart Markdown from root summary via LLM; writes safely to disk
- `RunInit(client *LLMClient) error` — full Map-Reduce init: discover code → build DirNode tree → synthesize root summary hierarchically → regenerate standard docs → update AGENTS.md with AI agent guidelines
- `RunUpdate(client *LLMClient, repoRoot string) error` — incremental op: SHA256 comparison against metadata cache detects file changes (added/modified/deleted) → determines affected directories via tree traversal → re-synthesizes summaries only where needed → conditionally regenerates architecture or quickstart docs

#### Configuration and Runner (`runner.go`)
- `NewRunner(cfg *Config) (*Runner, error)` — factory constructor; entry point for initializing the documentation generation system

### security — Filesystem Isolation & Process Synchronization

**Boundary:** All filesystem operations are gated by `repoRoot` absolute path as a constant security boundary. Path validation prevents traversal outside this root.

| Function | Behavior |
|----------|----------|
| `SafeResolve(repoRoot, inputPath string) (string, error)` | Neutralizes relative references (`..`, single-dot), resolves against repoRoot, returns validated absolute path or error on traversal attempt |
| `SimpleLock` struct — LockFilePath + FileHandle for process-level locking | `AcquireLock(repoRoot string)` opens `<repoRoot>/.code-reducer.lock` with O_EXCL; writes PID via atomic file semantics; returns `*SimpleLock` holding open handle. `Unlock(lock *SimpleLock) error` releases lock, removes lockfile from disk |
| `EnsureGitignoreHasLockfile(repoRoot string) error` | Appends `.code-reducer.lock` to `.gitignore` if absent (creates file if nonexistent) |

### tools — Repository Verification & Safe File I/O

**Boundary:** All functions operate within a verified git repository context established by `VerifyGitRepo`. Callers must invoke this before any repo-aware operation.

#### Git Operations (`git_tools.go`)
- `VerifyGitRepo(repoPath string) error` — validates git binary presence + running `rev-parse --is-inside-work-tree`; errors on either precondition failure
- `RunGit(repoPath string, args ...string) (string, error)` — executes git commands scoped to working tree; captures stdout trimmed of trailing whitespace; propagates non-zero exit codes as errors

#### Safe File I/O (`file_tools.go`)
- `ReadFileSafely(repoPath string) ([]byte, error)` — resolves relative path to absolute, performs error-checked open/read/close cycle; returns complete payload or error
- `WriteFileSafely(repoPath string, content []byte) error` — three safety measures: parent directory creation with mode preservation, symlink detection and rejection (prevents silent redirect attacks), TOCTOU-safe truncation via O_RDWR | O_CREATE | O_TRUNC
- `IsBinaryFile(path string) bool` — scans first 1024 bytes for null byte `\x00`; returns true if binary detected; used as quick filter during code discovery

#### Code Discovery & Filtering (`file_tools.go`)
- `LoadGitignore(repoRoot string) ([]string, error)` — reads `.gitignore`, strips comments and blank lines, returns active pattern set (nil if missing); patterns returned as-is for downstream matching
- `ShouldIgnoreFile(relPath string) bool` — evaluates against .gitignore patterns, dot-prefixed directories (`.build`, `.cache`), known binary extensions, null-byte presence; returns true if any rule matches → skip during discovery
- `DiscoverCodeFiles(repoRoot string) ([]string, error)` — recursively walks repoRoot collecting source files while skipping build dirs (`node_modules`, `.git`, `target`), .gitignore patterns, binary-flagged files; returns sorted slice of relative paths for deterministic consumption by analysis pipelines