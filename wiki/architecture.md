# Architecture Overview — Code-Reducer

## System Boundaries

**Code-Reducer** is a self-contained CLI application that generates structured technical documentation from code repositories using LLM inference via Ollama-compatible endpoints. The system boundary spans:

| Boundary | Description |
|----------|-------------|
| **Repository Root** | The working directory where `.code-reducer.yaml` and `.code-reducer.lock` reside. All path operations resolve against this root. |
| **LLM Endpoint** | An external Ollama-compatible HTTP server (default `http://localhost:11434`). The app sends structured prompts and receives JSON responses. |
| **Filesystem** | Read-only access to source code; write-only access is restricted to `.code-reducer.yaml` (config, `0600`) and `.code-reducer.lock` (concurrency control). |

### Module Interaction Map

```
┌─────────────┐     ┌──────────┐     ┌──────────────┐
│   cmd       │────▶│  config  │◀────│    tools      │
│ (CLI Entry) │     │ (state)  │     │ (repo I/O)    │
└──────┬──────┘     └────┬─────┘     └──────┬────────┘
       │                 │                   │
       ▼                 ▼                   ▼
┌─────────────┐     ┌──────────┐     ┌──────────────┐
│  security   │◀────│  engine  │────▶│    LLM Client │
│ (hardening) │     │ (pipeline)│     │    (Ollama)  │
└─────────────┘     └──────────┘     └──────────────┘
```

**Data path:** `cmd` → `config` (load/apply) → `security.SafeResolve` (path hardening)  
**Discovery path:** `tools.DiscoverCodeFiles` → `tools.FilterFilesBM25` → `engine.WrapInXmlDelimiter` → `engine.AutoScaleContext` → `engine.LLMClient`  
**Response path:** LLM API → `engine.CleanJSONResponse` → `engine.UnmarshalJSONResponse`

---

## Module Overview

### `cmd` — CLI Entry Point

Implements the Cobra command hierarchy. Three subcommands register via package-level `init()` functions:

| Command | Purpose |
|---------|---------|
| **init** | Registers the root command tree on import (auto-wired) |
| **setup** | Interactive configuration flow (prompts for model ID, Ollama URL, context size, ignore patterns, docs dir). Persists to `.code-reducer.yaml`. |
| **update** | Runtime update subcommand (registered alongside init/setup) |

`NeedsCredentialSetup()` inspects `OLLAMA_HOST`, `LLM_MODEL_ID`, and related env vars; returns `true` when any are missing, gating the setup prerequisite.

### `security` — Hardening Subsystem

Three exported functions enforce filesystem integrity:

| Function | Role |
|----------|------|
| **SafeResolve** | Resolves symlinks relative to repo root; compares canonical path against parent boundary. Rejects traversal attempts. |
| **AcquireLock** | POSIX `flock`-based advisory locking on `.code-reducer.lock`. Exclusive mode writes PID; shared mode acquires without persisting state. Non-blocking (`LOCK_NB`). |
| **EnsureGitignoreHasLockfile** | Ensures `.code-reducer.lock` is in `.gitignore`. Creates file if missing, appends entry if absent. |

All path operations sanitize inputs and operate on absolute paths post-sanitization to prevent TOCTOU race conditions.

### `config` — Configuration Management

Centralized persistence with precedence chain: **CLI flags → env vars → host-derived defaults**. Schema is flattened YAML (`Config` struct) persisted as `.code-reducer.yaml` (permissions `0600`).

**Precedence for Ollama context size:**
1. CLI flag `-num-ctx` / `--num-ctx`
2. Env var `OLLAMA_NUM_CTX`
3. Host total memory → proportional scaling fallback (default 8192)

**Env vars honored via `LoadAndApplyConfig`:**
| Key | Source |
|-----|--------|
| `CodeReducerModelIdEnvKey` | Model identifier |
| `OllamaBaseUrlEnvKey` | Ollama HTTP base URL |
| `OllamaNumCtxEnvKey` | Context size (token count) |
| `LangsmithApiKeyEnvKey` | LangSmith auth token |
| `LangchainProjectEnvKey` | LangChain project name |
| `LangchainTracingEnvKey` | Trace enable/disable flag |

### `tools` — Repository Introspection

Low-level primitives for reading, writing, discovering, and hashing files within a Git repository. All paths are canonicalized (trailing slashes removed, symlinks followed).

**Core operations:**
- **ReadFileSafely** — Resolves virtual path → root-boundary check → `os.ReadFile`
- **WriteFileSafely** — Creates parents (`MkdirAll`) → opens with `O_CREATE|O_WRONLY` → post-write symlink verification
- **DiscoverCodeFiles** — Recursive walk skipping build artifacts, dep trees, caches; returns sorted absolute paths
- **IsBinaryFile** — Scans first 1024 bytes for `\x00`; binary detection heuristic
- **RunGit / GetGitHead / CreateGitSummary** — Git command wrappers capturing stdout+stderr merged; HEAD hash and status summaries formatted as LLM prompt context
- **HashRepoRoot** — SHA-256 of canonicalized path (hex-encoded, 64-char output)

### `engine` — Code Retrieval Pipeline

Orchestrates the retrieval-to-inference flow: file ingestion → BM25 ranking → XML-wrapping → context scaling → LLM serialization → response deserialization.

**Key structures:**
| Structure | Role |
|-----------|------|
| **Message** (`Role`, `Content`) | Single LLM conversation turn; JSON-serializable |
| **LLMClient** (`ModelID`, `BaseURL`, `NumCtx`) | Ollama-compatible API credentials; constructed via `NewLLMClient` |
| **Event** (`Type`, `Message`) | Internal routing for completion/error signals |
| **DirNode** (`Path`, `Files`, `Children`) | Recursive directory tree representation consumed by BM25 |

**Context pipeline:**
1. **FilterFilesBM25** — Ranks files against query string using BM25 formula: `Score = (tf(t,d) * IDF(t)) / (dl + b * avgdl)`; returns top-K file paths
2. **WrapInXmlDelimiter** — Wraps content in `<file path="...">...</file>` tags for injection safety and provenance metadata
3. **AutoScaleContext** — Caps context at 8192 tokens when non-positive input; scales proportionally otherwise

**Response handling:**
- **CleanJSONResponse** — Strips markdown code fences (`` ``` ``) and trims delimiters from raw LLM output
- **UnmarshalJSONResponse** — Type-asserts cleaned JSON into caller-supplied interface pointer; panics on assertion failure if types mismatch

---

## Data Flow Summary

```
User Input ──→ RootCmd ──→ Config Load/Apply ──→ Security.SafeResolve
Repository Files ──→ DiscoverCodeFiles ──→ FilterFilesBM25 (BM25 Ranking)
Selected Content ──→ WrapInXmlDelimiter ──→ AutoScaleContext ──→ LLMClient
API Request ──→ Message Serialization ──→ Ollama Endpoint ──→ JSON Response
Response Handling ──→ CleanJSONResponse ──→ UnmarshalJSONResponse ──→ Output
```

All exported functions operate on absolute paths after sanitization to prevent directory-traversal attacks and TOCTOU race conditions. Configuration is persisted to `.code-reducer.yaml` with `0600` permissions; concurrent access is serialized via `flock`-based locking in `.code-reducer.lock`.