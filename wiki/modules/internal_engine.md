# Engine Module Architecture

## Overview and Data Flow

The `internal/engine` package implements an automatic software-documentation generation pipeline that uses a large language model (Ollama-compatible API) to analyze repository structure and produce markdown artifacts. The system operates in two modes—**init**, which generates fresh documentation from scratch, and **update**, which performs incremental regeneration based on detected code changes.

The execution entry point is the `Runner` struct in `runner.go`, which acquires an exclusive lock on the repository to prevent concurrent pipeline runs, instantiates an LLM client from configuration, and dispatches to either `RunInit` or `RunUpdate`. All disk I/O is funneled through the `tools.*` package; all network calls go through the `LLMClient`; concurrency protection is provided by a file-based lock acquired via `security.AcquireLock`.

The data flow for an update run follows this sequence:

1. **Safety preflight** — `EnsureGitignoreHasLockfile` verifies `.gitignore` contains the lockfile entry. Failure logs a warning and continues.
2. **Lock acquisition** — `security.AcquireLock(repoRoot)` writes a lock file; failure is fatal. A deferred unlock runs unconditionally.
3. **Client construction** — `NewLLMClient` creates an LLM client using `cfg.ModelID`, `cfg.BaseURL`, and `cfg.NumCtx`.
4. **Cache restoration** — `loadMetadataCache` reads `<docsDir>/.metadata.json`. Missing file yields a default-initialized empty cache; parse errors are wrapped for caller inspection.
5. **Code discovery** — Files under `<repoRoot>` are discovered respecting `.gitignore`, configured ignore lists, and the `docsDir` exclusion.
6. **Change detection via SHA256** — Each current file is hashed by `computeSHA256`. Hashes are compared against cached entries in `MetadataCache.Files`. Differences produce `"Added"`, `"Modified"`, or `"Deleted"` states (defined as `FileChange` in `tree.go`).
7. **Affected-directory propagation** — For each changed file, ancestor directories are marked affected. Deleted files trigger immediate cache purging and module-file removal. The tree is rebuilt from current paths; unaffected subtrees reuse cached summaries via a short-circuit check.
8. **Hierarchical synthesis** — `synthesizeNode` walks the directory tree bottom-up. Each node's summary is built from child summaries plus per-file facts extracted by LLM calls (one call per extraction step per changed file). Components are assembled and collapsed into a single module-level document via `reduceInChunks`.
9. **Root documentation regeneration** — `architecture.md` and `quickstart.md` are generated if they are missing or any top-level directory was affected. They are written through the same LLM pipeline with the root summary as context.
10. **Persistence** — The updated `MetadataCache` is marshaled back to `.metadata.json` via `saveMetadataCache`. Module summaries are persisted under `<docsDir>/modules/` keyed by a sanitized filename (via `ToSafeMarkdownFilename`).

The init run follows the same flow but skips change detection and affected-directory propagation, synthesizing all nodes from discovered code.

---

## LLM Client (`client.go`)

### Responsibility
Provides synchronous and streaming communication with an Ollama-compatible LLM API. The client is immutable after construction; all fields are assigned only in `NewLLMClient`. Both modes share a base system prompt that establishes agent identity and operating rules.

### Synchronous Mode — `CallLLM`
Builds a JSON request body combining the configured model ID, user-supplied messages, and a leading system prompt message. The request is POSTed to `<BaseURL>/api/chat` with `"Content-Type: application/json"`. On success (200), the full response body is read into memory and parsed once; on non-200 status, up to 1024 bytes of the error body are captured for reporting. The HTTP client has a 10-minute timeout applied globally.

### Streaming Mode — `StreamLLM`
Performs an identical outbound POST but with streaming enabled. Each line from the response is read via `bufio.NewReader`, trimmed, and parsed as JSON. Non-empty content segments invoke the caller-supplied `onChunk` callback immediately. The loop exits on EOF or when a chunk's `Done` flag is true. Invalid JSON lines inside the stream are silently skipped—no error returned to the caller unless the reader itself fails.

### Prompt Dispatch
- **`GetBaseSystemPrompt()`** — Returns static base prompt text.
- **`GetDefaultSystemPrompt(command)`** — Switches on `command`: `"module_synthesis"` returns a composition for per-module documentation; `"architecture"` returns a composition for global architecture documents.

---

## Response Parsing (`json_parser.go`)

### Responsibility
Normalizes raw LLM output into parseable JSON before downstream consumption. The parser tolerates markdown code fences (with or without language tags) and handles bracket/brace mismatches within embedded text by walking character-by-character while respecting string boundaries.

### Algorithmic Flow
1. **Whitespace trim** — Input is trimmed at entry.
2. **Fence removal** — A compiled regex matches content between ```` ``` ` (with optional `markdown`/`json` tag) and captures the inner text. No match returns input unchanged.
3. **Bracket-depth extraction** — Scans for the first `{` or `[`. Walks forward tracking an `inString` flag to skip escaped characters inside quoted strings. Pushes opening delimiters onto a stack; pops on matching close. Returns the substring from start index up to and including the closing character once the stack drains. If neither delimiter is found, input passes through unchanged.
4. **Unmarshal** — The cleaned string is passed directly to `encoding/json.Unmarshal`. Errors are wrapped with context: `failed to unmarshal JSON (cleaned: %q): <original>`.

