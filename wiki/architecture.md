# Architecture — Code Reducer

## 1. System Boundaries

**Code Reducer** is a Go-based CLI tool that generates and maintains automated documentation (wiki pages) from repository state via LLM inference. It operates as a single-process, lock-serialized agent: every run acquires an exclusive flock on a dedicated lockfile before performing any filesystem or Git operation, ensuring no two invocations can mutate the documentation store concurrently.

The application has three external dependencies:
- **Ollama** (default base URL `http://localhost:11434`) — LLM inference backend; configurable via config file, environment variable, or CLI flag.
- **Git** — source of truth for repository state; used to enumerate changed files and compute commit SHAs.
- **LangSmith / LangChain v2** (optional) — tracing instrumentation providers, injected into the process environment when enabled in configuration.

The tool runs on any OS that provides `flock(2)` semantics or a compatible lock primitive. It does not require network access beyond Ollama; all LLM calls are HTTP/JSON over a configurable base URL with a 10-minute timeout per request and retry logic for transient failures.

## 2. Initialization Order & Module Interaction

Execution follows a strict dependency chain: `security` → `tools` → `config` → `engine`, with `cmd` at the top bridging user input to engine execution. Every subsystem that touches repository state consults `security` first; every subsystem that performs filesystem I/O relies on `tools`; every subsystem that needs runtime parameters reads from `config`; the `engine` orchestrates the full pipeline and calls back into all three.

```
┌─────────────────────────────────────────────────┐
│  cmd (Cobra CLI)                                 │
│    └── RootCmd / updateCmd                       │
│          ├── config resolution                   │
│          ├── signal handling                     │
│          └── Runner.Run()                        │
├─────────────────────────────────────────────────┤
│  engine (orchestrator)                           │
│    ├── runner.go    — lock acquisition, mode dispatch │
│    ├── context.go   — tokenization, BM25 indexing  │
│    ├── engine.go    — diff parsing, cache I/O       │
│    ├── json_parser.go — response cleaning           │
│    └── LLMClient      — HTTP chat API wrapper        │
├─────────────────────────────────────────────────┤
│  config (runtime parameters)                     │
│    ├── ResolveConfig() — 4-tier precedence         │
│    ├── SaveConfig()   — atomic YAML write          │
│    └── propagation    — os.Setenv for downstream   │
├─────────────────────────────────────────────────┤
│  security (confinement & locking)                │
│    ├── SafeResolve     — path traversal guard       │
│    ├── AcquireLock     — flock(2) mutual exclusion  │
│    └── EnsureGitignoreHasLockfile                  │
├─────────────────────────────────────────────────┤
│  tools (low-level primitives)                    │
│    ├── ReadFileSafely / WriteFileSafely           │
│    ├── DiscoverCodeFiles — recursive walk + filters│
│    ├── RunGit / GetGitHead                        │
│    └── HashRepoRoot   — SHA-256 of absolute path  │
└─────────────────────────────────────────────────┘
```

### Data Flow: A Typical `update` Run

1. **CLI entry** (`cmd/root.go`) parses flags, checks for credential setup, and dispatches to `executeCommand`.
2. **Config resolution** (`config/ResolveConfig`) merges defaults → YAML file → env vars → CLI flags into a single `*Config`. Tracing keys are propagated via `os.Setenv` so downstream packages (LangSmith/LangChain) observe them without explicit dependency on this module.
3. **Lock acquisition** (`security.AcquireLock`) serializes concurrent runs by flocking the lockfile; `.gitignore` is verified to contain the lock entry.
4. **Repository validation** — `tools.VerifyGitRepo` ensures a valid git working tree exists.
5. **Diff parsing** (`engine.parseGitDiff`) turns raw `git diff` output into a slice of `FileChange` records (Added/Modified/Deleted/Renamed/Copied).
6. **Affected node detection** — `determineAffected` walks the directory tree, marking directories as affected whenever any child is changed; parent propagation ensures structural changes invalidate ancestor documentation entries.
7. **BM25 indexing** (`context.FilterFilesBM25`) ranks repository files by relevance to a query string using term frequency statistics from tokenized content; top-K results form the context for LLM inference.
8. **LLM call** — `LLMClient.CallLLM` streams the response with retry logic, accumulating chunks until completion or exhaustion of retries.
9. **Response parsing** (`json_parser`) strips markdown fences and extracts JSON via brace-matching; `UnmarshalJSONResponse` deserializes into typed structs for downstream use.
10. **Cache persistence** — `saveMetadataCache` writes `.metadata.json`; the next run reads it back to avoid reprocessing unchanged entities.
11. **Commit** — Git HEAD SHA is updated in config upon success; lock released.

