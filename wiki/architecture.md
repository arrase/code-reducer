# Architecture — Code-Reducer

## System Overview

Code-Reducer is an AI-driven code synthesis tool that scans Go repositories and produces structured Markdown documentation (wiki pages) via a Map-Reduce pipeline orchestrated by LLM calls. The entire system operates within the repository filesystem; no network or database interactions occur outside the Ollama HTTP transport used by `internal/engine`. All paths are resolved relative to a caller-supplied `repoRoot`, with strict boundary enforcement.

The system follows a **Map-Reduce** pattern: source files are discovered (map phase), facts are extracted per-file via LLM calls, and module summaries are synthesized bottom-up through directory traversal (reduce phase). The CLI entry point validates environment state, resolves configuration from disk or defaults, verifies engine mode consistency with project lifecycle state, instantiates the runner, and executes the documentation pipeline.

---

## Module Boundaries and Interaction

```
┌─────────────┐     ┌──────────────┐     ┌────────────────┐     ┌──────────────────┐
│  cmd        │────▶│ internal/    │────▶│   security      │────▶│   tools          │
│ (cobra)     │◀────│ config       │◀────│ (path resolution│◀────│ file + git ops    │
│ entry point │     │ resolution  │     │  & locking)     │     │ read/write/ignore│
└─────────────┘     └──────────────┘     └────────────────┘     └──────────────────┘
         │                                              │                  │
         ▼                                              ▼                  ▼
    ┌─────────────────────────────────────────────────────────────────────┐
    │                    internal/engine (orchestration layer)            │
    │  ├── Runner.Run()           acquires lock, dispatches mode         │
    │  ├── orchestrator           Map-Reduce pipeline core               │
    │  ├── llmClient              Ollama HTTP transport abstraction      │
    │  ├── cache                  Metadata persistence (JSON)            │
    │  ├── synthesis engine       Recursive directory processing          │
    │  ├── tree building          DirNode hierarchy & affected detection │
    │  └── chunking/reduction     Context window management              │
    └─────────────────────────────────────────────────────────────────────┘
```

### `cmd` → `internal/config`

The CLI layer (`cmd`) calls `config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag)` to merge CLI flags → environment variables → YAML config file → built-in defaults into a fully-resolved `*config.Config`. The priority chain is strict and deterministic. All resolution logic lives in `internal/config/resolve.go`; no network or external API calls occur here.

### `cmd` ↔ `internal/security`

The orchestrator acquires an exclusive lock via `security.AcquireLock(repoRoot)` before executing the pipeline. Path validation uses `security.SafeResolve` to prevent directory escape during all file operations. These two functions are also called by every I/O operation in `tools`, ensuring consistent boundary enforcement.

### `cmd` ↔ `internal/tools`

The CLI layer delegates all filesystem reads/writes/discovery to `internal/tools`. Functions like `ReadFileSafely`, `WriteFileSafely`, and `DiscoverCodeFiles` are called throughout the pipeline. All paths passed to these functions must be resolved via `SafeResolve` first, then forwarded with a fixed `repoRoot` argument.

### `cmd` ↔ `internal/engine`

The CLI entry point constructs `engine.NewRunner(cfg)` from the resolved configuration and invokes `Run(ctx, repoRoot, mode, callback)`. The runner acquires the lock internally, instantiates an orchestrator bound to an Ollama HTTP client, then dispatches either init or update based on the supplied mode string. All pipeline I/O lives inside this package; no calls cross back into other packages during execution.

### `internal/engine` internal dependencies

Within the engine package:
- **`orchestrator`** coordinates the full Map-Reduce flow. It loads `.gitignore`, merges with user-supplied ignore lists, persists a `MetadataCache` (JSON at `<docsDir>/<metadataFileName>`), computes SHA256 hashes per discovered file, builds a `*tree.DirNode` hierarchy, determines affected directories, and propagates the "affected" status bottom-up through directory traversal.
- **`llmClient`** handles all LLM communication via the Ollama HTTP transport at `baseURL/api/chat`. Messages are built by prepending a system prompt as role `"system"` followed by user/assistant messages in order. On 200 responses, the body is read and unmarshaled into an Ollama response schema; non-200 errors have their bodies truncated at `maxErrorBodyBytes` bytes.
- **Chunking & reduction** functions (`reduceWithLLM`, `reduceInChunks`, `chunkTextWithOverlap`) manage context windows by splitting oversized items with a default overlap of 800 runes, batching greedily until each batch fits within the character limit, then reducing via LLM calls per batch. A loop prevention check stops recursion when output reaches ≥95% of input rune count.
- **`pipelineContext`** holds references to the LLM client, repo root, config, affected directories map, precalculated hashes map, and event callback. It drives `extractFileFacts` (which reads file bytes or cache, chunks content with overlap, calls LLM per chunk per extraction step, strips markdown fences, reduces results) and `synthesizeNode` (which combines child summaries into a parent summary and writes the module's `.md` artifact to disk under `<docsDir>/modules/<safe-filename>.md`).
- **Tree building** (`buildTree`, `determineAffected`, `propagateAffected`) constructs nested `*tree.DirNode` entries from file paths. For each node, a safe markdown filename is computed and existence checked via `os.Stat`; if absent → mark affected (only `os.IsNotExist(err)` checked). Cross-reference with cache modules: empty string entry marks affected.
- **Standard docs generation** (`GenerateStandardDocs`) issues two LLM calls (architecture + quickstart) using the system prompt concatenated with architecture prompts; both are wrapped in error handlers that return prefixed failure messages.

---

## Data Flow Summary

```
OS env / CLI flags → cmd.ResolveConfig() → *config.Config
repoRoot           → security.SafeResolve() → absolute path (symlink-safe)
*config.Config     → engine.NewRunner(cfg).Run(ctx, repoRoot, Mode, callback)
                     │
                     ├─ Lock acquisition via security.AcquireLock(repoRoot)
                     ├─ metadata cache load/save at <docsDir>/<metadataFileName>
                     ├─ gitignore merge + code file discovery (tools.DiscoverCodeFiles)
                     ├─ SHA256 hashing per discovered file (computeSHA256)
                     ├─ DirNode tree construction → affected status propagation
                     ├─ Map: extractFileFacts(ctx, LLM, chunked content) per extraction step
                     ├─ Reduce: synthesizeNode(node *DirNode) bottom-up
                     ├─ Standard docs generation (architecture + quickstart) via 2× LLM calls
                     └─ metadata cache persist → completion event
```

---

## External I/O Summary

All interactions are local (disk + stdin/stderr). No network calls, database access, or API interactions occur outside the Ollama HTTP transport used by `internal/engine`. The global `setupCmd *cobra.Command` is constructed once at startup and not concurrently accessed in normal usage.