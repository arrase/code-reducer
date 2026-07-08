# CodeReducer — Wiki / Quickstart

## What is CodeReducer?

**CodeReducer** is a CLI-driven code reduction engine that scans Go repositories and generates/updates Markdown documentation pages for affected directories. It uses BM25 relevance scoring to rank source files, LLM-powered generation to produce docs, and deterministic caching to avoid redundant reprocessing.

---

## System Boundaries & Module Interaction

```
┌─────────────────────────────────────────────────────────┐
│                    CLI Entry Point                        │
│              cmd/ (root.go, init.go, update.go)           │
│         ┌──────┬────────┬────────┬────────┬────────┐     │
│         │init  │update  │setup   │runner  │ tools  │     │
│         └──┬───┴───┬────┴───┬────┴───┬────┴───┬────┘     │
│            │       │        │       │            │        │
│            ▼       ▼        ▼       ▼            ▼        │
│    ┌──────────┐ ┌────────┐ ┌──────┐ ┌────────┐ ┌──────┐ │
│    │config/   │ │engine/ │ │LLM   │ │cache   │ │git / │ │
│    │YAML      │ │BM25    │ │client│ │I/O     │ │tools │ │
│    │resolution│ │ranking │ │resp  │ │metadata│ │fs    │ │
│    └──────────┘ └────────┘ │parse │ └────────┘ └──────┘ │
│                            └──────┘                     │
│                                                     ┌─────┐│
│                    ┌──────────────────────────────▶│sec  ││
│                    │                              │     ││
│                    ▼                              ▼     ││
│            ┌─────────────┐              ┌───────────┐   ││
│            │runner.go    │◀────────────│security/  │   ││
│            │orchestrator │            │path lock  │   ││
│            └─────────────┘              └───────────┘   ││
└─────────────────────────────────────────────────────────┘
```

**Execution flow:** `main.go` → Cobra dispatch (`cmd/`) → `runner.Run(mode)` → LLM client + BM25 ranking + cache I/O → Markdown output. Configuration follows four-tier precedence: **system defaults → YAML file → env vars → CLI flags**.

---

## Quick Start — First Run

```bash
# 1. Initialize configuration (interactive credential setup)
code-reducer init

# 2. Generate wiki pages for the entire repo
code-reducer update

# 3. Override model/context without editing YAML:
code-reducer update --modelId "my-model" --numCtx 4096
```

**Config precedence order:** System defaults → `.code-reducer.yaml` → `os.Getenv(…)` → CLI flags. The resolved config is also propagated to downstream processes via environment variables (`CodeReducerModelIdEnvKey`, `OllamaBaseUrlEnvKey`, etc.).

---

## Key Architectural Decisions

### 1. Two-mode branching
- **`init`** — Scans the repo, generates initial wiki markdown pages for all affected directories.
- **`update`** — Runs git diff, identifies changed files, processes only affected regions. Both modes share the same engine pipeline; they diverge at cache initialization (`loadMetadataCache` returns empty default on miss).

### 2. Security gate before every state mutation
Before any write operation:
1. `SafeResolve` validates paths stay within repo root (blocks `..`, external symlinks)
2. `AcquireLock` acquires `flock(2)` exclusive/shared lock with TOCTOU-safe checks
3. `EnsureGitignoreHasLockfile` ensures `.gitignore` excludes the lock file

### 3. Deterministic caching
- **`MetadataCache`** (JSON on disk) stores per-file SHA256 hashes + generation facts
- **`loadMetadataCache` / `saveMetadataCache`** use atomic write-to-temp + rename for safety
- Subsequent runs skip already-cached files unless content hash changes

### 4. BM25 relevance ranking
- **`Document`** holds tokenized content with term frequencies
- **`FilterFilesBM25`** computes IDF-normalized scores across the corpus; returns top-K paths per query
- **`WrapInXmlDelimiter`** wraps file content in `<file path="…">content</file>` tags to prevent prompt injection

### 5. LLM response parsing
- **`CleanJSONResponse`** strips markdown code fences and extracts first/last `{}` or `[]` boundaries
- **`UnmarshalJSONResponse`** deserializes cleaned text into target structs; returns error on failure
- Malformed responses degrade gracefully (empty string returned)

---

## Module Responsibilities at a Glance

| Package | Role |
|---------|------|
| `cmd/` | Cobra CLI dispatch — `init`, `update`, `setup` subcommands |
| `internal/config/` | YAML persistence, four-tier config resolution, env propagation |
| `internal/engine/` | BM25 ranking, LLM orchestration, cache I/O, change detection |
| `security.go` | Path confinement (`SafeResolve`), `flock(2)` locking (`AcquireLock`), `.gitignore` enforcement |
| `internal/tools/file_tools/` | Safe read/write, recursive source discovery, binary detection, path hashing |
| `internal/tools/git_tools/` | Git command execution, repo validation, HEAD retrieval |

---

## Common Patterns for Developers

1. **Adding a new LLM provider** — Implement the `LLMClient` interface (messages + response parsing) and wire it into `NewRunner(cfg)` which delegates to the engine pipeline
2. **Extending config resolution** — Register new env var keys in the four-tier resolver; ensure corresponding process propagation via `os.Setenv`
3. **Custom file filtering** — Extend `isAllowedFile` (ignore lists + directory/component whitelists + extension allowlists); integration point is `discoverCodeFiles` → `filterChanges`
4. **Debugging BM25 results** — Inspect `FilterFilesBM25` output; term frequencies are pre-computed in the `Document` struct during tokenization phase
5. **Inspecting cache state** — Read `<repoRoot>/<docsDir>/metadata_cache.json` directly; contains last documented commit hash + per-file SHA256 hashes

---

## Error Handling Philosophy

- Config load failures return an empty struct (not nil); callers must inspect the value, not treat nil as "not loaded"
- Cache I/O failures degrade to default/empty state — no partial writes survive due to atomic temp+rename pattern
- LLM response parsing returns empty string on malformed input rather than panicking; downstream code checks for empty responses

---

## Security Boundaries

All filesystem operations are gated behind:
- **Path confinement** (`SafeResolve`) — prevents traversal outside repo root
- **Concurrent locking** (`AcquireLock` + `flock(2)`) — mutual exclusion across processes
- **TOCTOU-safe writes** (`WriteFileSafely`) — symlink check before truncation
- **Git isolation** — lock file excluded from `.gitignore` to prevent non-deterministic tracking

---

## Running the Engine Directly

```go
cfg := config.ResolveConfig(repoRoot, modelIdFlag, numCtxFlag)
runner := engine.NewRunner(cfg)
err := runner.Run("init") // or "update"
```

The `Runner.Run(mode)` method: acquires locks → creates LLM client → loads cache (or fresh default on init) → runs git diff → filters changes → determines affected directories → re-processes only affected files through BM25 + LLM generation → updates metadata cache.