# Module: `internal/engine` — Automated Codebase Documentation Pipeline

## Responsibility & Data Flow

The engine module orchestrates a single-purpose pipeline: **extract architectural facts from source files, synthesize them into Markdown documentation via LLM calls, and persist results incrementally across runs.** It is invoked once per repository with either an initialization mode (full rebuild) or an update mode (incremental refresh). The pipeline's state survives between invocations through a JSON-backed metadata cache stored at `{docsDir}/.metadata.json`.

**Data flow summary:**

```
Runner.Run() ──► LLMClient.CallLLM / StreamLLM ──► json_parser.go
     │                                              ▲
     ├──► tree.go (DirNode construction + change detection)   │
     ├──► synthesize.go (per-file extraction → chunking → synthesis)
     ├──► cache.go (persistent hash/facts/module summary store)
     └──► orchestrator.go (pipeline coordination, global doc generation)
```

All four public entry points funnel through `Runner.Run()`. The runner acquires an exclusive lockfile, instantiates a fresh `LLMClient` from config, branches on the mode string (`init` | `update`), and emits typed events via the caller-supplied `onEvent` callback. All domain-specific work lives downstream of the runner; it never contains synthesis logic itself.

---

## 1. LLM Interaction Layer (`client.go`)

The engine talks to Ollama-style endpoints through a single struct, `*LLMClient`. It is constructed once per run via `NewLLMClient(modelID, baseURL, numCtx)` and reused for every call in that run's lifetime. The HTTP client inside it carries a fixed 10-minute timeout; there is no way to swap or inject a custom one after construction.

**Synchronous path — `CallLLM`:** sends a complete request, blocks until the full response arrives, returns parsed content or error. Non-OK status codes do not propagate the raw read/Do error: up to 1024 bytes are consumed and returned as `"ollama api error: status %d, response: %s"`.

**Streaming path — `StreamLLM`:** opens a streaming connection; each decoded content chunk is forwarded to an injected `onChunk(string)` callback. Empty lines are skipped. Malformed JSON on any individual line is silently ignored and the loop continues; that specific line's data never reaches the callback and no signal propagates up. Iteration stops when `chunk.Done == true` or when `io.EOF` arrives.

**Prompt assembly:** every call prepends a base system prompt (hardcoded in `GetBaseSystemPrompt`) that defines the agent persona and defensive output rules. `GetDefaultSystemPrompt(command)` extends this base with task-specific framing for `"module_synthesis"` and `"architecture"`; any other command returns only the base prompt.

**Internal request preparation — `prepareOllamaRequest`:** builds the HTTP body once per call: prepends a system message to user messages, sets context window size from client config (`NumCtx`), optionally forces JSON output format. Reused across both batch and streaming paths.

---

## 2. Persistent Metadata Cache (`cache.go`)

All state between runs lives in `MetadataCache`, serialized as `{docsDir}/.metadata.json`. The struct holds three maps: `Files` (keyed by file path → `FileCacheEntry{SHA256, Facts}`), `Modules` (keyed by directory path → synthesized summary string), and a scalar `LastDocumentedCommit` tracking the last commit SHA that was fully documented.

**Load semantics — `loadMetadataCache`:** if the metadata file does not exist, returns an empty-but-initialized cache (`Files: make(map[string]FileCacheEntry)`, `Modules: make(map[string]string)`). Any other read/parse/write failure is wrapped with `%w` and returned as a hard error.

**SHA-256 computation — `computeSHA256`:** reads the raw bytes of any file at its virtual path via `tools.ReadFileSafely`, returns the hex-encoded SHA-256 digest in memory. The hash itself is not persisted; callers store it into `FileCacheEntry.SHA256`, which then gets flushed to disk by `saveMetadataCache`.

**Save semantics — `saveMetadataCache`:** marshals the full cache back to `.metadata.json` and writes via `tools.WriteFileSafely`. Marshaling or write failures propagate raw, unwrapped. No panic anywhere in this file.

**Concurrency:** none. The maps are nil-initialized by default; reads reassign them via `make()` if nil before use. Writes happen without locking. Read/write race conditions exist but no protection mechanism is present.

---

## 3. Tree Construction & Change Detection (`tree.go`)

The engine builds an in-memory directory tree from flat file paths using `DirNode{Path, Files, Children}`. The root represents the current working directory; each path is split on `/` to walk down and create intermediate nodes as needed.

**Change detection — `determineAffected`:** walks the tree top-down and marks a directory as affected under three conditions: any file in the directory matches one of the filtered `FileChange{Path, Status}` records (`Added`, `Modified`, `Deleted`); the safe resolved absolute path for the directory's markdown equivalent does not exist on disk (used when a module was removed but metadata has not yet been cleaned up); or the directory has no entry in `cache.Modules`. For each matched condition the directory is added to `affectedDirs`; recursion continues into children.

