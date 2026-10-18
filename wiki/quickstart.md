# Quickstart

## Prerequisites

- **Ollama** — default LLM backend. Default base URL is `http://localhost:11434` and default model is `ornith:9b`. Override via `OLLAMA_BASE_URL` or `CODE_REDUCER_MODEL_ID` environment variables.
- **Git** — the tool verifies the working directory is inside a git repo on first run.
- **Go** toolchain — the project is a Go module.

## Installation

```bash
git clone <repo-url>
cd <repo>
go build -o code-reducer ./cmd
```

Place the binary in your project root or a PATH directory.

## CLI Commands

| Command | Purpose |
|---------|---------|
| `setup` | Interactive wizard that writes `.code-reducer.yaml` (model ID, base URL, context size, ignore patterns, docs directory). |
| `init` | First-time documentation build — scans the repo and generates the initial wiki pages. |
| `update` | Incremental regeneration — detects files changed since the last documented commit and rewrites affected wiki pages. |

Persistent flags (`--model-id`, `--num-ctx`) are available on the root command and override config-file values.

## Configuration

Configuration is resolved in priority order: **CLI flag → environment variable → `.code-reducer.yaml` file**.

### Environment Variables

| Variable | Default | Purpose |
|---|---|---|
| `CODE_REDUCER_MODEL_ID` | `ornith:9b` | LLM model identifier |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama API endpoint |
| `OLLAMA_NUM_CTX` | `8192` | Context window size |

### CLI Flags

| Flag | Purpose |
|---|---|
| `--model-id` | Override LLM model ID |
| `--num-ctx` | Override context window size |

### Config File

`.code-reducer.yaml` (mode `0600`) is created on first run. It stores resolved configuration including model ID, base URL, context size, and custom prompts.

## Package Structure

```
.
├── cmd/              # CLI entry point (init, setup, update)
├── internal/
│   ├── config/       # Configuration loading, resolution, persistence
│   ├── engine/       # Documentation synthesis pipeline
│   ├── security/     # Path traversal prevention, process locking
│   └── tools/        # Safe filesystem and git utilities
└── wiki/             # Generated documentation output (default)
```

## Key Files

| File | Purpose |
|---|---|
| `.code-reducer.yaml` | Persisted configuration |
| `.metadata.json` | Engine cache (file hashes, module mappings) |
| `.code-reducer.lock` | Process lock file (auto-added to `.gitignore`) |
| `AGENTS.md` | Agent guidelines (updated on init) |
| `architecture.md` | Global architecture document |
| `quickstart.md` | Developer quickstart guide |
| `modules/<path>.md` | Per-module summaries |

## Error Handling

- **Lock contention**: `ErrLockHeld` returned if another process holds the lock.
- **Path traversal**: `ErrPathTraversal` returned if input path escapes repo root.
- **LLM failures**: Propagated as wrapped errors.
- **File read failures**: Swallowed and logged via status callback.
- **Git operations**: Errors wrapped and returned (no panics).