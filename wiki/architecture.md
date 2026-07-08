# Architecture Overview — Code-Reducer

## System Boundaries

Code-Reducer is a Map-Reduce pipeline that reads source code from a git repository, analyzes it via LLM calls against an Ollama-compatible endpoint, and writes back documentation markdown files into the same repo. The system has four hard boundaries:

| Boundary | What lives inside | What flows out |
|----------|-------------------|----------------|
| **`cmd`** (CLI) | Cobra root + subcommands (`init`, `update`) | Dispatches to engine via config; never touches code files directly |
| **`internal/config`** | Resolved configuration struct, env-var/CLI merging logic | Returns a single `Config` pointer consumed by every downstream component |
| **`security`** (path safety) | Lockfile acquisition/release, path containment enforcement | All I/O paths must pass through `SafeResolve`; lock state is process-local |
| **`internal/tools`** | Git helpers, safe file read/write, ignore rules, repo root hashing | Provides the verified environment contract (`VerifyGitRepo`) and filtered file lists to engine |
| **`internal/engine`** | Dir-tree construction, LLM synthesis, caching, reduce aggregation | Writes `.wiki/architecture.md` + quickstart; emits `Event` logs during execution |

The CLI is the only entry point. It never writes documentation directly — it orchestrates: verify repo → acquire lock → load config → run engine pipeline. The engine is the only component that touches source files and produces output docs. Configuration is a single resolved struct flowing through every stage; there are no per-stage partial configs.

## Shared Contracts

Every module agrees on three contracts before they interact:

### 1. Repository Root Contract
`tools/VerifyGitRepo(repoPath)` returns `true` only when `$PATH` contains a `git` binary and `.git/` exists at the given path. Every function that operates on files — whether it's reading, writing, hashing, or discovering — starts from this canonical root. The engine builds its `DirNode` tree using absolute paths derived from this root; the CLI never passes a non-verified path to the engine.

### 2. Path Containment Contract
All file-system operations flow through `security.SafeResolve(path)`. It takes an input path, canonicalizes it against the repo root, and errors if the resolved target escapes outside or contains `..` components pre-resolution. This is a defense-in-depth layer: even if a downstream module constructs a malicious relative path, `SafeResolve` rejects it before any OS call.

### 3. Configuration Contract
The config contract is asymmetric: **write** happens once at setup via `config.SaveConfig(cfg)` (0600 permissions), and **read** happens every execution via `config.ResolveConfig()` which re-merges YAML → env vars → CLI flags → defaults into a single struct. The resolved struct is the only source of truth downstream; there are no stale partial configs in flight.

## Module Interaction Map

```
User → cmd (Cobra)
       │
       ├── initCmd  ── scans repo, writes .wiki/* pages
       ├── updateCmd ── diffs against last documented commit, regenerates .wiki/*
       └── executeCommand(mode)
                    │
            ┌──────┴────────┐
            ▼                ▼
      security.AcquireLock  tools.VerifyGitRepo(repoPath)
            │                  │
            ▼                  ▼
    config.ResolveConfig()   (verified env contract established)
            │
            ▼
    engine.Runner.Run(cfg, repoRoot, mode)
```

The lock is acquired first so that no concurrent Code-Reducer process can corrupt in-flight state. Verification happens next because all subsequent operations depend on a valid git tree. Configuration resolution follows because the engine needs it for LLM calls and output paths. The engine then owns everything after — tree building, file discovery, synthesis, caching, reduce aggregation.

## Data Flow Through the Pipeline

### Init Path
`initCmd` → `engine.RunInit()` → `discoverCodeFiles(repoRoot)` (tools) → filtered by `ShouldIgnoreFile` → recursive walk builds `DirNode` tree (`buildTree`) → `synthesizeNode` traverses children and files, calling LLM with SHA256 caching (`computeSHA256` + `MetadataCache`) for deduplication across re-runs → `reduceInChunks` batches oversized content into context-window-sized chunks, processes each through synthesis, merges bottom-up until a single root summary remains → global architecture doc + quickstart written to `.wiki/`.

### Update Path
`updateCmd` → `GetGitHead(repoPath)` (tools) returns last documented commit hash → engine diffs current state against that baseline → produces `FileChange` records (`Added`, `Modified`, `Deleted`) → only affected files propagate upward via `propagateAffected`; unaffected subtrees are skipped entirely, which is the core efficiency gain of update mode.

### LLM Communication
Two call modes: synchronous (`CallLLM` — HTTP POST) and streaming (`StreamLLM` — chunk-by-chunk callback). Both go through the same response-cleaning pipeline: `stripOuterMarkdownFence` removes markdown code fences, then either raw text or JSON parsing depending on caller. System prompts are context-specific; `GetDefaultSystemPrompt` returns different instructions per command mode so synthesis behaves differently for init vs update.

### Caching
`MetadataCache` persists the last documented commit ID plus maps of file paths → SHA256 hashes and module paths → doc summaries, stored as `.metadata.json`. On each run, `loadMetadataCache` re-reads it; `computeSHA256` recalculates file hashes for changed files. This enables incremental synthesis: unchanged files reuse cached LLM results instead of re-prompting the model.

### Lock Lifecycle
`AcquireLock()` opens `.code-reducer.lock` with `O_WRONLY | O_CREATE | O_EXCL`. The atomic open fails immediately if another instance holds it — no polling, no retry logic, just hard failure. On success, the PID is written and flushed. `Unlock(lock)` closes the fd and removes the file. `EnsureGitignoreHasLockfile()` registers the lock artifact in `.gitignore` so it stays ephemeral and never gets committed.

## Conventions & Patterns

- **Pointer semantics for optional config sections**: `Model`, `Tracing` are pointers to allow zero-value YAML marshaling — unset fields don't appear in the file.
- **All I/O passes through safe wrappers**: `ReadFileSafely` normalizes separators and canonicalizes paths; `WriteFileSafely` checks for symlinks before truncating (TOCTOU guard against privilege escalation).
- **Ignore rules are layered**: custom `.gitignore` patterns + dot-prefix components + binary/extension suffixes. The engine consults all three via `ShouldIgnoreFile`.
- **Deterministic discovery output**: `DiscoverCodeFiles` returns paths sorted for stable ordering across runs, which is critical because synthesis depends on consistent tree structure.
- **Event logging**: every engine phase emits an `Event{Type, Message}` struct — useful for debugging and future observability integration without changing the pipeline logic.

## Failure Modes & Recovery

| Scenario | Behavior |
|----------|----------|
| No git binary in `$PATH` | `VerifyGitRepo` errors immediately; CLI surfaces this before any engine work |
| Lock already held by another process | `AcquireLock` fails atomically; user sees "already running" error |
| Config file missing | Setup flow triggers; user guided through interactive prompts to create `.code-reducer.yaml` |
| LLM call returns non-JSON when JSON expected | `UnmarshalJSONResponse` chain: fence strip → extract delimiters → unmarshal; parse errors wrapped with context |
| File changed between read and write (TOCTOU) | `WriteFileSafely` detects symlink replacement; returns error instead of silently overwriting target |
| Path escapes repo root during I/O | `SafeResolve` rejects pre-resolution; no OS call is made for escaped paths |

## Key Files on Disk

- `.code-reducer.yaml` — user config (0600 permissions, owner-only)
- `.code-reducer.lock` — process slot marker (created only when running)
- `.gitignore` — lockfile registered here to stay out of version control
- `.metadata.json` — synthesis cache (last commit ID + file/module hashes)
- `.wiki/*.md` — generated documentation output