## 3. Cache & State Persistence

Three state files live at the repository root:

| File | Purpose | Format |
|---|---|---|
| `.code-reducer.yaml` | Runtime configuration (model ID, Ollama URL, tracing flags) | YAML |
| `.metadata.json` | Last documented commit SHA + file/module cache map | JSON |
| `<lockfile>` | Flock target for mutual exclusion | OS lock |

The lock is ignored by git (`EnsureGitignoreHasLockfile` ensures the entry exists in `.gitignore`). Config and metadata files are written atomically with `0600` permissions; config loads return an empty struct on failure rather than erroring, so callers must inspect the returned value.

## 4. Configuration Precedence (Four-Tier Chain)

```
1. System defaults   — OllamaDefaultBaseURL, OllamaDefaultNumCtx
2. YAML file (.code-reducer.yaml) — unmarshaled if present; missing → empty struct
3. Environment variables — CodeReducerModelIdEnvKey, OllamaBaseUrlEnvKey, etc.
4. CLI flags            — --model-id, --num-ctx (highest priority)
```

`ResolveConfig(repoRoot, modelIdFlag, numCtxFlag)` returns the merged `*Config`. The YAML file is detected via `config.ConfigExists(cwd)`; a missing or unreadable file yields an empty struct with no error. On success, tracing-related env vars are propagated to the process so downstream binaries inherit them automatically.

## 5. Security Model

Every filesystem operation targeting repository resources passes through `security.SafeResolve`, which validates that resolved paths stay within the repo root and rejects path traversal components (`..`) at any resolution stage. Lock acquisition uses `flock(2)` with TOCTOU-safe checks: before truncation, the target is verified not to be a symlink (preventing hijack). `.gitignore` is maintained to exclude lock files from version control tracking.

## 6. LLM Interaction Contract

The engine abstracts LLM access behind `LLMClient`, which holds model identity and transport parameters. The client:
- Uses HTTP with a 10-minute timeout per request.
- Implements retry logic for connection resets, rate limits, and transient errors.
- Returns accumulated streamed chunks as a single string; partial responses are discarded only after retries exhaust.

Response parsing handles common model behaviors where structured output is wrapped in conversational scaffolding (markdown fences, prefix/suffix text). `CleanJSONResponse` extracts JSON by either stripping fences or finding the first/last brace pair.

## 7. CLI Surface Area

The root command registers two persistent global flags (`--model-id`, `--num-ctx`) via package-level `init()`. Subcommands:
- **update** — syncs wiki pages against repository changes since last documented commit; dispatches through `executeCommand` for full orchestration.
- **setup** (implicit) — triggered when no config file exists; guides the user interactively to generate `.code-reducer.yaml`.

Signal handling installs SIGINT/SIGTERM handlers that gracefully terminate engine execution during long-running LLM calls or diff parsing.

## 8. Key Design Decisions

- **Lock-first**: All write operations acquire an exclusive flock before any filesystem access, preventing concurrent mutation of the documentation store and metadata cache.
- **Fail-safe defaults**: Config loads return empty structs on failure; BM25 indexing returns top-K with K clamped by context size; tokenization uses a compiled regex for performance.
- **Path confinement**: Every repo-accessing operation is gated by `SafeResolve`, eliminating path traversal vectors at the entry point of every subsystem that touches repository state.
- **Propagated env vars**: Tracing configuration keys are written to process environment on resolve, so downstream packages (LangSmith/LangChain) inherit them without needing explicit dependency on this module.