**Bottom-up propagation — `propagateAffected`:** after the top-down walk completes, this function walks bottom-up so that any parent with an affected descendant becomes marked too. The result: every path whose subtree has at least one source of impact appears in `affectedDirs`, regardless of whether it was a direct change or inherited from a child.

---

## 4. Synthesis Engine (`synthesize.go`)

All synthesis logic lives unexported inside `engine` as `synthesizeNode`. It is the recursion anchor: every directory-level summary is produced by calling this function on each child first, then assembling their summaries plus per-file facts into a final parent summary.

**Per-file processing:** for each file in a node's `Files` slice, the engine reads bytes safely (skip silently if unreadable), computes SHA-256, checks the cache for a hash match to reuse stored facts without re-prompting. If no cache hit, the text is chunked with overlap and every extraction step runs across all chunks; each step's results are then consolidated into one fact per step via `reduceFileFacts`. The resulting `FileCacheEntry{SHA256, Facts}` is cached for future reuse.

**Component assembly:** builds a slice containing one entry per file (prefixed with filename) plus one entry per child summary (prefixed with `"Subsystem"`). If no components exist, the module cache is cleared and an empty string is returned. Otherwise `reduceInChunks` merges them into a single final summary.

**Persistence:** after synthesis completes for a directory, the final summary is written to `{docsDir}/modules/<safe-filename-of-path>.md`. Write failures are wrapped with `%w` and returned as error. Any read failure during per-file processing is swallowed; the file is skipped entirely without affecting the rest of the node's processing.

**Cache mutations:** `cache.Files[f] = FileCacheEntry{SHA256: fileHash, Facts: facts}` updates the per-file cache after extraction. `cache.Modules[node.Path]` stores the synthesized summary keyed by directory path; it is also consulted at entry if no descendants flag the directory as affected and a cached module exists — in which case the cached value is reused without re-prompting.

---

## 5. Chunking & Reduction (`chunking.go`)

All four functions here are unexported package-private: `reduceInChunks`, `reduceFileFacts`, `reduceItems`, `chunkTextWithOverlap`. None of them mutate global state or use concurrency primitives.

**Chunking with overlap — `chunkTextWithOverlap`:** splits a string into overlapping chunks (default 800-character overlap). If the text fits in one chunk, returns `[]string{text}`; otherwise iterates with overlap logic. No error return at all.

**Reduction pipeline:** `reduceItems` groups strings into batches whose total rune length stays within `maxChars`. Each batch is sent to an LLM and the response (markdown fences stripped) is returned as a single string. All returned strings are concatenated into one intermediate result, which is then fed back through `reduceItems` recursively until only one final string remains.

**LLM-specific reductions — `reduceInChunks` and `reduceFileFacts`:** both follow the same pattern but use different system prompts (`module_synthesis` for architecture synthesis; a base prompt with a fixed role instruction for fact consolidation). LLM errors are wrapped with descriptive prefixes: `"LLM error during synthesis: %w"` and `"LLM error during file fact consolidation: %w"`. Both return `("", nil)` for empty or single-item inputs without taking the error path.

**Infinite-loop guard:** if every item ends up in its own batch and there are more than one item, the function silently truncates each proportionally to `maxChars / len(items)` and forces them into a single batch — no error is returned. Callers receive whatever truncated result was produced with no way to know truncation occurred unless they inspect output length/content themselves.

---

## 6. JSON Response Parsing (`json_parser.go`)

This module solves one business rule: extract valid structured data from LLM-style API responses wrapped in markdown code fences. All three functions are pure — zero external I/O, no DB, no network.

