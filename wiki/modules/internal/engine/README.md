# Module: `internal/engine` — Automated Documentation Synthesis Engine

## 1. Module Responsibility & Data Flow

The `internal/engine` package implements an end-to-end pipeline that **discovers source files in a repository, classifies their change status against cached state, recursively synthesizes hierarchical summaries via LLM calls, and persists the results as markdown documentation**. The pipeline operates in two modes:

- **Init** (`ModeInit`) — full regeneration of all documentation.
- **Update** (`mode ModeUpdate`) — incremental reprocessing limited to directories whose descendants contain changed files.

The data flow proceeds from `Runner.Run()` → orchestrator selection → tree construction → per-node synthesis (bottom-up) → global doc generation → cache persistence. All external dependencies funnel through the `llmCaller` interface, enabling transport-level abstraction without coupling the engine layer to a specific LLM provider.

---

## 2. Types & Shared State

### Core Structs

| Type | Scope | Purpose |
|------|-------|---------|
| `orchestrator` | package-private | Orchestrates both init and update pipelines; embeds an `llmCaller`. |
| `Runner` | public (`internal/engine`) | Entry point for pipeline execution. Holds a pointer to `config.Config`. |
| `Message` (exported) | public | LLM protocol message envelope: `{Role, Content}`. Used by the Ollama client. |
| `DirNode`, `FileChange`, `ChangeStatus` | package-private / tree.go | Directory tree node with path, file list, and child map; file-change record for change detection. |
| `MetadataCache`, `FileCacheEntry` (exported) | public (`internal/engine`) — from `cache.go` | Top-level cache state holding per-file extraction facts and per-module directory summaries. |

### Supporting Types Referenced Externally

- `config.Config`, `llmCaller`, `pipelineContext` — declared elsewhere; referenced by orchestrator, runner, and synthesize modules.
- `security.SafeResolve` — path resolution helper used throughout for safe absolute-path construction.
- `tools.ReadFileSafely`, `tools.WriteFileSafely`, `tools.DiscoverCodeFiles`, `tools.LoadGitignore` — file-system helpers from the `tools` package.

---

## 3. LLM Client Layer (`client.go`)

The engine communicates with an external language model through a remote HTTP endpoint, specifically targeting Ollama's `api/chat` protocol. Interaction is abstracted behind the `llmCaller` interface so the engine layer can depend on it without tying to a specific transport implementation.

### Request Construction
1. Prepend any system prompt to the user-provided message list.
2. Serialize into Ollama's expected JSON schema (model ID, messages, stream flag, optional format/options).

### Execution & Response Parsing
3. Execute synchronous HTTP POST to `baseURL/api/chat` using a context-aware client with a fixed timeout; no retries are attempted.
4. On 200 OK, deserialize the JSON into an Ollama-style envelope and return only the model's reply content as the result string.

### Error Propagation
- **Non-OK status**: The body is read up to `maxErrorBodyBytes` (constant defined elsewhere) and the content is embedded in `"ollama api error: status {code}, response: {text}"`. If reading fails mid-stream, only the status code is reported; the underlying read failure is lost.
- **Successful status + parse error**: Response body is fully read into memory, then unmarshaled. JSON mismatch yields a wrapped error `"failed to parse response: %w"`.
- **Transport failure** (network down, DNS fail, timeout): Returned directly without retry logic; callers must handle their own backoff upstream.

### Notable Observations
- No circuit breaker or retry mechanism exists within this file. Transient network errors are not retried.
- `defaultHTTPTimeout` and `maxErrorBodyBytes` are referenced but defined elsewhere in the package (constants.go).

---

## 4. Metadata Cache Layer (`cache.go`)

The engine persists per-file extraction results ("facts") along with file integrity hashes, enabling change detection and incremental reprocessing between pipeline runs. The cache is stored as a versioned JSON file at `<docsDir>/<metadataFileName>` relative to `repoRoot`.

### Initialization & Loading
1. **Initialize empty cache** — creates versioned metadata container with maps ready to grow (files, modules).
2. **Load existing cache from disk** — reads the serialized metadata file; if missing (`os.ErrNotExist`), returns a fresh empty cache. Incompatible versions are silently recovered by returning a clean slate rather than failing.
3. **Nil safety**: Maps initialized to nil on load are replaced with fresh empty maps before use.

