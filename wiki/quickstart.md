# Code Reducer — Quickstart & Architecture Overview

## What It Does

`code-reducer` is a Go application that generates automated documentation (wiki pages) from repository state through LLM inference. On each run it scans changed files since the last documented commit, ranks them by relevance via BM25, and feeds the results to an Ollama-backed LLM to produce updated wiki entries.

---

## System Boundaries & Module Interaction

```
┌─────────────┐     ┌──────────┐     ┌──────────────┐     ┌──────────┐
│  cmd         │────▶│  engine   │────▶│    LLM       │────▶│ Response │
│  (Cobra CLI) │◀────│orchestrator│◀────│ client + retry│◀───│ parser   │
└─────────────┘     └─────┬─────┘     └──────────────┘     └──────────┘
                          │
              ┌───────────┼───────────┐
              ▼           ▼           ▼
       ┌──────────┐  ┌──────────┐  ┌──────────┐
       │ security  │  │   tools  │  │  config   │
       │(path lock │  │fs/git/  │  │4-tier     │
       │ isolation)│  │hashing)  │  │ resolution│
       └──────────┘  └──────────┘  └──────────┘
```

| Layer | Responsibility | Key Interaction |
|---|---|---|
| **cmd** | CLI entry point, signal handling, mode dispatch | Calls `engine.Run()` after resolving config and acquiring lock |
| **security** | Path confinement + flock-based locking + gitignore hygiene | Gated before any filesystem/Git operation in engine/tools |
| **tools** | Safe read/write, file discovery, Git queries, SHA-256 hashing | Consumed by engine for diff parsing, cache I/O, and content integrity |
| **config** | 4-tier resolution (defaults → YAML → env → CLI flags), process env propagation | Provides `Config` to engine; persisted in `.code-reducer.yaml` |
| **engine** | BM25 ranking, state tracking, LLM invocation, JSON extraction, pipeline orchestration | Top-level orchestrator; bridges raw repo state with LLM inference |

---

## Initialization Order (Execution Model)

1. `security` primitives gate all repository-accessing operations
2. `tools` provide low-level filesystem/Git/hash primitives consumed by other subsystems
3. `config` resolves runtime parameters via four-tier precedence chain, then propagates values into the OS process env
4. `engine` orchestrates the full documentation-generation pipeline

---

## Quickstart: First Run

```bash
# 1. Ensure Ollama is running with your model available
ollama serve

# 2. Point Code Reducer at your repository root (or run from repo root)
code-reducer --model-id <your-model> --num-ctx 8192 init
```

If no `.code-reducer.yaml` exists, an interactive setup flow prompts you for configuration and persists it to the repo root. On subsequent runs:

```bash
code-reducer update
```

`update` scans changed files since the last documented commit (HEAD), ranks them via BM25, invokes the LLM, and writes updated wiki pages back into the repository.

---

## Configuration Precedence

| Tier | Source | Example |
|---|---|---|
| 1 — Defaults | Hard-coded constants (`localhost:11434`, `8192` ctx) | Baseline when nothing else is set |
| 2 — YAML file | `.code-reducer.yaml` in repo root | Persistent user preferences |
| 3 — Environment variables | `CodeReducerModelIdEnvKey`, `OllamaBaseUrlEnvKey`, etc. | CI/CD overrides |
| 4 — CLI flags | `--model-id`, `--num-ctx` | Highest priority; per-invocation |

Resolved config is also injected into the process environment via `os.Setenv`, so downstream packages (LangSmith tracing, LangChain) observe consistent values without explicit dependency on this module.

---

## Key Data Structures

| Type | Package | Purpose |
|---|---|---|
| `Config` | `internal/config` | Runtime config; all resolved values |
| `MetadataCache` | `internal/engine` | Persisted state across runs (last commit SHA, processed files/modules) |
| `FileChange` | `internal/engine` | Parsed git diff entries (Added/Modified/Deleted/Renamed/Copied) |
| `Document` | `internal/engine/context` | File content + TF statistics for BM25 scoring |
| `LLMClient` | `internal/engine` | Abstraction over Ollama HTTP chat API with retry logic |
| `Runner` | `internal/engine/runner` | Pipeline entry point; acquires lock, dispatches init/update modes |

---

## Security Guarantees

- **Path confinement** — `SafeResolve()` rejects path traversal (`..`) and external symlinks before any filesystem call.
- **Concurrent access control** — `AcquireLock()` uses `flock(2)` semantics; TOCTOU-safe checks prevent symlink hijacking of lock targets.
- **State isolation** — Lock file is added to `.gitignore` so transient state isn't tracked by Git.

---

## Engine Pipeline Stages

1. **Indexing** (`context.go`) — Tokenize, estimate context budget, wrap content in XML delimiters for prompt-injection safety
2. **State Tracking** (`engine.go`) — Parse git diff → `[]FileChange`; detect affected nodes; update `MetadataCache`
3. **Inference** (`engine.go`) — `LLMClient.CallLLM()` with retry logic on transient errors (connection resets, rate limits)
4. **Response Handling** (`json_parser.go`) — Strip markdown code fences or locate JSON braces → unmarshal into target struct