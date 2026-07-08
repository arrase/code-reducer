# code-reducer — Quick Start & Wiki

## Overview

`code-reducer` is a CLI-driven repository analysis tool written in Go 1.26. It ingests source repositories, ranks files via BM25 term-frequency scoring, constructs hierarchical directory trees, and synthesizes markdown documentation through recursive chunk batching against an Ollama LLM backend. All results persist to disk.

**System boundary:** Single binary. Configuration lives in `.code-reducer.yaml` at the repository root. Runtime concurrency is serialized via `flock`-based file locking; all filesystem operations are hardened against path traversal and symlink injection.

---

## Prerequisites

- Go 1.26 (for building)
- Ollama running locally (`http://localhost:11434`) with a model loaded
- A git repository where you want to analyze

---

## First Run — Interactive Setup

```bash
code-reducer init
```

This prompts for the required environment variables and writes `.code-reducer.yaml` (mode 0600). The lockfile `.code-reducer.lock` is added to `.gitignore` automatically.

**Manual equivalent:** Set these env vars before invoking the root command:

| Variable | Default | Purpose |
|---|---|---|
| `MODEL_ID` | — (required) | Ollama model identifier |
| `OLLAMA_BASE_URL` | `http://localhost:11434` | Ollama API endpoint |
| `OLLAMA_NUM_CTX` | 8192 | Context size limit |

---

## Quick Start — Analyze a Repository

```bash
cd /path/to/repo
code-reducer
```

The root command performs:

1. **Credential gate** — checks if `MODEL_ID` is set; redirects to setup flow if missing
2. **Lock acquisition** — acquires `.code-reducer.lock` for process serialization (shared or exclusive depending on context)
3. **Config load** — reads `.code-reducer.yaml`; triggers interactive setup if absent
4. **LLM engine execution** — runs the model inference pipeline with live event streaming

---

## Architecture at a Glance

```
Repository Root
    │
    ▼  [security.SafeResolve] — canonical absolute path resolution
    ├── internal/tools/               Low-level repo ops (file I/O, git cmds, binary detection)
    │   ├── DiscoverCodeFiles(root) → []string
    │   ├── ReadFileSafely(path) → []byte
    │   ├── WriteFileSafely(relPath, content) error
    │   └── IsBinaryFile(path) bool
    │
    ├── internal/engine/              Orchestration layer (ranking, tree building, LLM client)
    │   ├── Tokenize(input string) []string
    │   ├── EstimateTokens(text string) int
    │   ├── FilterFilesBM25(query, files []Document) []Document
    │   ├── buildTree(filePaths []string) *DirNode
    │   ├── LLMClient (modelID, baseURL, numCtx)
    │   └── reduceInChunks(dirTree *DirNode, accumulated []byte) string
    │
    ├── internal/config/              Configuration management (YAML load/save/env resolution)
    │   ├── LoadConfig(cwd string) (*Config, error)
    │   ├── SaveConfig(cwd string, cfg *Config) error
    │   └── GetOllamaContextSize(flagVal string) int
    │
    └── internal/engine/context.go    Document struct assembly → XML-wrapped context
        Raw file content + TermFrequency map[string]int
        ▼  <file>path</file><content>...</content>
        ▼  [internal/engine/reduceInChunks] — recursive chunk batching
            DirNode tree → LLMClient.Chats() → markdown fence stripping
            ▼  Parsed JSON responses via json_parser.go utilities
```

---

## Module Interaction Flow

**Phase 1: Discovery & Ranking**

`DiscoverCodeFiles` recursively walks the repo root, excluding build artifacts and binary files. Each candidate passes `IsBinaryFile`. Paths are resolved through `SafeResolve` to enforce containment within the repository boundary. Collected paths feed into `Tokenize`, which produces a lowercase alphanumeric token stream per file. Term frequencies are computed per-file, then aggregated across the corpus for BM25 scoring via `FilterFilesBM25`.

**Phase 2: Context Assembly**

Ranked documents are assembled into an XML-wrapped context structure (`<file>path</file><content>...</content>`). Each entry is prefixed/suffixed with `<file>` markers to prevent prompt injection. The accumulated content is capped at `AutoScaleContext` (default 8192 tokens) before submission.

**Phase 3: LLM Synthesis**

The directory tree (`DirNode`) and XML context are fed into the Ollama API via `LLMClient.Chats()`. Responses are post-processed by `stripOuterMarkdownFence`, which extracts content between triple-backtick or JSON fences using regex. Results accumulate across recursive chunk batches in `reduceInChunks`.

**Phase 4: Persistence & Concurrency**

All filesystem writes go through `WriteFileSafely`, which verifies the target is not a symlink before persisting data. Process-level concurrency is managed by `AcquireLock` on `.code-reducer.lock` using `flock.Flock`. The lockfile path is excluded from Git tracking via `EnsureGitignoreHasLockfile`.

---

## Configuration Schema (`.code-reducer.yaml`)

```yaml
model:
  id: "your-model"
ollama:
  base_url: "http://localhost:11434"
  num_ctx: 8192
langsmith:
  api_key: ""
langchain:
  project_name: ""
  tracing_v2_enabled: false
ignore_patterns: []
docs_dir: "./output"
```

Config is loaded from file, then environment variables override. CLI flags take highest precedence. `LoadAndApplyConfig` silently skips missing files and only sets env vars that are not already present in the parent process.

---

## Database Path

Persistent state uses a fixed path: `code-reducer.sqlite`. No configuration required; always resolved relative to the working directory.

---

## Security Model

- **Path containment:** Every user-supplied path is validated against `repoRoot` via `SafeResolve` before any filesystem operation
- **Symlink injection prevention:** `WriteFileSafely` checks target paths for symlinks before writing
- **Binary file detection:** First 1024 bytes scanned for null bytes; binary files excluded from analysis
- **Git hygiene:** Lockfile is auto-added to `.gitignore`; git operations delegate to `RunGit` with combined stdout/stderr capture

---

## Subcommands

| Command | Purpose |
|---|---|
| `code-reducer init` | Interactive setup when credentials are missing |
| `code-reducer` (root) | Full analysis pipeline with live event streaming |