### Persistence & Integrity Tracking
4. **Persist cache state back to disk** — serializes the current cache (versioned, indented) and writes it atomically via `tools.WriteFileSafely`.
5. **Track extraction step history** — marshals the list of extraction steps into JSON and hashes them; this hash is stored as `steps_hash` in the top-level cache, enabling detection when the processing pipeline itself changed between runs.

### Domain Rules
- Cache versioning: only versions matching `currentCacheVersion` (constant = 1) are accepted; mismatches yield a clean cache + nil error.
- Graceful degradation: missing files and read failures return an empty cache instead of errors.
- File hash is computed from raw content at the repository root using virtual paths, not relative paths.

---

## 5. Tree Construction & Change Detection (`tree.go`)

This code provides filesystem change propagation analysis — it builds an in-memory directory tree from file paths, then determines which directories are affected by a set of file changes (additions, modifications, deletions) by traversing the tree and marking parent directories whose children have changed or been removed.

### Algorithm Steps
1. **Build Directory Tree**: Parse each input file path into hierarchical components (`/`-separated), recursively constructing `DirNode` objects that represent folders with their files and child subdirectories. Files at the root level go directly under the tree root node; nested paths create intermediate directory nodes.
2. **Initialize Affected Set**: Start with an empty set of affected directories. For each file change, if its status is "Deleted", immediately mark its parent directory as affected.
3. **Tree Traversal to Detect Changes**: Walk every `DirNode` in the tree:
   - If any file under that node appears in a changed-file map, mark the node's path as affected.
   - Check whether a corresponding markdown module path exists at disk (relative to `docs/modules/`). If it does not exist, mark the directory as affected — implying the module is being added or removed.
   - If the cached metadata for that path is empty, treat the directory as newly created and mark it affected.
   - Recursively process all child directories.
4. **Propagate Affected Status**: After initial detection, walk the tree a second time: if any descendant is marked affected, propagate that status upward to parent nodes so ancestors of changed subtrees are also flagged.

### Error Propagation
- `security.SafeResolve` errors in `determineAffected` are silently swallowed — only the success case proceeds to disk check.
- `buildTree` performs no error handling; internal logic failures leave state undefined with no recovery path.

---

## 6. Hierarchical Synthesis Pipeline (`synthesize.go`)

This code implements an automated codebase summarization engine that recursively analyzes source files and directories, extracting structured facts about each file's purpose/behavior via LLM calls, then synthesizing hierarchical summaries upward through a directory tree — ultimately producing per-directory documentation artifacts.

### Algorithm Steps
1. **Tree Traversal (Bottom-Up)**: Recursively process a directory node by first visiting all child directories (sorted for determinism), then processing files within the current directory.
2. **File Fact Extraction**:
   - Check cache and precomputed hashes before reading file content.
   - Read raw file bytes, compute SHA-256 hash if not already cached.
   - Split file content into overlapping chunks (size dynamically scaled from LLM context window).
   - For each chunk, invoke an LLM with a system prompt + step-specific user prompt to extract facts.
   - Consolidate all chunk results through a reduction step per extraction step.
   - Repeat across multiple sequential extraction steps until all perspectives are captured.
3. **Component Assembly**: Combine extracted file summaries and synthesized child-directory summaries into a unified list of components for the current directory node.
4. **Directory Synthesis**: Apply multi-step LLM-based chunked reduction on the assembled components to produce a single consolidated summary for the entire directory.
5. **Persistence & Caching**: Store computed hashes, file facts, and directory summaries in metadata caches; write final directory summaries to disk as markdown documentation under `cfg.DocsDir/modules/<safe-filename>`.

### Mutable State
- `pipelineContext.cache.Files` — Modified in `extractFileFacts`: `{SHA256: fileHash, Facts: facts}`.
- `pipelineContext.cache.Modules` — Modified in `synthesizeNode`: cleared when no components exist, set to final summary after synthesis completes.
- `pipelineContext.affectedDirs` — Read for membership checks during per-node processing; not modified within this file.

### Error Propagation
- **File read failure**: Logged as a warning via `logEvent(EventStatus, ...)`. Returns `"", nil` — effectively swallowed. No error propagates up.
- **Chunking failure** (`chunkTextWithOverlap`): Wrapped in `"failed to chunk file %s: %w"`. Propagates upward from `extractFileFacts`.
- **LLM call failure**: Wrapped in `"LLM error extracting %s for %s: %w"`. Propagates upward. Returns `"", error` to caller.
- **Directory synthesis** (`reduceInChunks`): Wrapped in a single return via `"failed to write module documentation for %s: %w"`. Propagates upward.
- Context cancellation short-circuits both synthesis and per-file processing without generating partial output.

