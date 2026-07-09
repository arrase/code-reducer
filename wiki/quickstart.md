# Quickstart — Code-Reducer Architecture

> Dense, developer-facing reference for understanding system boundaries and module interaction. Not a tutorial. Skip if you already know how to invoke `code-reducer init|update`.

---

## System Boundary: What Code-Reducer Owns

Code-Reducer is a CLI-driven documentation generator that extracts architectural facts from source files and synthesizes them into Markdown via LLM calls. The system boundary is the repository root (`repoRoot`) — everything outside it (network, other repos, user home) is not part of this system's responsibility.

The system operates in two modes: **`init`** (full rebuild) and **`update`** (incremental). State between invocations persists through a JSON-backed metadata cache at `{docsDir}/.metadata.json`.

---

## Module Interaction Map

```
main.go ──► cmd/ (Cobra CLI surface, pre-flight validation, mode routing)
          │
          ▼
    internal/config ──► ResolveConfig() returns *Config
                          │
                          ▼
    internal/engine ──► Runner.Run(callback) ──► orchestrator.go
                          ├──► tree.go (DirNode + change detection)
                          ├──► synthesize.go (per-file extraction → chunking → synthesis)
                          ├──► cache.go (persistent hash/facts/store)
                          └──► client.go (LLM interaction layer)
                          │
                          ▼
    internal/security ──► SimpleLock, SafeResolve (path-traversal prevention)
                          │
                          ▼
    internal/tools ──► ReadFileSafely, WriteFileSafely, VerifyGitRepo, RunGit
```

**Interaction rules:**

| Source Module | Target Module | Mechanism | Notes |
|---|---|---|---|
| `cmd/` | `internal/config` | calls `ConfigExists`, `LoadConfig`, `SaveConfig`, `ResolveConfig` | Config is resolved at runtime, not on init. CLI flags > env vars > YAML file > hardcoded defaults. |
| `cmd/` | `internal/engine` | `engine.NewRunner().Run(callback)` after validation passes | Engine owns the pipeline; cmd only registers commands and validates pre-flight conditions. |
| `internal/engine` | `internal/security` | `security.AcquireLock(repoRoot)`, `SafeResolve` | Lock acquired before any work begins; path resolution used for all file I/O to prevent traversal. |
| `internal/engine` | `internal/tools` | `tools.ReadFileSafely`, `tools.WriteFileSafely`, `tools.VerifyGitRepo`, `tools.RunGit` | All safe filesystem operations go through tools. Git process execution uses `RunGit`. |
| `internal/config` | `internal/security` | None direct | Config module is self-contained for its own I/O. |

---

## Entry Points & Lifecycle

### Init Flow (first-time documentation generation)

1. **`cmd/init.go:Run`** → calls `executeCommand("init")`
2. **Pre-flight validation in `root.go`:** cwd exists, git repo valid, config file present (or `code-reducer setup` invoked), mode routing checks pass
3. **`security.AcquireLock(repoRoot)`** — exclusive lock acquired; failure is fatal and short-circuits everything downstream
4. **`engine.NewRunner().Run(callback)`** with mode `"init"`:
   - Builds full DirNode tree from flat file paths
   - Synthesizes every directory bottom-up via `synthesizeNode`
   - Generates global docs (`architecture.md`, `quickstart.md`) only if at least one module's root dir was affected or files are missing
   - Writes `AGENTS.md` with idempotent append logic

### Update Flow (incremental refresh)

1. **`cmd/update.go:Run`** → calls `executeCommand("update")`
2. Same pre-flight validation as init, plus check that project is already initialized
3. **`engine.Runner.Run(callback)`** with mode `"update"`:
   - Change detection via SHA-256 comparison against cached map (`cache.Files`)
   - Affected directories marked top-down; propagation bottom-up so parents inherit affected status from children
   - Only re-synthesizes paths whose subtree has at least one source of impact
   - Cache pruning for deleted files

### Setup Flow (non-interactive alternative)

**`cmd/setup.go:RunSetupFlow(repoRoot)`** — sole exported identifier. Reads existing config, prompts user in sequence (model ID, Ollama URL, context size, ignore paths/extensions, docs dir), validates input, persists via `config.SaveConfig`. Swallows load errors; write failures exit with message.

---

## Configuration Resolution Rules

