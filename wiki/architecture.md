# CodeReducer — Global Architecture

## System Boundaries & Module Interaction Map

```
┌─────────────┐     ┌──────────────┐     ┌────────────────┐
│   cmd       │────▶│ internal/    │     │  security      │
│ (CLI Entry) │     │   engine     │◀────│  (guards)      │
│             │     │              │     │                │
│ • RootCmd   │     │ • BM25 rank  │     │ • SafeResolve  │
│ • Init cmd  │     │ • LLM client │     │ • AcquireLock  │
│ • Update    │     │ • Caching    │     │ • Gitignore    │
│ • Setup     │     │ • Change det │     │                │
└──────┬──────┘     └──────┬───────┘     └────────────────┘
       │                   │              ▲
       ▼                   ▼              │
┌─────────────┐     ┌──────────────┐     │
│ internal/   │     │  config      │◀────┘
│    tools    │     │ (4-tier)    │
│             │     │ • YAML load │
│ • File ops  │     │ • Env var   │
│ • Git ops   │     │ • CLI flags │
│ • Hashing   │     └──────────────┘
│ • Discovery │
└─────────────┘
```

**Core contract:** All subsystems resolve paths through `internal/tools` (safe read/write), validate repository boundaries via `security`, and load configuration from the 4-tier chain defined in `config`. Engine owns execution orchestration; cmd owns CLI dispatch.

---

## Data Flow — Initialization (`init`) Mode

```
User → RootCmd --mode="init"→ executeCommand()
       │
       ├─ git_tools.VerifyGitRepo(repoRoot)          [security gate]
       ├─ config.LoadConfig(cwd)                     [4-tier resolution]
       └─ config.ConfigExists(cwd)?                 → RunSetupFlow() if missing
                                                    │
                                            setup.go:RunSetupFlow()
                                                    │
                                                    ├─ Interactive prompts (TUI)
                                                    └─ config.SaveConfig(cwd, cfg)

Engine.Run("init"):
  │
  ├─ security.SafeResolve(repoRoot, path...)         [path confinement]
  ├─ tools.DiscoverCodeFiles(repoRoot)               → corpus for BM25
  ├─ engine.FilterFilesBM25(query, documents)        → ranked file list
  ├─ parseGitDiff("git diff HEAD")                   → []FileChange
  ├─ determineAffected(dirTree, changes, cache)      → affected dirs map
  ├─ For each affected dir:
  │   ├─ tools.ReadFileSafely(path)                 → Document
  │   ├─ engine.WrapInXmlDelimiter(content)         → injection mitigation
  │   └─ LLMClient.Chat(messages)                   → generated doc content
  ├─ tools.WriteFileSafely(docPath, output)         [TOCTOU-safe write]
  └─ saveMetadataCache(repoRoot, docsDir, cache)    [atomic JSON write]
```

---

## Data Flow — Update Mode

Same pipeline as init, except:

- `config.LoadConfig(cwd)` returns existing state (no setup flow triggered)
- `parseGitDiff` captures only post-init changes
- LLM client re-processes **only** files whose SHA256 differs from cached hash (`metadata_cache.json`)
- `Run("update")` skips credential setup gating

---

## Configuration Resolution Pipeline

```
System Defaults (hardcoded)
    │  OllamaDefaultBaseURL = "http://localhost:11434"
    │  OllamaDefaultNumCtx   = 8192
    ▼
YAML File (.code-reducer.yaml) — if present, unmarshal into Config struct
    │  Falls back to empty struct on read failure (no error returned)
    ▼
Environment Variables (os.Getenv checks):
    CodeReducerModelIdEnvKey, OllamaBaseUrlEnvKey, OllamaNumCtxEnvKey,
    LangsmithApiKeyEnvKey, LangchainProjectEnvKey, LangchainTracingEnvKey
    │  Each applied only when non-empty; empty leaves prior value intact
    ▼
CLI Flags (positional args) — highest priority override
```

Final `*Config` is returned. Resolved tracing keys are propagated to OS process via `os.Setenv()` so downstream packages observe them without explicit dependency on config module.

---

## Security Layer — Pre-Flight Guards

All three primitives execute before any state-modifying operation:

| Guard | Mechanism | Failure Mode |
|---|---|---|
| **SafeResolve** | Canonicalize path, verify resolved target is descendant of repo root | Rejects `..` traversal and external symlinks |
| **AcquireLock** | Opens lockfile, TOCTOU-safe symlink check, then `flock(2)` exclusive/shared | Returns error; no partial state left |
| **EnsureGitignoreHasLockfile** | Ensures `.gitignore` exists with lockfile entry | Idempotent; writes if missing |

