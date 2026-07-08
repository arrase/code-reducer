# Architecture Overview

## System Boundaries

Code-Reducer is a single-process CLI application that ingests source code from a repository root, ranks candidate files via BM25 lexical scoring against user queries, synthesizes documentation through an LLM pipeline, and persists configuration with restricted permissions. The system operates as a lock-serialized engine: concurrent invocations are prevented via POSIX file locking (`AcquireLock`), the analysis boundary is enforced through path-traversal protection (`SafeResolve`), and all I/O passes through TOCTOU-safe wrappers to reject symlink races.

**External boundaries:**
- CLI invocation → `RootCmd` dispatches subcommand
- Configuration resolved via layered precedence (system defaults → YAML file → env vars → CLI flags)
- Security perimeter established (`AcquireLock`, `SafeResolve`)
- Repository traversal discovers source files (`DiscoverCodeFiles`)
- BM25 ranking scores candidate documents against query terms
- LLM synthesis consumes ranked content within bounded context window
- Documentation persisted to `DocsDir`

## Module Interaction Map

```
┌─────────────┐     ┌──────────────┐     ┌────────────┐
│  CLI Layer   │     │ Config       │     │ Engine      │
│              │     │ Subsystem    │     │             │
│ RootCmd      │────▶│ ResolveConfig│────▶│ Runner      │
│ NeedsCred    │     │ Load/Save    │     │ BM25+LLM   │
│ RunSetupFlow │     │ ConfigSchema │     │ LLMClient  │
└─────────────┘     └──────────────┘     └────────────┘
       │                    │                   │
       ▼                    ▼                   ▼
┌─────────────┐     ┌──────────────┐     ┌────────────┐
│ Security     │     │ Tools        │     │ DocsOut    │
│ Subsystem    │     │ Subsystem    │     │            │
│ SafeResolve  │     │ ReadFileSafely│   │ Persist    │
│ AcquireLock  │     │ WriteFileSafely│  │ to DocsDir │
│ EnsureGitignore│   │ DiscoverCodeFiles│
└─────────────┘     └──────────────┘     └────────────┘
```

## Data Flow Summary

**Initialization path:** `RootCmd` → `NeedsCredentialSetup()` → (if true) `RunSetupFlow()` → persist config atomically.

**Analysis path:** `RootCmd` → `ResolveConfig()` → `AcquireLock()` → `DiscoverCodeFiles()` → BM25 scoring (`FilterFilesBM25`) → LLM synthesis (`CallLLM`/`StreamLLM`) → documentation persisted to `DocsDir`.

## Key Architectural Decisions

### Lock Serialization
All operations are serialized through POSIX advisory file locking. `AcquireLock(repoRoot, exclusive)` returns a `*flock.Flock` handle that must be released by the caller. Concurrent invocations are prevented; no parallel execution within a single process lifetime.

### Path Traversal Protection
`SafeResolve(repoRoot, inputPath)` is the mandatory entry point for all filesystem I/O. It validates external paths resolve strictly within `repoRoot`, rejects absolute paths and `..` traversals, and detects symlink targets pointing outside the repo boundary. Returns normalized absolute path on success or sentinel error on failure.

### Configuration Precedence
Four-layer resolution with strict override scope: system defaults → YAML file → environment variables (`CODE_REDUCER_MODEL_ID`, etc.) → CLI flags. Each layer is a pure function returning `(partial *Config, error)`. The resolver composes left-to-right; partial configs merge field-by-field with higher-precedence winning on any non-zero value. Returns a copy to prevent accidental mutation. If YAML parse fails, resolver treats it as "no config file" and falls through to env/CLI layers only.

### TOCTOU-Safe I/O
`WriteFileSafely` performs a stat equivalent immediately prior to truncation; if target is not a regular file (symlink or directory), operation aborts with error, preventing `os.SYMLINK` race conditions where dangling symlinks could cause data loss.

### LLM Pipeline with Retry Logic
`CallLLM` wraps synchronous invocation with retry logic for transient HTTP errors: status 429 (Too Many Requests) and 500–504 (server/transport failures). `StreamLLM` reads line-by-line from HTTP stream, invoking a callback per chunk.

### BM25 Ranking
Two-stage retrieval-generation pipeline: candidate files are loaded, tokenized via regex (`[a-z0-9]+`), scored with smoothed IDF against query terms using TF·IDF formula, sorted by descending relevance, and top K paths returned. Constants `k1=1.5` (TF saturation) and `b=0.75` (doc-length normalization) are configurable.

### Prompt Injection Mitigation
File path and content are encapsulated inside `<file_content>` XML-like tags before LLM consumption to mitigate prompt injection risks during synthesis.

## Module Interaction Details

**CLI Bootstrap ↔ Config:** `RootCmd` initializes the command tree; configuration resolution flows through `ResolveConfig`. If credentials missing, `NeedsCredentialSetup()` returns true and triggers `RunSetupFlow()`, which persists parameters atomically via `SaveConfig(cfg, path)` with mode 0600.

**Security ↔ Tools:** Security functions operate relative to canonical `repoRoot`. `SafeResolve` validates all paths before any tool function touches them. `AcquireLock` establishes the concurrency boundary that protects all operations in the pipeline.

**Engine ↔ Config:** `Runner` receives resolved config, instantiates `LLMClient`, and drives documentation generation within bounded context window (8192 default). BM25 scoring feeds ranked file paths into LLM prompt via `WrapInXmlDelimiter`.

**Tools ↔ Security:** Low-level primitives (`ReadFileSafely`, `WriteFileSafely`) shield consumers from path resolution failures, symlink race conditions, and non-deterministic subprocess behavior. Data flows converge on byte-level operations before being consumed by higher-order tooling modules.