### Error Characteristics
- `StripOuterMarkdownFence` and `CleanJSONResponse` return strings only; failures beyond the output shape cannot be detected by callers.
- `UnmarshalJSONResponse` is the only function that surfaces errors, wrapping them explicitly via `%w`.

---

## Chunk Reduction (`chunking.go`)

### Responsibility
Implements a recursive chunk-reduction algorithm that processes collections of code/architecture items through an LLM-based synthesis pipeline and produces a single consolidated output. Items are grouped into batches bounded by `maxChunkChars` (3× the LLM context window), then each batch is synthesized independently, and intermediate results are concatenated and reduced again until one item remains—a tree-like reduction pattern.

### Algorithmic Flow
1. **Empty input** — Returns `("", nil)` immediately.
2. **Single-item truncation** — When exactly one item exceeds the context window, it is mutated in-place to `<items[0][:maxChunkChars]>\n...[truncated]>`. No error is returned; processing continues.
3. **Batching phase** — Items are greedily grouped into batches where each batch's combined length stays within `maxChunkChars`. Overflow starts a new batch.
4. **Fallback truncation** — If every item exceeds limits individually, each item is proportionally truncated and forced into a single batch. This prevents deadlock on unbatchable inputs.
5. **Single-batch terminal** — When exactly one batch exists after grouping, the entire set is sent directly to `c.CallLLM` for synthesis; the result is stripped of markdown fences before return.
6. **Multi-batch recursion** — Each batch is recursively reduced via `reduceInChunks`. Intermediate results are concatenated into a single list, which then triggers another recursive call until reaching the base case where all items fit in one context window.

### Error Handling
- Recursive-call errors and LLM synthesis errors propagate directly. Synthesis errors are wrapped with `"LLM error during synthesis: %w"`.
- Truncation is a silent side effect, not an error path—no warning logged unless it's the only item (in which case `logEvent` is invoked).

---

## Hierarchical Synthesis (`synthesize.go`)

### Responsibility
Recursively walks a directory tree (`DirNode`), producing synthesized textual summaries for each node. Each directory's summary is built from child summaries plus per-file facts extracted by LLM calls. Unchanged files reuse cached SHA256 hashes and previously extracted facts; changed or new files trigger fresh extraction via configurable `ExtractionSteps`.

### Algorithmic Flow
1. **Context check** — Aborts early if context is cancelled.
2. **Cache hit short-circuit** — If node is not affected (`affectedDirs` map) and a cached summary exists, returns immediately without LLM calls.
3. **Recursive child synthesis** — Children are sorted; each is synthesized in order. Non-empty summaries accumulate into `childSummaries`.
4. **File fact extraction loop** — For each file: compute SHA256 via `computeSHA256`; decide cache hit vs. LLM-driven extraction; accumulate facts. Read errors on individual files are swallowed (`continue`).
5. **Component assembly** — File components followed by child subsystem summaries form an ordered slice.
6. **Chunked reduction** — All assembled components feed `reduceInChunks` to produce the directory's final summary.
7. **Cache update & persist** — The result is stored in both `MetadataCache.Files[f]` (with new hash and facts) and `MetadataCache.Modules[node.Path]`. Module docs are written under `<docsDir>/modules/<safe-filename>.md` via `tools.WriteFileSafately`; write errors are wrapped.

### Error Propagation
- LLM call failure during extraction is fatal for the node: wrapped as `"LLM error extracting %s for %s: %w"` and stops all further processing. No partial results returned.
- SHA256 computation or file read failures on individual files are swallowed; other files still contribute components. The result may be incomplete but not an error.

---

## Directory Tree (`tree.go`)

### Responsibility
Defines the in-memory `DirNode` hierarchy and implements affected-directory propagation logic that distinguishes between directories containing changed files and unaffected ones, enabling incremental synthesis without re-analyzing unchanged subtrees.

### Structures
- **`FileChange`** — `Path string`, `Status string` (`"Added" | "Modified" | "Deleted"`). Input primitive for tracking changes across the repository.
- **`DirNode`** — `Path string`, `Files []string`, `Children map[string]*DirNode`. Each node holds its own file list and a map of child subdirectories keyed by name.

### Affected-Directory Propagation
1. Individual directories are flagged as affected if any direct file appears in the change set, or if no cached module entry exists for the path, or if `modules/<safe-filename>.md` does not exist at the expected location under `docsDir/modules`.
2. Once individual nodes are flagged, ancestors are recursively marked affected by walking upward—ensuring parent summaries reflect changes even when they themselves contain no direct file modifications.

### Error Handling
- `SafeResolve` errors in `determineAffected` are caught and ignored; the entire block is skipped silently.
- `os.Stat(absModulePath)` errors: only `os.IsNotExist(err)` triggers the affected flag; other Stat errors (permission denied, etc.) are treated as non-existence and silently ignored.