---

## Tools Subsystem — Low-Level Primitives

All functions resolve virtual paths to absolute filesystem locations:

- `ReadFileSafely(path)` → `[]byte + error`
- `WriteFileSafely(path, data)` → TOCTOU-safe (checks not-symlink before truncating)
- `DiscoverCodeFiles(root)` → recursive walk with exclusion rules (binaries, locks, build artifacts)
- `ShouldIgnorePath(relPath, ignores)` → 4-match strategies: exact, prefix, component, glob (`*`, `?`, `[...]`)
- `IsBinaryFile(path)` → scans first 1024 bytes for null byte
- `RunGit(cmd, dir)` → in-process git execution with `--no-pager`

---

## Engine Subsystem — Core Pipeline Components

### BM25 Ranking (`context.go`)

```go
type Document struct {
    Content            string
    TermFrequencies    map[string]int  // token → count for IDF weighting
    TokenCount         int             // length normalization factor
}
```

`FilterFilesBM25(query, documents)`: tokenize corpus → compute term frequencies → apply BM25 formula with normalized IDF and `avgdl` length normalization → return top-K file paths.

### Caching (`engine.go`)

`MetadataCache` is a JSON-deserializable struct holding: last documented commit hash, per-file SHA256 + facts map, and module mappings. Persisted to `<repoRoot>/<docsDir>/metadata_cache.json` via atomic write-to-temp + rename.

**Change detection:** `parseGitDiff(raw) → []FileChange` categorizes adds/deletes/renames/copies/modifications. `determineAffected(dirNode, changes, cache)` checks changed files against module mappings; `propagateAffected(node)` recursively marks ancestor directories when any descendant is affected.

### LLM Integration (`engine.go` + `json_parser.go`)

`LLMClient` holds model ID, base URL, context limit, and HTTP client (10-minute default timeout). Messages use role-structured JSON: `{Role: "system"|"user"|"assistant", Content}`.

`CleanJSONResponse(raw)`: extracts JSON from markdown code fences or finds first/last matching brace pair in raw text → returns empty string on malformed input (graceful degradation). `UnmarshalJSONResponse(cleaned, target)` deserializes into the provided interface value; errors returned if deserialization fails.

### Runner (`runner.go`)

`Runner` holds engine config reference. `NewRunner(cfg *Config)` initializes instance bound to a specific config pointer. `Run(mode string) error`:

1. Acquire repository lock (mutex or file-based per environment)
2. Create LLM client via `NewLLMClient`
3. Branch on mode: `"init"` vs update
4. Load metadata cache from disk (or create fresh for init)
5. Run git diff → filter changes → determine affected directories
6. Re-process only affected files through BM25 ranking + LLM generation
7. Update metadata cache with new SHA256 hashes and facts

---

## Entry Point & CLI Hierarchy

```
main.go (package-level entry)
  │
  ▼
cmd.RootCmd (Cobra instance; subcommands registered at package init)
  │
  ├─ cmd.init() registers InitCmd → triggers repo scan + wiki page generation
  └─ executeCommand(mode string) error:
       │
       ├─ git_tools.VerifyGitRepo(repoRoot)        [gate #1]
       ├─ config.ResolveConfig(...)               [gate #2 — full resolution chain]
       ├─ if NeedsCredentialSetup(cfg)?            → RunSetupFlow()  [gate #3]
       └─ engine.Run(mode)                         [execution delegation]
```

`update.go` contains no exported identifiers; all update-specific logic is scoped to the `cmd` package.

---

## Module Dependency Summary

| Consumer | Imports From | Purpose |
|---|---|---|
| **cmd** | config, engine, security, tools | CLI dispatch, mode branching, credential gating |
| **engine** | config, tools, security, LLM client | Pipeline orchestration, BM25 ranking, caching, change detection |
| **security** | (standalone) | Path confinement, locking primitives |
| **tools** | (standalone primitives) | File ops, git execution, hashing, discovery |
| **config** | (standalone resolution) | 4-tier config loading, persistence, env propagation |

---

## Key Invariants

- All file writes use atomic patterns (temp + rename) to prevent TOCTOU races.
- Repository root is the immutable confinement boundary; `SafeResolve` enforces this before any filesystem operation.
- Configuration resolution never returns errors for missing/empty files—callers must inspect returned struct values.
- LLM responses are sanitized via XML delimiters and JSON extraction before deserialization to prevent injection attacks.
- Metadata cache is the single source of truth for file state between runs; SHA256 fingerprints enable incremental reprocessing without full corpus scan.