---

## 7. Chunking & Reduction Algorithms (`chunking.go`)

This module implements a **map-reduce tree reduction** algorithm for LLM-based code synthesis. It takes multiple text items (code facts, file descriptions, architecture notes), batches them within context-window limits, sends each batch to an LLM, and recursively reduces the outputs until a single consolidated result remains — stopping when further reduction would not shrink the output significantly (loop prevention).

### Algorithm Steps
1. **Input Validation** — Return empty string if no items provided; return item as-is for single-item inputs in `reduceFileFacts`.
2. **Pre-Expansion** — Any individual item exceeding the character limit is split into smaller overlapping chunks via `chunkTextWithOverlap`, then all resulting pieces are pooled back into a flat list.
3. **Binning by Size** — Items (and pre-expanded chunks) are grouped into batches such that no batch exceeds `maxChars` runes, with overflow items starting a new batch.
4. **Recursive Reduction** — Each batch is sent to the LLM via `reduceFn`. The function calls itself recursively on each batch's result until only one item remains in `intermediate`.
5. **Loop Prevention Check** — Before recursing again, total output runes are compared against 95% of total input runes. If output ≥ 95% of input (information is not being condensed), the algorithm stops and returns all intermediate results concatenated with double newlines, preserving information without exceeding context windows.
6. **LLM Integration** — The LLM caller receives a system prompt (e.g., "Synthesize architecture for {nodePath}" or "Consolidate facts for {filePath}") plus user content formed by joining batch items with double newlines. Markdown fences are stripped from the response before return via `stripOuterMarkdownFence`.

### Error Propagation
- LLM errors (`c.CallLLM`) are wrapped with `"LLM error during synthesis"` or `"LLM error during file fact consolidation"`.
- Context cancellation (`ctx.Err()`) is checked at entry to `reduceItems` and after each recursive batch call; cancelled context returns the error directly without wrapping.
- Internal recursion failure propagates straight up through recursive `reduceItems` calls without additional wrapping.

---

## 8. Orchestrator Pipeline (`orchestrator.go`)

This code implements a Map-Reduce pipeline that automatically generates and maintains documentation for software repositories by recursively analyzing source code structure with an LLM, then writing the synthesized results back as markdown files (architecture overview, quickstart guide, per-module summaries).

### Algorithm Steps
1. **Code Discovery** — Locate all code files in the repository root, filtering out documentation directories and patterns from `.gitignore` + user-configured ignore lists.
2. **Hash-Based Change Detection** — Compute SHA256 hashes for each discovered file and compare against a cached state to classify changes as Added, Modified, or Deleted.
3. **Tree Construction** — Organize the code files into a hierarchical directory tree structure (`DirNode` with children).
4. **Affected Directory Determination**:
   - *Init mode*: Mark all directories as affected (full regeneration).
   - *Update mode*: Identify only those directories whose descendants contain changed files, propagating "affected" status upward through the tree.
5. **Hierarchical Tree-Merging** — Recursively synthesize each node starting from leaves and working toward the root. Each synthesis call to the LLM produces a summary of that directory's code, which becomes input for parent-level synthesis (reducing as you move up).
6. **Standard Documentation Generation** — After the tree is fully synthesized, generate two global documents:
   - `architecture.md` — High-level system overview based on the root synthesis output.
   - `quickstart.md` — Onboarding and usage guide derived from the same root summary.
7. **Agent Guidelines Update** — Write or append an AI Agent Guidelines file that references all generated documentation paths, ensuring future agent interactions are informed by existing docs.
8. **Cache Maintenance**:
   - *Pruning*: Remove cache entries for directories no longer present in the code tree and delete their corresponding markdown files on disk.
   - *Invalidation*: If extraction steps configuration changes, reset the file cache to force full regeneration on next run.

### External I/O & Error Patterns
- **LLM API calls**: Delegates to `o.client.CallLLM(ctx, sysPrompt, messages, false)` with a system prompt constructed from `cfg.SystemPrompt + cfg.ArchitecturePrompt`. Called twice per run — once for `architecture.md`, once for `quickstart.md` (in `GenerateStandardDocs`). The pipeline's hierarchical synthesis (`synthesizeNode`) also calls the LLM internally.
- **Disk writes**: Architecture and quickstart docs are written via `tools.WriteFileSafely` after stripping outer markdown fences; modules directory is created with `os.MkdirAll` using `defaultDirPerm`. Agent file is either overwritten or appended depending on content presence. Stale module files are removed via `security.SafeResolve` + `os.Remove`.
- **Swallowed errors**: Missing `.gitignore` (if not present) returns empty list without error; hash computation failures per file log warnings and continue processing; cache save failure in teardown logs a warning but never returns an error, meaning post-run state may be inconsistent if the cache write fails.

