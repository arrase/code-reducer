# wiki/quickstart.md — Code-Reducer Architecture Quickstart

## System Boundaries & Module Interaction Topology

```
┌─────────────── cmd ───────────────┐
│ RootCmd → executeCommand(mode)    │
│ init / update subcommands         │
│ RunSetupFlow (interactive)        │
└──────────────┬────────────────────┘
               │ CLI dispatch
┌──────────────▼────────────────────┐
│ internal/config                    │
│ Config.ResolveConfig()             │
│ 4-layer merge: defaults < yaml < env < flags │
└──────────────┬────────────────────┘
               │ resolved *Config
┌──────────────▼────────────────────┐
│ security                           │
│ AcquireLock → SafeResolve         │
│ Path containment enforced          │
└──────────────┬────────────────────┘
┌──────────────▼────────────────────┐
│ internal/tools                     │
│ VerifyGitRepo, DiscoverCodeFiles  │
│ ReadFileSafely, WriteFileSafely   │
└──────────────┬────────────────────┘
               │ file list + *Config
┌──────────────▼────────────────────┐
│ internal/engine                    │
│ buildTree → synthesizeNode         │
│ reduceInChunks → RunInit           │
│ LLMClient (Ollama)                 │
└───────────────────────────────────┘
```

**Execution lifecycle:** Runner acquires process lock (`security`) → verifies repository root (`tools`) → loads/resolves configuration (`config`) → executes synthesis pipeline (`engine`). All I/O operations pass through `SafeResolve` to prevent path traversal escapes outside the canonical repo root.

---

## Module 1: `cmd` — CLI Surface & Orchestration

**Boundary:** External entry point. Owns command registration, setup flow, and lifecycle orchestration. Does not own configuration resolution or engine execution—delegates both.

| Component | Responsibility |
|-----------|----------------|
| `RootCmd.executeCommand(mode)` | Root orchestrator: git validation → implicit setup → config merge → `.metadata.json` check → credential gate (`NeedsCredentialSetup()`) → engine dispatch with signal handling |
| `initCmd` / `updateCmd` | Subcommands for wiki regeneration. `updateCmd` is registered in package init to ensure availability alongside root command |
| `RunSetupFlow` | Interactive setup: prompts model ID, Ollama Base URL, context size, ignore paths/extension, doc dir → writes `.code-reducer.yaml` |

**Data flow:** `RootCmd` → `executeCommand(mode)` → git validation → implicit setup (if missing) → configuration merge → `.metadata.json` check → `NeedsCredentialSetup()` gate → engine dispatch with signal handling.

---

## Module 2: `internal/config` — Configuration Resolution & Persistence

**Boundary:** Single source of truth for pipeline configuration. Owns persistence, runtime overrides, and precedence merging. Consumed by every downstream module via the resolved `*Config`.

### Precedence Merge Order
```
system defaults (OllamaDefault*) < YAML config (.code-reducer.yaml) < environment variables (*_EnvKey) < CLI flags
```

### Key Types & Contracts

| Type | Contract |
|------|----------|
| `Config` | YAML-serializable; pointer semantics prevent zero-value marshaling for optional sections (model, tracing). Sole struct persisted to disk. |
| `MetadataCache` | Maintains last documented commit ID + maps of cached files and modules |

### Persistence Functions

| Function | Contract |
|----------|----------|
| `ConfigExists(dir)` | Stat check only—no read. Returns bool for CLI entry points to prompt on first run |
| `LoadConfig(dir)` | Read YAML → unmarshal into `Config`. Malformed returns zero-value + error |
| `SaveConfig(cfg)` | Marshal YAML → write with 0600 permissions (owner-only) |
| `ResolveConfig()` | 4-layer merge: defaults < yaml < env vars < CLI flags. Returns canonical config for all pipeline stages |

### Environment Variable Keys
- `CodeReducerModelIdEnvKey` — runtime LLM model ID override
- `OllamaBaseUrlEnvKey` — custom Ollama API base URL (default: `http://localhost:11434`)
- `OllamaNumCtxEnvKey` — context window size in tokens
- `LangsmithApiKeyEnvKey`, `LangchainProjectEnvKey`, `LangchainTracingEnvKey` — tracing config

