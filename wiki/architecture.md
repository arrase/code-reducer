# Wiki / Architecture Overview

## System Boundaries and Interaction Topology

Code-Reducer is an automatic software-documentation generation system driven by Ollama-compatible LLMs. It operates on a repository's source code tree to produce markdown artifacts describing architecture, module boundaries, business logic, state management, error handling patterns, and agent guidelines. Execution enters the system through `internal/engine/runner.go` (`Runner.Run`) and flows across four subsystems before returning control to the caller.

```
┌───────────────┐     ┌───────────────┐     ┌───────────────┐     ┌───────────────┐
│   config      │────▶│   security    │────▶│   tools        │────▶│   engine       │
│  (resolution) │     │ (path-boundary│     │ (I/O, git, tree│     │ (LLM pipeline) │
│               │     │  enforcement) │     │  discovery, I/O│     │               │
└───────────────┘     └───────────────┘     └───────────────┘     └───────────────┘
```

- **`config`** resolves all configuration state (CLI flags, environment variables, YAML file, or hardcoded defaults) into a single `*Config` struct. It is the first subsystem called by the runner and owns all persisted state at `.code-reducer.yaml`.
- **`security`** enforces path-boundary rules against directory-traversal attacks and provides process mutual exclusion via OS-level file locking (`O_EXCL`). All filesystem operations in downstream subsystems are expected to resolve paths through `security.SafeResolve` first.
- **`tools`** performs repository traversal, safe file read/write with TOCTOU race mitigation, gitignore-aware filtering, binary classification, and Git subprocess execution. Every call is synchronous; no goroutines or channels exist in this package.
- **`engine`** implements the documentation generation pipeline: LLM client abstraction, JSON response normalization, chunked reduction for context-window management, hierarchical synthesis across directory trees, metadata caching, mode dispatch (`init` / `update`), and lifecycle orchestration via `Runner.Run`.

### Execution Flow

1. `Runner.Run(ctx, repoRoot, mode, onEvent)` acquires an exclusive lock through `security.AcquireLock`, then delegates to either `client.RunInit` or `client.RunUpdate` based on the mode string.
2. The LLM client in `engine/client.go` handles all network communication; errors flow back to the runner where they are propagated without wrapping (except for unsupported-mode and lock-acquisition failures).
3. All disk I/O within the engine is routed through `tools.WriteFileSafely` / `ReadFileSafely`, which themselves call `security.SafeResolve` for path validation before touching any file.
4. Concurrency protection at the filesystem level uses OS-level locking managed by `security.AcquireLock`; no synchronization primitives (`sync.Mutex`, channels, atomics) are used anywhere in the codebase.

---

## Configuration Resolution Chain

The configuration subsystem implements a strict four-tier priority chain: CLI flags → environment variables → YAML file → hardcoded defaults. Persisted state lives at `.code-reducer.yaml` with `0600` permissions restricted to owner-only access.

| Tier | Source | Override Target |
|---|---|---|
| 1 | CLI flag `--model-id` or env | `Config.ModelID`, `OllamaBaseURL` |
| 2 | CLI flag `--num-ctx` or env | `Config.OllamaNumCtx` |
| 3 | `.code-reducer.yaml` via `LoadConfig` | Ignore lists, extraction steps (falling back to defaults) |
| 4 | Package-level read-only constants (`DefaultIgnores`, `DefaultIgnoredExtensions`, `DefaultExtractionSteps`) | Final fallback for empty slices at resolve time |

The resolution function `config.ResolveConfig(repoRoot, modelIdFlag string, numCtxFlag string)` orchestrates the full pipeline. Load failures are swallowed—callers receive an empty pointer with no error return. MergeAndDeduplicate ensures user-provided values take precedence when duplicates exist across merge boundaries.

---

## Security Contracts

Three infrastructure contracts are enforced without any synchronization primitives:

