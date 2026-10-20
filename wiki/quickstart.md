# Quickstart Guide

## Prerequisites

- Go toolchain (required for building)
- Git repository (required for `verifyGitRepo` validation)
- Ollama runtime with model `ornith:9b` (default) or custom model via `--model-id`
- Network access to Ollama base URL (default: `http://localhost:11434`)

## Installation & Build

```bash
go build ./...
```

Binary location: `./code-reducer` (or wherever `go build` places the executable).

## CLI Commands

| Command | Purpose |
|---------|---------|
| `init` | Initialize project documentation; scans repository and builds initial wiki markdown pages |
| `read` | Read documentation pages or specific heading sections from the generated wiki repository |
| `search` | Search wiki markdown pages at section granularity using BM25 ranking |
| `setup` | Interactive CLI setup; prompts for LLM Model ID, Ollama Base URL, Context Size, Ignore patterns, Documentation directory, Output Language |
| `update` | Incremental documentation updates; scans for files modified since the last documented commit |

## Configuration

Configuration is resolved in priority order: **system defaults → YAML config file → environment variables → CLI flags**.

### Configuration File

- **Path**: `.code-reducer.yaml` (default)
- **Permissions**: `0600`
- **Atomic writes**: Uses temp-file-plus-rename pattern

### Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `CODE_REDUCER_MODEL_ID` | `ornith:9b` | Ollama model identifier |
| `CODE_REDUCER_LANGUAGE` | *(empty)* | Output language override |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama API base URL |
| `OLLAMA_NUM_CTX` | `8192` | Ollama context window size |

### CLI Flags

| Flag | Type | Description |
|------|------|-------------|
| `--model-id` | string | Override Ollama model ID |
| `--num-ctx` | int | Override context window size |
| `--language` | string | Override output language |
| `--search-path-hints` | []string | Path hints for search scoring |
| `--search-limit` | int | Max search results to return |

## Project Structure

```
.
├── cmd/                    # CLI commands (init, read, search, setup, update)
├── internal/
│   ├── config/            # Configuration loading, resolution, persistence
│   ├── engine/            # LLM-based synthesis orchestration (Map-Reduce pipeline)
│   ├── search/            # BM25 full-text search over markdown sections
│   ├── security/          # Path-traversal-safe resolution and file locking
│   └── tools/             # Safe filesystem ops, git verification, markdown parsing
└── main.go                # Entry point
```

## Quick Start Workflow

1. **Clone** the repository and ensure it's a valid Git repo
2. **Run `setup`** (or use `--model-id`, `--ollama-base-url` flags for non-interactive setup)
3. **Run `init`** to generate initial documentation pages
4. **Run `update`** for incremental updates after code changes
5. **Run `search`** to query documentation sections
6. **Run `read`** to view specific documentation pages or sections

## Error Handling

- Fail-fast pattern: functions return immediately on error without recovery or retry
- All errors wrapped with `fmt.Errorf("...: %w", err)` for context preservation
- No synchronization primitives (locks, mutexes, channels, atomics) in config module
- Path traversal protection via `security.SafeResolve` before all filesystem operations

## Task Routing

| Goal / Task | Module Doc | Key Source Files |
|---|---|---|
| Run initial synthesis | `internal/engine/README.md` | `engine.go`, `orchestrator.go` |
| Run incremental update | `internal/engine/README.md` | `orchestrator.go`, `metadata_cache.go` |
| Configure LLM backend | `internal/config/README.md` | `config.go`, `resolve.go` |
| Manage extraction steps | `internal/config/README.md` | `config.go`, `extraction_steps.go` |
| Search documentation | `internal/search/README.md` | `search.go`, `bm25.go` |
| Validate paths & locking | `internal/security/README.md` | `safe_resolve.go`, `lock.go` |
| Safe file I/O operations | `internal/tools/README.md` | `file_ops.go`, `gitignore.go` |
| Markdown parsing utilities | `internal/tools/README.md` | `markdown.go`, `heading.go` |
| Discover code files | `internal/tools/README.md` | `discover.go` |
| Verify git repository | `internal/tools/README.md` | `git.go` |
| Pipeline orchestration | `internal/engine/README.md` | `pipeline.go`, `llm_client.go` |
| Chunking & batching | `internal/engine/README.md` | `chunking.go` |
| Finalize wiki output | `internal/engine/README.md` | `finalize.go` |
| Configuration persistence | `internal/config/README.md` | `io.go` |
| Configuration resolution | `internal/config/README.md` | `resolve.go` |
| Metadata cache management | `internal/engine/README.md` | `metadata_cache.go` |