### Defaults
- `DefaultIgnores`: `.git`, `node_modules`, `dist`, etc.
- `DefaultIgnoredExtensions`: `.png`, `.zip`, `.pyc`, etc.

These are merged with user-supplied entries during resolve and applied to file-system traversal downstream.

---

## Module 3: `security` — Path Safety & Process Serialization

**Boundary:** Two orthogonal safety guarantees—path containment enforcement and process serialization via file-based locking. All public functions operate against an absolute repository root derived at initialization; no downstream operation can escape the intended working directory.

### Lock Lifecycle
```
EnsureGitignoreHasLockfile() → AcquireLock() → SimpleLock { path, *os.File } → Unlock(lock)
```

| Function | Contract |
|----------|----------|
| `AcquireLock()` | Opens `.code-reducer.lock` with `O_WRONLY \| O_CREATE \| O_EXCL`. Atomic: if held, open fails immediately. Writes PID, returns `SimpleLock` handle |
| `Unlock(lock)` | Closes fd + `os.Remove` lockfile path. Prevents stale PID files from deadlocking subsequent acquires |
| `EnsureGitignoreHasLockfile()` | Idempotently registers lock artifact in `.gitignore`. Ephemeral—never version-controlled |

### Path Safety: `SafeResolve(path string) (string, error)`
Canonicalizes input path to absolute representation relative to repo root. Performs strict containment check; rejects any traversal (`..`) detected pre-resolution. Returns error if resolved target lies outside boundary or symlink bypass is detected. All I/O operations must pass through this function.

---

## Module 4: `internal/tools` — Repository Verification, Safe I/O, File Discovery

**Boundary:** Low-level repository operations for higher-level analysis modules. Consistent pattern: verify environment → resolve paths safely → read/write with TOCTOU guards → filter against ignore rules. All functions operate on an absolute or virtual repo root resolved once per session.

### Repository Verification
| Function | Contract |
|----------|----------|
| `VerifyGitRepo(repoPath)` | Two preconditions: (1) `git` executable accessible via `$PATH`, (2) `.git/` exists and path resolves to it. Blocks downstream when callers assume git-backed repo |

### Git Operations
- `RunGit(repoPath, args...)` — executes git command inside `repoPath`; returns trimmed stdout + error; stderr discarded
- `GetGitHead(repoPath)` — delegates to `RunGit("rev-parse", "HEAD")`, trims whitespace → current commit hash. Anchors state snapshots for discovery/hashing modules

### Safe File I/O
| Function | Contract |
|----------|----------|
| `ReadFileSafely(virtualPath)` | Resolves virtual (relative-to-repo-root) path to absolute on-disk location; normalizes separators so `./foo`, `../bar/baz`, and `baz` produce identical byte slices for same file |
| `WriteFileSafely(absPath, data)` | TOCTOU-safe: verifies target is not a symlink via `os.Lstat()` before truncating. Returns error if symlink detected (prevents privilege escalation via symlink swap) |

### Path Resolution & Hashing
- `HashRepoRoot(path)` — SHA-256 hex digest of resolved absolute path. Deterministic fingerprint for current repo location; used for caching, diff detection, state anchoring

### File Discovery & Ignore Logic
| Function | Contract |
|----------|----------|
| `LoadGitignore(repoRoot)` | Reads `.gitignore`, parses line-by-line (skips comments + blank lines), returns active ignore patterns in original form (no glob expansion) |
| `ShouldIgnoreFile(relPath)` | Evaluates: custom gitignore patterns, dot-prefixed components, known binary/extension suffixes (.exe, .so), common non-source extensions. Returns true if matched |
| `DiscoverCodeFiles(repoRoot)` | Recursively walks tree collecting high-signal source files (.go, .py, .rs, etc.). Skips build directories (node_modules/, vendor/, __pycache__/), dependency paths by prefix, any entry matching ignore list. Returns flat sorted slice of relative file paths—primary input to analysis pipelines |

---

## Module 5: `internal/engine` — Map-Reduce Pipeline Orchestration