- **Path-boundary enforcement** — `security.SafeResolve(repoRoot, inputPath)` requires resolved absolute paths to stay strictly inside the repository root. Any detected escape via `..` prefix returns a sentinel error `"security violation: path traversal detected"`.
- **Process mutual exclusion** — `AcquireLock(repoRoot)` creates an atomic lockfile (`O_CREATE|O_EXCL`). If it already exists, acquisition fails with a "lock held" message. Release closes and removes the file unconditionally via deferred cleanup in `Runner.Run`.
- **Version-control exclusion** — `EnsureGitignoreHasLockfile(repoRoot)` ensures `.code-reducer.lock` is listed in `.gitignore`, preventing git from tracking it.

---

## Repository Discovery and Safe I/O

The tools subsystem provides three independent capability groups: code discovery, safe file operations with TOCTOU mitigation, and Git utilities. All calls are synchronous; no goroutines or channels exist anywhere in the package.

**Code discovery** walks `repoRoot` recursively via `filepath.WalkDir`, skipping directories matching `.gitignore` patterns (compiled by `LoadGitignore`) or any dot-prefixed component, then filters leaves through `ShouldIgnoreFile`. Walk callback errors are swallowed—individual directory entries that fail to stat return `nil` to the caller.

**Safe file read/write** operations resolve paths through `security.SafeResolve` first, then perform actual I/O:
- `ReadFileSafely` wraps both resolution and read failures with `"failed to read file:"`.
- `WriteFileSafely` performs TOCTOU-safe writes via `os.MkdirAll`, open for write+create, lstat/stat comparison (detecting symlink swaps or race conditions), truncate, then content write. Symlink races return errors prefixed `"security violation"` / `"TOCTOU symlink race detected"`.

**Git utilities** provide subprocess execution (`RunGit` with `--no-pager` prepended) and repo verification (`VerifyGitRepo` discarding inner error if git query fails).

---

## LLM Client Abstraction

The engine package provides synchronous and streaming communication with an Ollama-compatible LLM API via `engine/client.go`. The client is immutable after construction; all fields are assigned only in `NewLLMClient`. Both modes share a base system prompt that establishes agent identity and operating rules.

- **`CallLLM(messages []Message) (*Response, error)`** — Builds a JSON request body combining the configured model ID, user-supplied messages, and a leading system prompt message. POSTed to `<BaseURL>/api/chat` with `"Content-Type: application/json"`. On success (200), the full response body is read into memory and parsed once; on non-200 status, up to 1024 bytes of the error body are captured for reporting. A 10-minute timeout applies globally.
- **`StreamLLM(messages []Message, onChunk func(string)) error`** — Performs an identical outbound POST but with streaming enabled. Each line from the response is read via `bufio.NewReader`, trimmed, and parsed as JSON. Non-empty content segments invoke the caller-supplied `onChunk` callback immediately. The loop exits on EOF or when a chunk's `Done` flag is true. Invalid JSON lines inside the stream are silently skipped—no error returned to the caller unless the reader itself fails.
- **`GetBaseSystemPrompt()`** / **`GetDefaultSystemPrompt(command)`** — Returns static base prompt text; switches on command for per-module or global architecture compositions.

---

## Response Parsing and Chunk Reduction

### JSON Normalization Pipeline

The parser tolerates markdown code fences (with or without language tags) and handles bracket/brace mismatches within embedded text by walking character-by-character while respecting string boundaries:
- **`StripOuterMarkdownFence(input)`** — Trims whitespace, applies regex to match content between ```` ``` ` (with optional `markdown`/`json` tag), captures inner text. No match returns input unchanged.
- **`CleanJSONResponse(input)`** — Scans for first `{` or `[`, walks forward tracking an `inString` flag to skip escaped characters inside quoted strings, pushes opening delimiters onto a stack, pops on matching close. Returns substring from start index up to and including the closing character once the stack drains. If neither delimiter is found, input passes through unchanged.
- **`UnmarshalJSONResponse(input)`** — Passes cleaned string directly to `encoding/json.Unmarshal`. Errors are wrapped with context: `"failed to unmarshal JSON (cleaned: %q): <original>"`.

### Chunk Reduction Algorithm

