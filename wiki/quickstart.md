# Quick Start — Code-Reducer

## What It Does

Code-Reducer is a CLI tool that turns code repositories into structured documentation by feeding source files through an LLM (via Ollama). The pipeline: discover → rank with BM25 → wrap in XML delimiters → call the model → parse JSON. Everything runs end-to-end from one command line invocation.

## System Boundaries & Module Interaction

| Module | Responsibility | Interacts With |
|---|---|---|
| **`cmd`** | Cobra CLI hierarchy (`init`, `setup`, `update`). Registers subcommands at import time; `RunSetupFlow` orchestrates interactive config collection. | `config`, `security` |
| **`config`** | YAML-backed schema (`.code-reducer.yaml`) with env-var precedence and runtime flag overrides. Persists to disk at `0600`; mirrors selected fields into `os.Getenv` only when absent. | `cmd`, `tools`, `security` |
| **`security`** | Path-traversal mitigation (`SafeResolve`), POSIX advisory locking (`.code-reducer.lock` via `flock`), `.gitignore` integration for lockfile exclusion. Serializes concurrent access to shared resources. | `cmd`, `tools`, all I/O paths |
| **`tools`** | Hardened file ops: read/write with TOCTOU checks, recursive discovery (skipping build artifacts/`.git/`/vendor/), binary detection, git commands (`RunGit`, `GetGitHead`, `CreateGitSummary`), repo-root hashing. All exported functions resolve virtual paths through the filesystem layer before any I/O. | `engine`, `config`, `security` |
| **`engine`** | Orchestration of the full retrieval pipeline: BM25 ranking → XML wrapping → context scaling → LLM request serialization → JSON response deserialization. Owns data structures (`Message`, `LLMClient`, `DirNode`, `Document`). | All modules (consumes output from others) |

### Data Flow Summary

```
User CLI Input ──→ cmd.RootCmd ──→ config.Load/Apply ──→ security.SafeResolve
Repository Files ──→ tools.DiscoverCodeFiles ──→ engine.FilterFilesBM25 (BM25 rank)
Selected Content ──→ engine.WrapInXmlDelimiter ──→ engine.AutoScaleContext ──→ engine.LLMClient
LLM Request ──→ Message Serialization ──→ Ollama Endpoint ──→ JSON Response
Response Handling ──→ CleanJSONResponse ──→ UnmarshalJSONResponse ──→ Output
```

## First Run — Setup

1. **Check credentials.** `NeedsCredentialSetup()` inspects env for `OLLAMA_HOST`, `LLM_MODEL_ID`, etc. If any are missing, run:
   ```bash
   code-reducer setup --repo-root <path>
   ```
2. **Persist config.** The interactive flow writes `.code-reducer.yaml` at the repo root with `0600` permissions. Lockfile management is automatic via `.code-reducer.lock`.
3. **Run a doc generation pass:**
   ```bash
   code-reducer run --repo-root <path> [--docs-dir <out>]
   ```

## Key Patterns Worth Knowing

- **Config precedence:** CLI flag → env var → host-derived default (e.g., total memory scales `OllamaNumCtx`). `LoadAndApplyConfig` loads from disk and mirrors to process env only when the env slot is empty.
- **Path safety everywhere.** All exported functions in `tools` and `security` resolve virtual paths through `SafeResolve` before I/O, preventing symlink traversal and TOCTOU race conditions.
- **BM25 retrieval.** `FilterFilesBM25` ranks files by BM25 score against the user query; top-K results are wrapped in `<file path="...">...</file>` XML delimiters for injection safety.
- **Context scaling.** `AutoScaleContext` caps total prompt length at 8192 tokens when input is non-positive, otherwise passes through.
- **JSON response handling.** `CleanJSONResponse` strips markdown code fences (` ``` `) before deserialization via `UnmarshalJSONResponse`.