---

## 9. Runner Orchestration (`runner.go`)

This code manages and orchestrates an automated documentation generation pipeline for software projects, supporting both initial documentation creation (init mode) and incremental updates to existing documentation (update mode). It serves as the main entry point that coordinates AI-powered document processing across a repository.

### Algorithm Steps
1. Ensure project lockfile is added to `.gitignore` for version control safety.
2. Acquire an exclusive repository lock to prevent concurrent documentation operations.
3. Initialize LLM client and orchestrator based on provided configuration (model, base URL, context settings).
4. Execute the appropriate pipeline mode: either run full initialization or incremental update using the configured AI model.

### Synchronization & Locking
- A repository-level lock is acquired via `security.AcquireLock(repoRoot)` and released via `defer lock.Unlock()`. This protects repository state during pipeline execution. The exact lock type (file-based, in-memory) depends on implementation in `internal/security`.
- All other struct fields are assigned once in `NewRunner()` and never modified after that; no mutable shared state exists within this file's boundary.

---

## 10. Utility Helpers (`utils.go`)

This file provides lightweight utility helpers for the engine module: generating safe markdown filenames from directory paths, and creating adapter-style log event callbacks that support optional listeners.

### Domain Rules
- Empty/`.` module paths map to `"README.md"` by default via `toSafeMarkdownFilename`.
- Unknown or invalid callbacks are silently swallowed — no panic on nil listeners in `makeLogEvent`.

---

## 11. Constants & Configuration (`constants.go`)

This file defines operational configuration constants for an AI inference/agent engine runtime. It establishes fixed parameters that govern timeout behavior, error handling limits, context window management, and filesystem conventions used during agent execution.

| Constant | Purpose |
|----------|---------|
| `defaultHTTPTimeout` (10 minutes) | Maximum duration for remote API calls before abort. |
| `maxErrorBodyBytes` (1 KB) | Limits how much error body data can be captured from failed requests. |
| `defaultChunkOverlap` (800 tokens) | Overlap between consecutive text chunks during streaming or processing. |
| `minNumCtxFloor` (512) | Minimum threshold for context length calculations. |
| `contextWindowAllocRatio` (0.75) | Reserves 75% of available space for primary content, remainder for metadata/overhead. |
| `maxCharsMultiplier` (3x) | Multiplies base character limit to determine maximum allowed output length. |
| `metadataFileName`, `agentsFileName`, `defaultDirPerm` | Filesystem conventions for state persistence during agent runs. |

---

## 12. Test Coverage (`chunking_test.go`, `tree_test.go`)

### Chunking Tests — Inferred Signatures
- `chunkTextWithOverlap(text string, maxRunes int, overlapRunes int)` → `([][]string, error)`: Splits arbitrary text into fixed-size chunks with configurable overlap. Validates that `maxRunes > 0` and `overlapRunes < maxRunes`. Short texts return single-element slices; exact-fit texts also return single elements.
- `reduceItems(ctx context.Context, items []string, maxChars int, reduceFn func([]string) (string, error))` → `(string, error)`: Reduces an ordered slice of items into a single string while respecting a character limit. All input items must still appear somewhere in the final output after reduction.

### Tree Tests — Inferred Signatures
- `buildTree(files []string)` → `*DirNode`: Parses flat file paths into hierarchical directory structures; files at root level go directly under tree root node; nested paths create intermediate directory nodes.
- `determineAffected(tree *DirNode, tempDir string, docs string, cache Cache, changes []FileChange)` → `map[string]bool`: Walks the tree while tracking file changes; marks modules whose source files changed as affected; unrelated branches remain unaffected.
- `propagateAffected(tree *DirNode, affected map[string]bool)` → `map[string]bool`: Propagates affected status to parent directories after initial detection — if any descendant is marked affected, ancestors are also flagged (transitive propagation).

### Test Observations
- All test functions invoke production methods without checking return values or handling errors; any errors they produce are swallowed. Assertions use `t.Errorf` and `assert.True` patterns with no caller chain visible in tests alone.