Implements a recursive chunk-reduction algorithm that processes collections of code/architecture items through an LLM-based synthesis pipeline and produces a single consolidated output. Items are grouped into batches bounded by `maxChunkChars` (3× the LLM context window), then each batch is synthesized independently, and intermediate results are concatenated and reduced again until one item remains—a tree-like reduction pattern:

1. **Empty input** — Returns `("", nil)` immediately.
2. **Single-item truncation** — When exactly one item exceeds the context window, it is mutated in-place to `<items[0][:maxChunkChars>\n...[truncated]>`. No error returned; processing continues.
3. **Batching phase** — Items are greedily grouped into batches where each batch's combined length stays within `maxChunkChars`. Overflow starts a new batch.
4. **Fallback truncation** — If every item exceeds limits individually, each is proportionally truncated and forced into a single batch to prevent deadlock on unbatchable inputs.
5. **Single-batch terminal** — When exactly one batch exists after grouping, the entire set is sent directly to `CallLLM` for synthesis; result is stripped of markdown fences before return.
6. **Multi-batch recursion** — Each batch is recursively reduced via `ReduceInChunks`. Intermediate results are concatenated into a single list, which then triggers another recursive call until reaching the base case where all items fit in one context window.

Recursive-call errors and LLM synthesis errors propagate directly. Synthesis errors are wrapped with `"LLM error during synthesis: %w"`. Truncation is a silent side effect, not an error path—no warning logged unless it's the only item (in which case `logEvent` is invoked).

---

## Hierarchical Synthesis and Metadata Caching

**`SynthesizeNode(node *DirNode, cache MetadataCache) (string, error)`** — Recursively walks a directory tree (`DirNode`), producing synthesized textual summaries for each node. Each directory's summary is built from child summaries plus per-file facts extracted by LLM calls. Unchanged files reuse cached SHA256 hashes and previously extracted facts; changed or new files trigger fresh extraction via configurable `ExtractionSteps`.

Algorithmic flow:
1. **Context check** — Aborts early if context is cancelled.
2. **Cache hit short-circuit** — If node is not affected (`affectedDirs` map) and a cached summary exists, returns immediately without LLM calls.
3. **Recursive child synthesis** — Children are sorted; each synthesized in order. Non-empty summaries accumulate into `childSummaries`.
4. **File fact extraction loop** — For each file: compute SHA256 via `computeSHA256`; decide cache hit vs. LLM-driven extraction; accumulate facts. Read errors on individual files are swallowed (`continue`).
5. **Component assembly** — File components followed by child subsystem summaries form an ordered slice.
6. **Chunked reduction** — All assembled components feed `ReduceInChunks` to produce the directory's final summary.
7. **Cache update & persist** — The result is stored in both `MetadataCache.Files[f]` (with new hash and facts) and `MetadataCache.Modules[node.Path]`. Module docs are written under `<docsDir>/modules/<safe-filename>.md` via `tools.WriteFileSafely`; write errors are wrapped.

Error propagation: LLM call failure during extraction is fatal for the node—wrapped as `"LLM error extracting %s for %s: %w"` and stops all further processing. No partial results returned. SHA256 computation or file read failures on individual files are swallowed; other files still contribute components. The result may be incomplete but not an error.

---

## Directory Tree and Affected-Directory Propagation

The `DirNode` hierarchy defines the in-memory representation: each node holds its own file list and a map of child subdirectories keyed by name. Individual directories are flagged as affected if any direct file appears in the change set, or if no cached module entry exists for the path, or if `modules/<safe-filename>.md` does not exist at the expected location under `docsDir/modules`. Once individual nodes are flagged, ancestors are recursively marked affected by walking upward—ensuring parent summaries reflect changes even when they themselves contain no direct file modifications.

---

## Orchestrator and Mode Dispatch

The orchestrator implements the automatic documentation generation pipeline for software repositories. Coordinates code discovery, tree building, hierarchical synthesis, metadata cache operations, standard doc regeneration, AGENTS.md maintenance, and module cleanup. Emits typed `Event` objects (type + message) so callers can observe pipeline progress without parsing log output.

