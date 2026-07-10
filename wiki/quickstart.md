# quickstart.md

> Last updated: 2026-05-18  
> Maintained by: Code-Reducer agent (auto-generated)

---

## What is Code-Reducer?

Code-Reducer is a terminal-based documentation-generation tool for Go projects. It produces and maintains technical wikis — `architecture.md`, `quickstart.md`, per-module API summaries under `modules/` — from your source code by invoking an Ollama-compatible LLM endpoint.

---

## Quick Start

### Prerequisites

- **Go 1.21+** (standard library only)
- An **Ollama-compatible** local LLM server running and reachable at the configured base URL
- A working terminal (TTY required for interactive setup; non-TTY requires manual config persistence)

### First Run — Interactive Setup

```bash
cd <your-go-project>
code-reducer setup
```

This prompts you for:

| Prompt | Default | Notes |
|---|---|---|
| LLM Model ID | `ornith:9b` | Overrides via env var `CODE_REDUCER_MODEL_ID` |
| Ollama Base URL | `http://localhost:11434` | Overrides via env var `OLLAMA_BASE_URL` |
| Context Size (num-ctx) | `8192` | Overrides via env var `OLLAMA_NUM_CTX`; must parse as positive int |
| Docs directory | `wiki/` | Custom docs output path |
| Ignore paths/files | none | Comma-separated absolute paths excluded from traversal |
| File extensions to ignore | none | Comma-separated file extensions excluded from traversal |

Pressing **Enter** on any prompt preserves the existing or default value. Pressing `clear` or `none` wipes list-type inputs entirely. Stdin read failures log to stderr and continue using current values — no rollback occurs.

After setup completes, a `.code-reducer.yaml` file is written with 0600 permissions in your working directory.

### First Run — Manual Configuration (CI / Non-TTY)

```bash
export CODE_REDUCER_MODEL_ID="ornith:9b"
export OLLAMA_BASE_URL="http://localhost:11434"
export OLLAMA_NUM_CTX=8192

code-reducer init
```

### Subsequent Runs — Incremental Update

```bash
code-reducer update
```

This performs change detection (SHA-256 per file), regenerates only affected modules, and reuses existing `architecture.md`/`quickstart.md` unless the root-level code changed. If no changes are detected, it exits with "up to date" status.

### Full Regeneration — Init Mode

```bash
code-reducer init
```

Regenerates all documentation regardless of cache state: architecture, quickstart, per-module summaries, and `AGENTS.md`.

---

## Configuration Precedence

The tool resolves configuration through a four-tier pipeline. Each layer overwrites the previous when present; missing layers are skipped transparently.

| Layer | Source | Mechanism |
|---|---|---|
| 1 (lowest) | Built-in constants | `ornith:9b`, `http://localhost:11434`, `8192` |
| 2 | `.code-reducer.yaml` | Read via `config.LoadConfig`; non-existent file treated as empty config |
| 3 | Environment variables | `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX` (non-empty only) |
| 4 (highest) | CLI flags | `--model-id`, `--num-ctx` (`strconv.Atoi`; non-positive values silently retain prior layer) |

### Environment Variable Defaults

```bash
export CODE_REDUCER_MODEL_ID="ornith:9b"
export OLLAMA_BASE_URL="http://localhost:11434"
export OLLAMA_NUM_CTX=8192
```

---

## Output Artifacts

| File | Purpose | Regenerated On |
|---|---|---|
| `architecture.md` | Global architecture overview | Root-level code change, or not present on disk |
| `quickstart.md` | Onboarding guide | Root-level code change, or not present on disk |
| `modules/<module>/*.md` | Per-module API summaries | File hash mismatch (Added/Modified) for that module |
| `AGENTS.md` | AI Agent Guidelines header | First run; appended to if existing file lacks the marker |

Per-file operations (`Added`, `Modified`, `Deleted`) are tracked internally and drive selective regeneration. Stale modules whose directories no longer exist in the codebase are pruned silently — their markdown files removed without error logging.

---

## System Architecture Overview

```
┌───────────────────────────────────────────────────┐
│                   cmd.RootCmd.Execute()            │
│              (entry point: all lifecycle)           │
├───────────────────────────────────────────────────┤
│  init.go / update.go                              │
│  (Cobra subcommand registration only)              │
├───────────────────────────────────────────────────┤
│  cmd.RootCmd                                      │
│  ┌─────┐    ┌─────┐    ┌─────┐                   │
│  │--model-id│ │--num-ctx│ │setup/│                 │
│  └─────┘    └─────┘    └─────┘                   │
├───────────────────────────────────────────────────┤
│  internal/config                                  │
│  ResolveConfig() → *config.Config                 │
│  (4-tier: constants → YAML → env → flags)         │
├───────────────────────────────────────────────────┤
│  internal/engine                                   │
│  ┌────────────┐    ┌──────────────────────────┐   │
│  │ runner.go  │    │ orchestrator.go          │   │
│  │ Run()      │────▶│ RunInit / RunUpdate     │   │
│  └────────────┘    └──────────────────────────┘   │
│                                                      │
│  ┌─────────────┐  ┌──────────────┐  ┌───────────┐  │
│  │ tree.go     │  │ synthesize.go │  │ chunking.go│  │
│  │ buildTree / │  │ reduceInChunks│ │ reduceFile │  │
│  │ propagate   │  │ reduceItems   │ │ facts      │  │
│  └─────────────┘  └──────────────┘  └───────────┘  │
│                                                      │
│  ┌──────────────┐                                    │
│  │ LLMClient    │  CallLLM → /api/chat              │
│  │ (10m timeout)│  Response parsed via json_parser   │
│  └──────────────┘  StripOuterMarkdownFence           │
├───────────────────────────────────────────────────┤
│  external packages:                                │
│  security.AcquireLock()    → repo-level lockfile    │
│  cache.loadMetadataCache() → on-disk state          │
│  tools.{LoadGitignore,DiscoverCodeFiles,...}       │
│  tools.ReadFileSafely / WriteFileSafely            │
└───────────────────────────────────────────────────┘
```