---

## Orchestrator (`orchestrator.go`)

### Responsibility
Implements the automatic documentation generation pipeline for software repositories. Coordinates code discovery, tree building, hierarchical synthesis, metadata cache operations, standard doc regeneration, AGENTS.md maintenance, and module cleanup. Emits typed `Event` objects (type + message) so callers can observe pipeline progress without parsing log output.

### Structures
- **`Event`** — `Type string`, `Message string`. Typed event used for cross-cutting logging.

### RunInit Flow
1. Load `.gitignore`, apply configured ignore lists, scan repository for source-code files.
2. Convert flat file list into a hierarchical `DirNode` tree rooted at the repo root via `buildTree`.
3. Walk the tree bottom-up; each node is summarized by LLM invocation. The root node's summary becomes `rootSum`.
4. Invoke LLM to write `architecture.md` and `quickstart.md`, passing `rootSum` as context. Errors are wrapped with `"failed to generate ...: %w"`.
5. Append or create an "AI Agent Guidelines" section in `AGENTS.md`; if file exists and already contains the marker, skip. Write errors are swallowed.
6. Persist cache via `saveMetadataCache` with a `"local"` commit marker.

### RunUpdate Flow
1. Re-discover code files using identical filtering logic.
2. Detect changes via SHA256 comparison against cached hashes in `MetadataCache.Files`. Differences classify into `"Added"`, `"Modified"`, or `"Deleted"`.
3. For every changed file, walk upward from its parent to the root directory, marking each ancestor as affected. Deleted files are removed from cache immediately; modules whose directories are no longer active are purged via `os.Remove` (errors ignored).
4. Rebuild tree and prune cache so it reflects only currently existing code.
5. Propagate affect markers through the tree via `propagateAffected`. If no directories are affected, short-circuit with a "no modifications" message; return without further LLM calls.
6. Synthesize only affected nodes via `synthesizeNode`, skipping unaffected subtrees entirely—reusing cached summaries for them.
7. Regenerate `architecture.md`/`quickstart.md` if they are missing or any top-level directory was affected; otherwise reuse existing files.
8. Persist updated cache with `"local"` commit marker.

### Cross-Cutting Rules
- **Gitignore stacking** — `.gitignore`, configured ignore lists, and the `docsDir` itself are all applied together when discovering code files.
- **Safe file resolution** — All paths resolved through `security.SafeResolve` before any filesystem operation.
- **Atomic writes** — Documentation written via `tools.WriteFileSafely`.
- **Module cleanup** — Modules whose directories no longer appear in the active set are removed from disk; removal errors are ignored (`_ = os.Remove`).

---

## Runner (`runner.go`)

### Responsibility
Acts as the orchestrator for code analysis or documentation pipelines. Manages lifecycle execution from initialization through completion, handling state transitions based on a provided mode string. Ensures repository integrity by verifying that the lockfile is excluded from version control before executing pipeline logic.

### Structures
- **`Runner`** — Internal field `cfg *config.Config`. No exported fields; construction and configuration are external to this file's scope.

### Methods
- **`NewRunner(cfg *config.Config) *Runner`** — Factory for creating a new runner instance.
- **`Run(ctx context.Context, repoRoot string, mode string, onEvent func(Event)) error`** — Main entry point. Acquires an exclusive lock on the repository; if acquisition fails, returns immediately with wrapped error. A deferred unlock releases the lock unconditionally regardless of how the function exits.

### Mode Dispatch
- `"init"` → calls `client.RunInit(ctx, repoRoot, r.cfg, onEvent)`
- `"update"` → calls `client.RunUpdate(ctx, repoRoot, r.cfg, onEvent)`
- Any other value → returns `fmt.Errorf("unsupported mode: %s", mode)` as a fatal error.

### Error Handling Summary
| Step | Behavior on Failure |
|---|---|
| `EnsureGitignoreHasLockfile` | Swallowed; logged as warning via `onEvent`. Execution continues. |
| `AcquireLock` | Fatal, wrapped: `"failed to acquire repository lock: %w"`. Caller sees original error wrapped. |
| `client.RunInit` / `RunUpdate` | Propagated raw without wrapping. Deferred lock cleanup still runs. |
| Unsupported mode | Fatal, wrapped: `"unsupported mode: %s"`. |

---

## Utilities (`utils.go`)

### Responsibility
Maps internal identifiers (module paths) onto safe, human-readable filenames for markdown output. Operates at the boundary between the engine and the documentation generation system.

### `ToSafeMarkdownFilename(modulePath string) string`
1. All filesystem separator characters are replaced with underscores (`_`). This normalizes platform-specific path separators into a single ASCII character.
2. If the resulting string is empty or equals `"."`, it is replaced with the literal string `"root"`.
3. A `.md` suffix is appended, signaling that the output is a markdown document file.

The mapping is deterministic and pure—same input always yields the same filename. No error return for invalid inputs. All transformations are best-effort with silent normalization.