### Init Flow
1. Load `.gitignore`, apply configured ignore lists, scan repository for source-code files.
2. Convert flat file list into a hierarchical `DirNode` tree rooted at the repo root via `buildTree`.
3. Walk the tree bottom-up; each node summarized by LLM invocation. The root node's summary becomes `rootSum`.
4. Invoke LLM to write `architecture.md` and `quickstart.md`, passing `rootSum` as context. Errors wrapped with `"failed to generate ...: %w"`.
5. Append or create an "AI Agent Guidelines" section in `AGENTS.md`; if file exists and already contains the marker, skip. Write errors swallowed.
6. Persist cache via `saveMetadataCache` with a `"local"` commit marker.

### Update Flow
1. Re-discover code files using identical filtering logic.
2. Detect changes via SHA256 comparison against cached hashes in `MetadataCache.Files`. Differences classify into `"Added"`, `"Modified"`, or `"Deleted"`.
3. For every changed file, walk upward from its parent to the root directory, marking each ancestor as affected. Deleted files removed from cache immediately; modules whose directories are no longer active purged via `os.Remove` (errors ignored).
4. Rebuild tree and prune cache so it reflects only currently existing code.
5. Propagate affect markers through the tree via `propagateAffected`. If no directories are affected, short-circuit with a "no modifications" message; return without further LLM calls.
6. Synthesize only affected nodes via `SynthesizeNode`, skipping unaffected subtrees entirely—reusing cached summaries for them.
7. Regenerate `architecture.md`/`quickstart.md` if they are missing or any top-level directory was affected; otherwise reuse existing files.
8. Persist updated cache with `"local"` commit marker.

Cross-cutting rules: **Gitignore stacking** — `.gitignore`, configured ignore lists, and the `docsDir` itself are all applied together when discovering code files. **Safe file resolution** — All paths resolved through `security.SafeResolve` before any filesystem operation. **Atomic writes** — Documentation written via `tools.WriteFileSafely`. **Module cleanup** — Modules whose directories no longer appear in the active set removed from disk; removal errors ignored (`_ = os.Remove`).

---

## Runner and Lifecycle Management

The runner acts as the orchestrator for code analysis or documentation pipelines. Manages lifecycle execution from initialization through completion, handling state transitions based on a provided mode string. Ensures repository integrity by verifying that the lockfile is excluded from version control before executing pipeline logic.

- **`NewRunner(cfg *config.Config) *Runner`** — Factory for creating a new runner instance.
- **`Run(ctx context.Context, repoRoot string, mode string, onEvent func(Event)) error`** — Main entry point. Acquires an exclusive lock through `security.AcquireLock`; if acquisition fails, returns immediately with wrapped error. A deferred unlock releases the lock unconditionally regardless of how the function exits.

Mode dispatch:
- `"init"` → calls `client.RunInit(ctx, repoRoot, r.cfg, onEvent)`
- `"update"` → calls `client.RunUpdate(ctx, repoRoot, r.cfg, onEvent)`
- Any other value → returns `fmt.Errorf("unsupported mode: %s", mode)` as a fatal error.

Error handling summary for runner:

| Step | Behavior on Failure |
|---|---|
| `EnsureGitignoreHasLockfile` | Swallowed; logged as warning via `onEvent`. Execution continues. |
| `AcquireLock` | Fatal, wrapped: `"failed to acquire repository lock: %w"`. Caller sees original error wrapped. |
| `client.RunInit` / `RunUpdate` | Propagated raw without wrapping. Deferred lock cleanup still runs. |
| Unsupported mode | Fatal, wrapped: `"unsupported mode: %s"`. |

---

## Utilities for Markdown Output Generation

Maps internal identifiers (module paths) onto safe, human-readable filenames for markdown output. Operates at the boundary between the engine and the documentation generation system.

- **`ToSafeMarkdownFilename(modulePath string)`** — All filesystem separator characters replaced with underscores (`_`). This normalizes platform-specific path separators into a single ASCII character. If resulting string is empty or equals `"."`, it is replaced with literal string `"root"`. A `.md` suffix appended, signaling that the output is a markdown document file.

The mapping is deterministic and pure—same input always yields same filename. No error return for invalid inputs. All transformations are best-effort with silent normalization.