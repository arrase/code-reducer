# Architecture Overview

## System Boundaries

Code-Reducer is a CLI-driven documentation agent that automatically generates and maintains project wikis. It operates on a git repository root and produces markdown documentation in a configurable output directory (default: `wiki/`). The system has no network, database, or external API dependencies beyond an LLM backend (Ollama by default).

The system boundary is the repository root. All filesystem operations resolve virtual paths through a security layer that prevents path traversal, even when symlinks are involved. The tool runs as a single process with no async patterns, no global mutable state, and no synchronization mechanisms.

## Module Interaction Map

```
cmd/          ──► orchestrates CLI entry point, dispatches to engine
internal/config/ ──► configuration persistence and resolution
internal/engine/ ──► synthesis pipeline orchestration
internal/security/ ──► path traversal prevention, process locking
internal/tools/  ──► safe filesystem operations, git verification
```

All modules communicate via function parameters and return values. No shared mutable state exists between packages. The `cmd` package is the only entry point; all other packages are internal.

## Operational Modes

Three modes are exposed via the CLI:

- **init** — first-time documentation build. Scans the repository, hashes all code files, synthesizes summaries hierarchically, generates `architecture.md` and `quickstart.md`, updates `AGENTS.md`, and persists the metadata cache.
- **setup** — interactive configuration wizard. Collects LLM model ID, Ollama base URL, context size, ignore patterns, and docs directory from the user. Persists via `config.SaveConfig`.
- **update** — incremental regeneration. Identifies files modified since the last documented commit using cached SHA-256 hashes and regenerates only affected modules.

## Configuration Resolution

Configuration is resolved in priority order: CLI flag → environment variable → config file → system default. The config file is `.code-reducer.yaml` with `0600` permissions. Three environment variables are supported: `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX`.

The config schema includes LLM settings, extraction step definitions, prompt templates, ignore patterns, and the docs directory. Extraction steps are a fixed list: `API_SIGNATURES`, `BUSINESS_LOGIC`, `STATE_AND_CONCURRENCY`, `ERRORS_AND_SIDE_EFFECTS`.

## Security Primitives

Two non-negotiable security primitives protect the system:

1. **Path traversal prevention** — `SafeResolve(repoRoot, inputPath)` canonicalizes the repository root, resolves symlinks on the root and intermediate ancestors, walks up the input path to the first physically existing ancestor, rebuilds the full path, and verifies the result lies strictly inside the repository root via `filepath.Rel`. Returns `ErrPathTraversal` if containment fails.

2. **Process-level locking** — `AcquireLock(repoRoot)` opens a lock file with `O_WRONLY|O_CREATE|O_EXCL` for atomicity, writes the process PID, and returns a `*SimpleLock` handle. `Unlock()` closes the file descriptor and removes the lock file. Uses `sync.Mutex` to protect the closed state.

## Synthesis Pipeline

The engine orchestrates a hierarchical synthesis pipeline:

1. **Tree construction** — `buildTree` parses a file slice into a hierarchical `*DirNode` structure.
2. **Change detection** — SHA-256 hashes are computed for all code files. The metadata cache stores per-file hashes with associated facts. On update, files modified since the last documented commit are identified. Ancestors are marked affected when a descendant is affected.
3. **Hierarchical synthesis** — `synthesizeChildren` processes child directories recursively, synthesizing subsystem summaries before individual files. `synthesizeNode` reduces all components for a directory into a final summary, writes it to `modules/<path>.md`, and persists the cache.
4. **Text reduction** — `reduceInChunks` splits oversized items with an 800-character overlap, batches remaining items, and reduces each batch. If total output reaches 95% of input, reduction stops and results are concatenated.
5. **Fact extraction** — `extractFileFacts` invokes the LLM for each extraction step per chunk of a file. Strips outer markdown fences, filters "none" facts, and caches results keyed by file SHA-256.

## LLM Abstraction

The `llmCaller` interface abstracts LLM invocation with `CallLLM(ctx, systemPrompt, userPrompt) (string, error)` and `NumCtx() int`. The default implementation serializes requests to the Ollama `/api/chat` endpoint with a 10-minute timeout. On 200 OK, the full response body is read; on non-200, up to 1024 bytes of the error body are captured.

## Metadata Cache

`MetadataCache` persists file state and configuration across engine runs. It stores a version, a `stepsHash` derived from model ID and all prompt templates, per-file SHA-256 hashes with associated facts, and module-to-path mappings. The cache is stored as `.metadata.json` in the docs directory. If the version is incompatible, it resets.

## Error Handling

Errors propagate via return values. No panics or crashes are present. The cobra framework displays returned errors and exits with a non-zero status code. Specific cases: file read failures during extraction are swallowed and logged via `onStatus`; stale cleanup failures are silently ignored; lock acquisition and cache save failures are fatal.

## Filesystem Safety

All filesystem writes use atomic semantics: `WriteFileAtomic` writes to a temporary file first, then renames to the target path. `WriteFileSafely` uses atomic write semantics. `RemoveFileSafely` ignores `os.IsNotExist` errors. `tools.WriteFileAtomic` is used for config persistence.

## Git Integration

`VerifyGitRepo(repoRoot)` verifies the repository root resides inside a git repository by executing `git rev-parse --is-inside-work-tree`. `EnsureGitignoreHasLockfile` ensures `.code-reducer.lock` is included in `.gitignore`. `LoadGitignore` loads patterns for ignore pattern matching during file discovery.

## State and Concurrency

No synchronization mechanisms are present. Global variables (`initCmd`, `setupCmd`, `updateCmd`) are declared at package scope and initialized once; they are never modified after initialization. No mutexes, locks, atomic types, or async patterns are used.