### How the Modules Interact

1. **`cmd.RootCmd.Execute()`** resolves configuration and dispatches to `init`, `update`, or `setup`. Cobra output suppression is enabled — errors propagate as values; no panics occur within this package.

2. **`internal/config.ResolveConfig()`** is the single source of truth for runtime configuration. It layers defaults, persisted YAML config (if present), environment variables, and CLI flags in that order. All non-structural state is ephemeral — scoped to function calls and returned as values. No package-level mutable state survives beyond initialization.

3. **`internal/engine.Runner.Run()`** is the pipeline orchestrator. It acquires a repository-level lock (via `security.AcquireLock`) to prevent concurrent runs, constructs an `LLMClient`, then dispatches to either the full regeneration (`RunInit`) or incremental update (`RunUpdate`). The runner's config field is read-only within this file — all mutations originate before construction.

4. **`internal/engine.Orchestrator.Pipeline()`** executes the Map-Reduce logic:
   - **Map Phase**: Discovers code files, computes SHA-256 per file, builds a directory tree via `buildTree`. Hash computation errors are silently skipped; only successfully computed hashes are stored.
   - **Change Detection** (update mode): Compares current hashes against the metadata cache to classify files as Added/Modified/Deleted. Stale modules whose directories no longer exist are pruned silently (`os.Remove` error ignored).
   - **Reduce Phase**: Calls `synthesizeNode` bottom-up on the directory tree to produce per-module summaries and a root summary string via LLM calls. Errors from individual synthesis batches are wrapped — single oversized items get truncated with `\n...[truncated]`. Infinite-loop guard falls back to per-item truncation when batch merging stalls.
   - **Standard Docs Generation**: Produces `architecture.md` and `quickstart.md` from the root summary via two additional LLM calls. Writes use `tools.WriteFileSafely`; errors are wrapped with context (e.g., `"failed to write quickstart.md: %w"`).

5. **`internal/engine.tree.go`** builds a nested directory tree from flat file paths, splitting on `/`. Affected propagation traverses the tree recursively — if any direct child is in the changeset, ancestors accumulate affected status upward. Module existence checks use `os.Stat`; only `os.IsNotExist` is acted on; all other errors are silently dropped.

6. **`internal/engine.synthesize.go`** implements context-aware chunking with three modes:
   - **Module Synthesis** (`reduceInChunks`): Processes architectural nodes in batches constrained by `NumCtx * 3`. Errors wrapped as `"LLM error during synthesis: %w"`.
   - **File Fact Consolidation** (`reduceFileFacts`): Deduplicates and merges extracted facts per step within a file. System prompt built from base + fixed role string.
   - **Recursive Convergence** (`reduceItems`): Reduces items in batches → collects intermediate results → recurses until everything converges to a single output. Termination guaranteed: short-circuits at top when `len(batches) == 1`.

7. **`internal/engine.chunking.go`** provides overlapping chunk support via `chunkTextWithOverlap`, splitting raw text into segments with overlap for context continuity between adjacent LLM calls. Silent swallow rules apply: `maxRunes <= 0` returns full text as single chunk; final partial chunk at end of runes breaks the loop without error or truncation marker.

8. **LLM Client (`internal/engine.LLMMClient`)** constructs an HTTP client with a default transport (no custom proxy/redirect/TLS config) and a 10-minute timeout. `CallLLM()` prepends the system prompt as role `"system"`, serializes to Ollama `/api/chat` JSON payload, performs non-streaming POST, reads the body fully on 200 OK or truncates at ~1 KB on non-OK status. Response parsing handles markdown code fences, balanced bracket matching with escape state tracking, and multiple JSON candidates in a single response.

9. **Response Parsing** (`internal/engine.json_parser.go`): `StripOuterMarkdownFence` strips surrounding ``` fences silently. `CleanJSONResponse` iterates stripped content calling an internal balanced-bracket extractor; unbalanced JSON is swallowed — the original trimmed input is returned unchanged. `UnmarshalJSONResponse` attempts multiple candidates, keeps the last error, and emits `"no valid JSON found in response"` only after exhausting all options.

10. **Graceful Shutdown**: SIGINT/SIGTERM handlers are installed via `signal.NotifyContext`. A deferred `stop()` cancels the context; any in-flight work respecting the cancelled context exits cleanly. No error-return path exists for signal handling within this file.

---

## Defensible Design Rules

- All failure paths return errors propagated to cobra — no panics observed.
- Stdio read failures during setup log to stderr and continue with current values; no rollback or restart occurs.
- Lock cleanup always runs via `defer` on any return path, including errors.
- Write operations use safe variants (`ReadFileSafely`, `WriteFileSafely`) where available.
- Permission normalization is implicit — existing files with different permissions will fail the write.
- No retry logic: if final persistence fails (e.g., `SaveConfig`), the flow aborts.
- Concurrency primitives are absent in the config package; it is single-threaded by design.