The effective configuration merges three sources at runtime: persisted `.code-reducer.yaml`, CLI flags (`--model-id`, `--num-ctx`), and built-in defaults. Priority order is hardcoded defaults < YAML file values < environment variables < CLI flags. Empty string/zero values do not override; only non-empty takes precedence, except model ID where explicit empty is treated as "not set" to allow fallback through the chain.

Config files are stored with Unix permission mode `0600`. Lockfile entry appended to `.gitignore` via O_APPEND|O_CREATE semantics.

---

## LLM Interaction Layer

The engine talks to Ollama-style endpoints through `*LLMClient` in `internal/engine/client.go`. Constructed once per run, reused for every call in that run's lifetime. HTTP client carries a fixed 10-minute timeout; no way to swap after construction.

**Synchronous path — `CallLLM`:** sends complete request, blocks until full response arrives. Non-OK status codes consume up to 1024 bytes and return as `"ollama api error: status %d, response: %s"`.

**Streaming path — `StreamLLM`:** opens streaming connection; each decoded chunk forwarded to injected callback. Empty lines skipped; malformed JSON silently ignored per line. Iteration stops when `chunk.Done == true` or `io.EOF` arrives.

Every call prepends a base system prompt that defines the agent persona and defensive output rules. Task-specific framing appended for `"module_synthesis"` and `"architecture"`.

---

## Chunking & Reduction Pipeline (Unexported Engine Logic)

All synthesis logic lives unexported inside `engine` as `synthesizeNode`: recursion anchor where every directory-level summary is produced by calling this function on each child first, then assembling their summaries plus per-file facts into a final parent summary.

**Per-file processing:** read bytes safely (skip silently if unreadable), compute SHA-256 via `tools.ReadFileSafely`, check cache for hash match to reuse stored facts without re-prompting. If no cache hit, text is chunked with overlap and every extraction step runs across all chunks; results consolidated into one fact per step via `reduceFileFacts`.

**Component assembly:** builds a slice containing one entry per file (prefixed with filename) plus one entry per child summary (prefixed with `"Subsystem"`). If no components exist, the module cache is cleared and an empty string returned. Otherwise merged into single final summary via recursive reduction until only one string remains.

**Infinite-loop guard:** if every item ends up in its own batch and more than one item exists, function silently truncates each proportionally to `maxChars / len(items)` and forces them into a single batch — no error returned.

---

## Concurrency & Safety Observations

No file in any module uses `sync.Mutex`, channels, atomic operations, or equivalent concurrency primitives anywhere except within internal types not visible here. All state mutations occur on a single goroutine's call stack during recursion (`synthesizeNode`). The metadata cache is read/written without locking — race conditions exist but no protection mechanism is present.

No panics: every function returns `(value, error)` or equivalent; no `panic`, `runtime.Goexit()`, or unhandled recover blocks are present in any of the examined files.

---

## Error Handling Summary Across Modules

| Category | Where It Happens | Notes |
|---|---|---|
| **Swallow (silent)** | Config stat errors; ResolveConfig load failure → empty config; LLM malformed JSON per line; `.gitignore` load failures; AGENTS.md read failures; `os.Remove` on stale module files | No wrapping, no sentinel value. Caller receives bool or non-nil config with zero-valued fields. |
| **Wrap with prefix + `%w`** | All YAML parse/marshal/write errors in config; all LLM errors from `CallLLM`; file write failures during synthesis and global doc generation; lock acquisition failure; JSON unmarshal failure | Error chain preserved via `%w`. |
| **Propagate raw / stdout** | Runner.Run returns non-nil error → `"Documentation Run Failed: %v"` to stdout (unlike other fatal paths which use stderr) | Only the runner's final path prints to stdout. All validation failures print to stderr. |
| **Fatal exit code 1** | cwd missing, git repo invalid, config file missing + stdin not terminal, mode routing violation, lock acquisition failure | Varies per condition; all use stderr for error messages. |

---

## External Dependencies (Not Part of System Boundary)

- **`github.com/spf13/cobra`** — CLI framework for command tree construction and flag parsing
- **`gopkg.in/yaml.v3`** — YAML configuration parsing for `.code-reducer.yaml`
- **`sabhiram/go-gitignore`** — Gitignore rule compilation used by `internal/tools`
- **Ollama-style HTTP endpoints** — LLM interaction through `*LLMClient` in `internal/engine`