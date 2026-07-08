# Quickstart — Code-Reducer Architecture

## What Is Code-Reducer?

Code-Reducer is a Go-based CLI that manages LLM provider configuration and synthesizes documentation from code repositories via an incremental Map-Reduce pipeline.

```
main.go → cmd.RootCmd.Execute()
    ↓
┌─────────────┐     ┌──────────────┐     ┌──────────────────┐
│  config      │◄────►│   engine     │────►│    security       │
│ (resolution) │     │ (synthesis)  │     │  (isolation/lock) │
└─────────────┘     └──────────────┘     └──────────────────┘
```

## Module Boundaries & Interaction Flow

| Layer | Subsystem | Responsibility | Entry Point |
|-------|-----------|----------------|-------------|
| **CLI** | `cmd` | Command tree, credential gating, interactive setup, update dispatch | `main.go → cmd.RootCmd.Execute()` |
| **Config** | `internal/config` | Detect → load → merge → resolve config across 4 precedence layers (defaults → YAML → env → flags). Persist to `.code-reducer.yaml`. Write tracing env vars for downstream services. | `ResolveConfig(cliFlags, env)` |
| **Engine** | `engine` | Hierarchical Map-Reduce synthesis pipeline: lock acquisition → LLM client instantiation → directory tree construction → recursive module synthesis → global docs + quickstart output. | `Runner.Run(ctx, repoRoot, mode)` |
| **Security** | `security` | Path traversal prevention (`SafeResolve`), process locking via `.code-reducer.lock`, gitignore hygiene for the lockfile. | `AcquireLock()` / `SafeResolve(input)` |
| **Tools** | `tools` | Safe file I/O with TOCTOU protection, recursive code-file discovery (filters build artifacts/locks/binaries), git wrappers (`RunGit`, `GetGitHead`, `VerifyGitRepo`), repo-root SHA-256 fingerprinting. | `DiscoverCodeFiles(root)` / `ReadFileSafely(path)` |

### Data Flow Sequence

1. **Init** — `cmd.init()` wires the Cobra command tree internally (no exported symbols).
2. **Setup gate** — `NeedsCredentialSetup()` reads persisted config state; returns true if model ID is missing → routes to interactive `RunSetupFlow()`.
3. **Resolve** — If credentials exist, flow proceeds: `config.ResolveConfig()` merges defaults/YAML/env/flags into canonical `*Config`, writes it as world-unreadable (`0600`) `.code-reducer.yaml`.
4. **Dispatch** — CLI flags route to either the update command or direct init/update execution.
5. **Lock** — Engine acquires process lock via `AcquireLock()` (PID stored in `.code-reducer.lock`). Ensures gitignore entry exists.
6. **Discover** — `tools.DiscoverCodeFiles(repoRoot)` walks directory tree, filters ignored paths and binaries, returns sorted relative path slice.
7. **Build tree** — File paths become `DirNode` hierarchy; per-file SHA-256 cached in `MetadataCache`.
8. **Synthesize** — `synthesizeNode(ctx, node)` recursively processes children/files: files → markdown synthesis via LLM; directory → `.metadata.json` write + propagated result.
9. **Output** — Root-level synthesis emits global architecture docs and quickstart documentation.

## Entry Points by Use Case

| Goal | Call | Returns |
|------|------|---------|
| Run CLI binary | `cmd.RootCmd.Execute()` | exit 0 (success) / exit 1 (error) |
| Interactive setup | `RunSetupFlow()` | `error` after persisting model ID, Ollama URL, context size, ignore paths, docs dir |
| Incremental update | `updateCmd.Run(cmd, args)` | `error` |
| Full init pipeline | `engine.RunInit(ctx, repoRoot, cfg, onEvent)` | `error`, writes global + quickstart docs |

## Configuration Resolution Layers

```
1. System defaults        (OllamaDefaultBaseURL, OllamaDefaultNumCtx=8192)
    ↓ override
2. .code-reducer.yaml     (LoadConfig → yaml.Unmarshal)
    ↓ override
3. Environment variables   (CodeReducerModelIdEnvKey, OllamaBaseUrlEnvKey, etc.)
    ↓ override
4. CLI flags               (highest precedence)
```

Resolved `*Config` is also propagated to external tracing environment variables so downstream services observe them without separate injection logic.

## Key Safety Contracts

- **Path isolation**: `SafeResolve(input)` canonicalizes input and verifies it resolves strictly within `repo.Root`. Any deviation returns an error immediately — prevents directory traversal.
- **TOCTOU protection**: `WriteFileSally` checks inode stability between resolve and open; refuses to truncate if destination is a symlink.
- **Process locking**: `AcquireLock()` writes PID into `.code-reducer.lock`; concurrent runs block until lock released. Stale state cleaned by `Unlock()`.
- **Git hygiene**: `EnsureGitignoreHasLockfile` appends the lockfile path to `.gitignore` if missing, prevents accidental version control tracking of runtime state.

## Developer Notes

- All CLI wiring is internal-only (`init()` in `cmd` package). No exported symbols for subcommand registration — keeps public API minimal.
- `config.ResolveConfig()` must be called before any engine operation; callers should verify via `NeedsCredentialSetup()` first.
- Engine operations require a valid git repo: `tools.VerifyGitRepo()` runs as admission gate before file discovery and hashing.
- All LLM interactions go through the `LLMClient` abstraction — model ID, base URL, context size are configurable at runtime via config/env/flags.