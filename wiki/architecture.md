# Global Architecture Overview

## System Boundaries

Code-Reducer is a Go-based CLI application that manages LLM provider configuration and synthesizes documentation from code repositories through an incremental Map-Reduce pipeline. The system boundary encompasses:

- **CLI layer** (`cmd/`) — Cobra command tree, credential collection, and command dispatch
- **Configuration layer** (`config/`) — Multi-source resolution (YAML → env vars → CLI flags) with persistence to `.code-reducer.yaml`
- **Engine layer** (`engine/`) — Hierarchical Map-Reduce synthesis pipeline that discovers code files, constructs a directory tree via `DirNode`, and synthesizes modules recursively through LLM calls
- **Security layer** (`security/`) — Path traversal prevention (`SafeResolve`) and process locking (`SimpleLock` / `.code-reducer.lock`)
- **Tools layer** (`tools/`) — Repository utilities: safe file I/O with TOCTOU protection, code file discovery, git wrappers, SHA-256 hashing

## Data Flow

```
main.go → cmd.RootCmd.Execute()
  │
  ├── config.ResolveConfig(cliFlags, env vars) → *config.Config
  │     (system defaults → YAML → env vars → CLI flags; writes to tracing env vars)
  │
  ├── security.SafeResolve(input) → string | error
  │     (canonicalizes path; rejects traversal outside repo root)
  │
  ├── tools.DiscoverCodeFiles(repoRoot) → []string
  │     (recursive walk; filters build artifacts, deps, cache, binaries, ignores)
  │
  ├── engine.Runner.Run(ctx, repoRoot, mode, onEvent)
  │     ├── AcquireLock() → *SimpleLock | error
  │     │     └── security.Lock lifecycle (.code-reducer.lock)
  │     ├── NewLLMClient(cfg) → *LLMClient
  │     │     └── Ollama HTTP client with 10-min timeout
  │     ├── synthesizeNode(ctx, node, cfg, cache, affectedDirs)
  │     │     ├── computeSHA256(path) for file metadata caching
  │     │     ├── reduceInChunks(ctx, c, nodePath, items, logEvent) → string | error
  │     │     │     └── chunks code files into LLM context window batches
  │     │     ├── synthesizeNode recursively processes children + files
  │     │     ├── writes per-directory markdown to docsDir
  │     │     └── propagates synthesized output upward
  │     └── mode == "init" → RunInit(ctx, repoRoot, cfg, onEvent)
  │           ├── builds full DirNode tree from discovered files
  │           ├── synthesizes all modules hierarchically
  │           ├── generates global architecture docs (wiki/architecture.md)
  │           └── writes quickstart documentation
  │
  └── config.SaveConfig(config, dir) → error
        (persisted as .code-reducer.yaml with mode 0600)
```

## Module Interaction Map

| Source | Destination | Mechanism |
|---|---|---|
| `main.go` | `cmd/` | Delegates to `RootCmd.Execute()`; exit status 1 on error, 0 on success |
| `config.ResolveConfig` | `engine/*LLMClient` | Resolved config feeds model ID, base URL, context size into LLM client factory |
| `security.SafeResolve` | `engine/synthesizeNode` | Validates all user-supplied paths stay within repo root before engine processes them |
| `tools.DiscoverCodeFiles` | `engine.RunInit` / `synthesizeNode` | Provides sorted file list that the tree builder consumes to construct `DirNode` hierarchy |
| `engine.LLMClient.CallLLM` / `StreamLLM` | `config.ResolveConfig` | LLM receives system prompt from `GetDefaultSystemPrompt`; resolved config propagates to tracing env vars |
| `security.AcquireLock` → `.code-reducer.lock` | `gitignore` via `EnsureGitignoreHasLockfile` | Lock file is added to `.gitignore` so runtime state stays out of version control history |
| `tools.RunGit` / `GetGitHead` | `engine.loadMetadataCache` | Git HEAD hash feeds the metadata cache's "last documented commit" field for diff detection |
| `tools.HashRepoRoot` | `engine.MetadataCache` | SHA-256 of repo root fingerprints repository snapshot state |

## Operational Modes

The system operates in two distinct modes:

1. **Setup mode** — invoked when `NeedsCredentialSetup()` returns `true` (no model ID resolved). Runs `RunSetupFlow()` to collect LLM Model ID, Ollama Base URL, context size limit, ignored paths, and docs directory path. Persists via `config.SaveConfig`.

2. **Runtime mode** — dispatched directly when configuration is already valid. Supports two sub-commands:
   - **`update`** — incremental documentation updates by scanning files changed since the last documented commit (diff metadata from git state).
   - **`init`** (via `RunInit`) — full pipeline that discovers all code files, builds directory tree, synthesizes modules hierarchically, and generates global architecture + quickstart docs.

## Configuration Resolution Order

```
system defaults (OllamaDefaultBaseURL / OllamaDefaultNumCtx)
  ↓
YAML file (.code-reducer.yaml via LoadConfig)
  ↓
Environment variables (CodeReducerModelIdEnvKey / OllamaBaseUrlEnvKey / etc.)
  ↓
CLI flags (highest precedence)
```

Resolved config is written back to external tracing environment variables so downstream services observe consistent configuration without separate injection.