**Boundary:** Implements the three-phase pipeline for automated codebase documentation generation. Owns tree construction, synthesis, and reduction logic. Consumes resolved config (for LLM client instantiation) and operates within `SafeResolve` path safety boundary inherited from `security`.

### Data Structures
| Type | Contract |
|----------|----------|
| `DirNode { Path, Files, Children }` | Represents node in recursive directory tree. Holds absolute path, flat list of immediate files, child subtrees for nested directories |
| `FileChange { Path, Status }` | Single file-change record produced by diffing current state against baseline snapshot. Status: "Added", "Modified", or "Deleted" |
| `Event { Type, Message }` | Logging struct holding event category + human-readable description emitted during execution |

### Tree Construction & Affected Propagation
- `buildTree(filePaths)` — constructs root `*DirNode` from absolute file paths by parsing into directory components; recursively nests children under parent nodes
- `determineAffected / propagateAffected` — walks tree marking directories as affected when any descendant is marked affected, a module doc is missing, or cache entries are empty. Recursive walker bubbles upward from leaf to root ensuring every ancestor of changed files inherits the affected flag

### LLM Client & Communication Layer
| Component | Contract |
|----------|----------|
| `LLMClient` / `NewLLMClient` | Aggregates connection settings + HTTP transport for Ollama-compatible endpoint. Initialized with model ID, base URL, context window size, default timeout |
| `CallLLM / StreamLLM` | Synchronous non-streaming POST returns raw response string or error; streaming initiates request, parses chunks line-by-line, invokes callback per content segment |
| `stripOuterMarkdownFence / CleanJSONResponse / UnmarshalJSONResponse` | Regex-based fence stripping for common LLM output artifacts. `UnmarshalJSONResponse` chains cleaning + unmarshaling with contextual parse-error wrapping |

### Caching Layer
- `FileCacheEntry & MetadataCache` — stores SHA256 hash + facts per file within cache structure; maintains last documented commit ID along with cached files and modules maps
- `loadMetadataCache / saveMetadataCache` — reads metadata JSON from docs directory (returns initialized or parsed `*MetadataCache`); serializes into formatted JSON at specified metadata path
- `computeSHA256(contentPath)` — SHA-256 hash of content at virtual path; returns hex-encoded string for deduplication across re-runs

### Core Pipeline Functions
| Function | Contract |
|----------|----------|
| `synthesizeNode(node, llmClient)` | Traverses directory tree node's children and files; extracts file facts through LLM analysis with SHA256 caching; hierarchically merges all components into consolidated summary. Each leaf analyzed independently; intermediate nodes aggregate descendants' summaries before returning upward |
| `reduceInChunks(items, llmClient)` | Recursively batches code items by character limit; synthesizes via LLM calls to produce architecture summaries. Splits input set into chunks fitting within context window, processes each through `synthesizeNode`, merges results bottom-up until single root summary remains |

### Runner & Orchestration
| Function | Contract |
|----------|----------|
| `Runner / NewRunner(cfg)` | Struct holding engine configuration pointer; primary container for executing code-reduction pipelines |
| `Run(mode, llmClient)` | Orchestrates full execution lifecycle: acquires lock → instantiates LLM client from config → dispatches to context-specific documentation pipeline based on repository root and command type |

---

## Cross-Module Integration Map

```
cmd                    ── CLI surface & orchestration ──▶ internal/config
internal/config        ── resolved Config contract ────▶ internal/engine (LLMClient)
internal/config        ── DefaultIgnores/DefaultIgnoredExtensions ──▶ internal/tools (ShouldIgnoreFile)
internal/tools         ── repo root verification + file discovery ──▶ internal/engine (buildTree input)
security               ── SafeResolve path safety ────▶ ALL I/O in engine, tools
security               ── AcquireLock/Unlock          ──▶ cmd (executeCommand lifecycle guard)
internal/tools         ── git state queries (HEAD)    ──▶ internal/engine (MetadataCache anchoring)
```

**Key principle:** Every module's public contract is a struct or function signature defined in its boundary. No hidden dependencies—`config`, `security`, and `tools` are the three pillars that `cmd` and `engine` build upon.