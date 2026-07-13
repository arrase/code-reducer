# quickstart.md

## Quick Start Guide

Code-Reducer generates and maintains project wiki documentation (`wiki/architecture.md`, `wiki/quickstart.md`, per-module API summaries) via an LLM-driven synthesis pipeline operating on source code repositories.

### Prerequisites

- A Git repository (`.git` directory present at repo root)
- Ollama running locally with a compatible model available
- Optional: existing `.code-reducer.yaml` configuration file; if missing, an interactive setup wizard runs automatically when stdin is not a terminal

### Running the Tool

```bash
# Full generation from scratch
code-reducer init

# Incremental regeneration after code changes
code-reducer update
```

**Mode Guard:** The tool validates that `init` and `update` are used in correct states. Running `init` on an already-initialized repository requires switching to `update`. Running `update` before initialization fails with a suggestion to run `init` first.

### Configuration Priority

Each setting follows its own resolution chain, independent of others:

| Setting | Resolution Order |
|---|---|
| Model ID | YAML → env (`CODE_REDUCER_MODEL_ID`) → CLI flag |
| Ollama Base URL | Hard-coded default (`http://localhost:11434`) → YAML → env (`OLLAMA_BASE_URL`) |
| Context Size | Hard-coded default (8192) → YAML → env (`OLLAMA_NUM_CTX`) → CLI flag |
| Docs Directory | Hard-coded default (`wiki`) → YAML only |

Prompts (system, synthesis, architecture, file fact consolidation) follow: hard-coded default → YAML override. No CLI or env overrides exist for prompt fields.

### Pipeline Behavior

**Init mode:** Full generation from scratch. Discovers all code files respecting `.gitignore` and configured ignores, computes SHA256 hashes per file, builds a hierarchical directory tree, then synthesizes bottom-up: leaf file summaries flow up through child directories to module-level aggregates, converging at the repository root where `architecture.md` and `quickstart.md` are produced.

**Update mode:** Incremental regeneration triggered by detected file changes. Compares current hashes against cached ones (Added/Modified/Deleted), builds tree only from currently existing files, determines affected directories based on change set, and re-runs synthesis within affected modules only. If nothing changed, returns immediately with no regeneration.

### Error Handling at a Glance

- Event-emitted errors are printed to stderr and swallowed
- Only the runner's return error is wrapped with `"documentation run failed: %w"` before propagation
- File read failures during synthesis (cache miss for individual files) log as warnings but do not halt processing; facts remain empty strings for those files
- Lock acquisition failure propagates with wrapping; lock release on failure is deferred and non-fatal
- Configuration file does-not-exist errors are swallowed, replaced with an empty config

### Concurrency Note

The engine uses a lockfile to serialize concurrent invocations. No explicit goroutines or mutexes exist within the synthesis pipeline itself beyond the embedded `sync.Mutex` in the security subsystem's `SimpleLock`. Map writes to cached state lack locking and constitute a potential data race if multiple goroutines invoke with shared cache instances; external synchronization is not documented within this module boundary.