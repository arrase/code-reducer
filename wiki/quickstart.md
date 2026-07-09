# Code-Reducer — Quick Start

## What It Does

Code-Reducer automatically generates and maintains documentation for Go repositories. Given a codebase, it produces:

- **`architecture.md`** — A high-level system overview
- **`quickstart.md`** — Getting-started instructions
- **Per-module docs** — One document per module (stored in `wiki/modules/`)

All output is synthesized by an LLM. The tool runs as a synchronous, file-system-bound process with no shared mutable state across goroutines.

## Prerequisites

| Dependency | Requirement |
|---|---|
| Go repository | A git-initialized repo at the working directory |
| LLM backend | An Ollama-compatible endpoint (default: local Ollama) |
| Git executable | Required for commit-based change detection |

If you do not have a `.code-reducer.yaml` config file yet, run `code-reducer setup` to create one interactively.

## Installation & Configuration

### Run the Setup Wizard (Recommended)

```bash
code-reducer setup
```

This produces `.code-reducer.yaml` in the repo root with sensible defaults and asks you for the LLM model ID and base URL.

### Manual Configuration

Configure via CLI flags, environment variables, or YAML config file. Precedence is: **CLI flag > env var > YAML file > system default**.

| Parameter | Flag | Env Var | Default |
|---|---|---|---|
| LLM model ID | `--model` | `CODE_REDUER_MODEL` | (must be set) |
| Ollama base URL | `--base-url` | `CODE_REDUER_BASE_URL` | (must be set) |
| Context window size | `--ctx-size` | `CODE_REDUER_NUM_CTX` | system default |
| Docs directory | — | — | `wiki/` |

The config file is `.code-reducer.yaml`, written with mode 0600. All filesystem writes go through path-sandboxed, atomic primitives that prevent TOCTOU races and symlink attacks.

## Running Code-Reducer

### Full Regeneration (Init Mode)

Regenerates all documentation from scratch:

```bash
code-reducer init
```

Steps performed by default:

1. Acquires a process-level lock to prevent concurrent runs.
2. Discovers code files, skipping git directories and ignored paths/extensions.
3. Builds an in-memory directory tree.
4. Determines which modules are affected (changed files, missing docs).
5. Synthesizes root documents (`architecture.md`, `quickstart.md`) via two LLM calls.
6. Synthesizes per-module documentation for each affected module.
7. Persists a metadata cache to disk so future updates can skip unchanged work.

### Incremental Update (Update Mode)

Re-document only modules that have changed since the last run:

```bash
code-reducer update
```

Behavior:

- If no files have changed, exits with a success message and does not regenerate anything.
- If global docs (`architecture.md`, `quickstart.md`) exist on disk but no affected directories exist, skips regeneration entirely.
- Otherwise regenerates global docs first, then processes only the affected modules.

### Output Location

By default, generated documentation lands in the `wiki/` directory at the repository root:

```
wiki/
├── architecture.md
├── quickstart.md
└── modules/
    ├── <module-name>.md
    └── ...
```

You can override this with the `--docs-dir` flag or by setting it in the YAML config.

## Error Handling & Exit Codes

| Condition | Behavior |
|---|---|
| Successful run | Exits 0, status messages to stdout |
| LLM error | Wrapped error printed to stderr, exit 1 |
| Filesystem error (read/write) | Wrapped with context, propagated up; AGENTS.md writes are silently discarded |
| Path sandbox violation | Hard failure, no write occurs |
| Lock contention | Acquisition fails atomically; process exits immediately |

Status messages flow through an event callback to stdout. Errors specific to the `error` event type go to stderr. OS-level failures exit with code 1 and print to stderr.

## Concurrency Model

Code-Reducer runs as a single-process, file-system-bound tool:

- No shared mutable state across goroutines within the engine package.
- Process-level mutual exclusion is achieved via an atomic lockfile (`.code-reducer.lock`) opened with `O_EXCL` — no check-and-act race window exists between acquisition and use.
- The lock sentinel is automatically added to `.gitignore` so it does not enter version control.

## Architecture at a Glance

```
CLI invocation → flag parsing & mode selection → env validation (git repo root, config existence)
    ↓
Merged configuration resolution (flags → env vars → YAML → defaults)
    ↓
Signal handler installation for graceful shutdown on SIGINT/SIGTERM
    ↓
Pipeline execution in event-loop mode
    ↓
Completion or failure exit with appropriate status code
```

The pipeline is synchronous. All external communication goes through a custom Ollama-compatible LLM client constructed in `internal/engine`. Configuration is resolved entirely before the engine runs; no re-resolution occurs mid-pipeline.