**StripOuterMarkdownFence:** detects and removes surrounding ````json` or ```markdown` fences using a package-level compiled regex (`markdownFenceRe`). If no fence matches, returns the original trimmed input unchanged. No error returned; caller cannot distinguish "no fence" from "fence with empty body."

**CleanJSONResponse:** applies `StripOuterMarkdownFence`, then performs bracket-matching extraction: tracks brace depth while skipping content inside string literals (respecting escape sequences), and when brackets balance to zero returns the matched substring. If brackets never close, returns the full trimmed input unchanged — caller receives either valid JSON or garbage with no sentinel to tell which.

**UnmarshalJSONResponse:** the only function that surfaces errors. Calls `CleanJSONResponse` internally, then invokes `encoding/json.Unmarshal`. Failure is wrapped as `"failed to unmarshal JSON (cleaned: %q): %w"`, preserving the original error chain for `errors.Unwrap`. No panic anywhere in this file.

---

## 7. Orchestrator (`orchestrator.go`)

The orchestrator coordinates the full documentation pipeline and exposes three public methods on `*LLMClient`: `GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum, logEvent)`, `RunInit(ctx, repoRoot, cfg, onEvent)`, and `RunUpdate(ctx, repoRoot, cfg, onEvent)`. Two internal helpers — `setupPipeline` and `teardownPipeline` — are package-private.

**Disk I/O surface:**
- Writes: `architecture.md` and `quickstart.md` via `tools.WriteFileSafely` (wrapped with `%w`) during global doc generation; `{repoRoot}/docsDir/modules/` created via `os.MkdirAll` mode 0755 in init only.
- Reads: `.gitignore` loaded once per run (errors swallowed as log warning); metadata cache load (same swallow pattern).
- Append-only writes to `{repoRoot}/AGENTS.md` with `"AI Agent Guidelines"` header check — errors assigned to `_`, never returned or logged, preventing duplicate content on re-runs.

**Global doc decision:** `architecture.md`/`quickstart.md` are only refreshed when at least one module's root directory was affected OR either file is missing entirely; otherwise they remain intact without regeneration.

**Affected propagation:** in update runs, for each change the system walks upward from the file's directory to mark all parent directories as affected. This ensures that if a subdirectory has changes, all ancestor directories are flagged too, so parent summaries reflect child changes during re-synthesis.

**Idempotent AGENTS.md writes:** only appends when the header marker isn't already present. If the read itself fails, it immediately writes fresh guidelines without checking — errors assigned to `_`. No log event emitted for this path.

---

## 8. Runner (`runner.go`)

`Runner{cfg *config.Config}` is the top-level entry point that coordinates a full run from lock acquisition through completion. It owns the execution lifecycle: validates gitignore hygiene (best-effort), acquires an exclusive repository lock, instantiates the LLM client from config, branches on mode, and releases the lock via defer regardless of outcome.

**Lock semantics:** `security.AcquireLock(repoRoot)` is called before any work begins with a deferred unlock guaranteeing release. Acquisition failure wraps as `"failed to acquire repository lock: %w"` and returns immediately — short-circuits all downstream work (no LLM client creation, no pipeline execution). Lock acquisition failure is treated as fatal; there is no recovery attempt or fallback path.

**Git hygiene:** `security.EnsureGitignoreHasLockfile(repoRoot)` appends the lockfile entry to `.gitignore`. Failure is swallowed — logged as an event with `"Warning:"` prefix, then execution continues regardless of severity. No early return. The operation is not critical to correctness; a missing lockfile in `.gitignore` is acceptable.

**Mode dispatch:**
- `init` → runs the initialization pipeline (first-time documentation generation: full tree build, all directories synthesized bottom-up, standard docs generated from root summary).
- `update` → runs the update pipeline (change detection via SHA-256 comparison against cached map, affected dir marking, cache pruning for deleted files, selective re-synthesis on affected paths only).
- Any other mode → returns an error. Unknown modes are rejected outright with no wrapping.

**Event callback pattern:** a user-supplied `onEvent` function is threaded through every stage of execution. Each step emits typed `"status"` events with messages, allowing external consumers to observe progress without the runner needing to know what they are. No domain logic for documentation generation lives in this file — it only coordinates and forwards.

---

## 9. Utilities (`utils.go`)

`ToSafeMarkdownFilename(modulePath string)` maps internal code paths to readable Markdown filenames by replacing path separators with underscores. Pure string manipulation, no external I/O, no global state mutation, no concurrency primitives. Returns a single `string`; any failure mode results in an undefined value — no error return type exists for this function.

---

## Concurrency & Safety Summary

No file in the engine module uses `sync.Mutex`, channels, atomic operations, or equivalent concurrency primitives anywhere except within internal types not visible here. All state mutations occur on a single goroutine's call stack during recursion (`synthesizeNode`). The metadata cache is read/written without locking — race conditions exist but no protection mechanism is present.

**No panics:** every function returns `(value, error)` or equivalent; no `panic`, `runtime.Goexit()`, or unhandled recover blocks are present in any of the examined files.

**Error wrapping patterns:**
- Swallowed / logged (no return): lockfile gitignore check, `.gitignore` load, metadata cache load, AGENTS.md read failures, `os.Remove` on stale module files.
- Wrapped with `%w`: all LLM errors from `CallLLM`, file write failures during synthesis and global doc generation, lock acquisition failure, JSON unmarshal failure.
- Propagated directly (no wrapping): pipeline errors (`RunInit`, `RunUpdate`), context cancellation signals.