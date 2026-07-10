# Module: internal/engine — Code Documentation Pipeline Engine

## Overview

The `internal/engine` package implements a Map-Reduce pipeline that generates and maintains technical documentation for a Go codebase using Ollama-compatible LLM inference endpoints. The engine produces three artifacts per run: `architecture.md` (global overview), `quickstart.md` (onboarding guide), and per-module API summaries under `modules/`. Execution is gated by a repository-level lock to prevent concurrent runs against the same root, and state persistence across invocations is handled by an on-disk metadata cache.

**Data flow:** Repository isolation (`security.EnsureGitignoreHasLockfile`) → lock acquisition (`security.AcquireLock`) → LLM client construction from config → pipeline dispatch (`RunInit` or `RunUpdate`) → recursive tree synthesis with bottom-up reduction → standard docs generation → final state persistence. Errors propagate to the caller unless explicitly swallowed and logged via the `Event{Type: "status", Message: ...}` channel.

---

## Pipeline Orchestration

### Runner Entry Point (`runner.go` — `Runner.Run`)

The public entry point for all pipeline invocations. Responsibilities: repository isolation, concurrency guard acquisition, LLM client construction from config, and mode-gated dispatch to the appropriate pipeline method.

```
Run(ctx, repoRoot, mode string, onEvent func(Event)) error
  ├─ ensure .gitignore lockfile entry (best-effort; failure logged as warning)
  ├─ acquire repository-level lock via security.AcquireLock(repoRoot)
  │   └─ defer lock.Unlock() — released on any return path including errors
  ├─ build *LLMClient from r.cfg.ModelID, BaseURL, NumCtx
  ├─ switch mode:
  │   case "init":      client.RunInit(ctx, repoRoot, r.cfg, onEvent)
  │   case "update":    client.RunUpdate(ctx, repoRoot, r.cfg, onEvent)
  │   default:          return fmt.Errorf("unsupported mode: %s", mode)
```

The `cfg *config.Config` field is read-only within this file; all mutations to the runner's configuration originate before construction. The lock returned by `security.AcquireLock(repoRoot)` is a local variable with deferred cleanup—its internal type and mutual exclusion semantics are not observable from this source alone.

### Orchestrator Pipelines (`orchestrator.go` — `RunInit`, `RunUpdate`)

#### Full Run (`RunInit`)

Executes an all-or-nothing regeneration regardless of cache state:

1. **Setup** — Load `.gitignore` via `tools.LoadGitignore(repoRoot)`; merge with user ignore list; load metadata cache from disk via `cache.loadMetadataCache()`. Failures logged as warnings, not returned.
2. **Map Phase** — Discover code files via `tools.DiscoverCodeFiles(...)`, compute SHA-256 per file via `computeSHA256(repoRoot, f)`, build directory tree via `buildTree(codeFiles)`. Hash computation errors silently skipped in loops; only stored if hash computed successfully.
3. **Mark All Affected** — Recursively flag every node as affected (init starts fresh).
4. **Reduce Phase** — Call `synthesizeNode` bottom-up on the tree to produce a root summary string and per-module summaries via LLM calls through `c.CallLLM`.
5. **Generate Standard Docs** — Produce `architecture.md` and `quickstart.md` from the root summary via two additional `CallLLM` invocations: one for architecture, one for quickstart. Writes via `tools.WriteFileSafely`; errors wrapped with context (e.g., `"failed to write quickstart.md: %w"`).
6. **Update AGENTS.md** — Read existing file via `tools.ReadFileSafely(repoRoot, "AGENTS.md")`. If missing, create with guidelines header. If present but does not contain "AI Agent Guidelines", append separator + new content block. Only writes when content actually differs. Errors: if read fails but write succeeds → `"failed to write AGENTS.md: %w"`; if both fail → wrapped errors chain.
7. **Teardown** — Persist metadata cache via `saveMetadataCache(repoRoot, docsDir, cache)`. Failure logged as warning, function always returns nil at end regardless of this failure.

#### Incremental Run (`RunUpdate`)

Executes only re-generations for directories whose contents actually changed:

1. **Setup** — Load pipeline state from metadata cache (same path as init).
2. **Map Phase** — Discover code files, hash each via `computeSHA256`, build tree.
3. **Change Detection** — Compare current file hashes against cached hashes in `cache.Files`:
   - New file in codebase but absent from cache → `FileChange{Status: "Added"}`
   - Existing file with different hash → `FileChange{Status: "Modified"}`
   - Cached file no longer in codebase → `FileChange{Status: "Deleted"}` (also removed from cache)
4. **Prune Stale Modules** — Remove any module summaries whose directory is no longer active, delete their markdown files on disk via `os.Remove(absModuleFile)` (error assigned to `_`, never logged).
5. **Propagate Affected** — Run tree-aware propagation (`propagateAffected`/`determineAffected`) to mark directories affected by changes. Errors from these calls are discarded without capture.
6. **Early Exit** — If zero directories are affected → pipeline ends with "up to date" status, no further synthesis occurs.
7. **Conditional Reduce** — If root-level change detected or global docs don't exist on disk (`os.Stat(absArch)`/`os.Stat(absQs)`), run full synthesis; otherwise skip and reuse existing `architecture.md`/`quickstart.md`.
8. **Teardown** — Save updated cache via `saveMetadataCache(repoRoot, docsDir, cache)`.

### Directory Tree Construction & Affected Propagation (`tree.go`)

#### Type Definitions

| Type | Fields | Purpose |
|------|--------|---------|
| `FileChange` | `Path string`, `Status string` | Models a file operation with path and status (`Added`, `Modified`, `Deleted`). Consumed by `determineAffected`. |
| `DirNode` | `Path string`, `Files []string`, `Children map[string]*DirNode` | Hierarchical tree node holding files at that level and child directories in its subtree. Produced by `buildTree`. |

#### Functions (all unexported)

- **`buildTree(codeFiles)`** — Converts a flat list of file paths into nested `*DirNode` structure, preserving directory nesting by splitting on `/`. Appends to `DirNode.Files`; populates `DirNode.Children` via map assignment.
- **`propagateAffected(tree, changeset)`** — Traverses the tree recursively: if any direct child of the current node is in the changeset, mark it affected. Accumulates affected status upward through ancestors. Returns mutated `affectedDirs`.
- **`determineAffected(tree, changeset)`** — Computes module path for each directory via `ToSafeMarkdownFilename`, checks existence on disk via `os.Stat(absModulePath)`, reads metadata cache (`cache.Modules[n.Path] == ""`), marks node affected if any check fails. Only `os.IsNotExist` is acted on; all other errors are silently dropped.

---

## LLM Client Abstraction (`client.go`)

### Type: `LLMClient`

```go
type LLMMClient struct {
    ModelID   string
    BaseURL   string
    NumCtx    int
    HTTPClient *http.Client  // default transport, no custom config (no proxy/redirect/TLS)
}
```

Constructed via `NewLLMClient(modelID, baseURL, numCtx)` with a 10-minute HTTP timeout. Fields set once; never reassigned within any method.

### Methods on `*LLMClient`

#### `CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (string, error)`

Full lifecycle: prepends system prompt as first message with role `"system"` → serializes into Ollama `/api/chat` JSON payload with model ID and context window size → non-streaming HTTP POST → response body fully read on 200 OK or truncated at 1 KB via `io.LimitReader(resp.Body, 1024)` on non-OK status. Returns only assistant content string; returns empty string on failure.

**Error behavior:**
| Failure path | Error type | Wrapping strategy |
|---|---|---|
| JSON marshal fails in request prep | `fmt.Errorf("failed to marshal request: %w", err)` | Wrapped with `%w` |
| `http.NewRequestWithContext` fails | raw error from `NewRequestWithContext` | **Not wrapped** — passed through as-is |
| HTTP Do (network-level failure) | raw error from `c.HTTPClient.Do(req)` | **Not wrapped** |
| Body read fails on 200 OK path | raw error from `io.ReadAll` | **Not wrapped** |
| JSON unmarshal fails on response | `fmt.Errorf("failed to parse response: %w", err)` | Wrapped with `%w` |
| Non-OK status (body truncated at 1 KB) | `fmt.Errorf("ollama api error: status %d, response: %s", ...)` | New error constructed; body discarded if read fails |

#### `GetBaseSystemPrompt() string`

Returns the fixed base role definition and defensive guidelines inherited by every LLM call regardless of task type.

#### `GetDefaultSystemPrompt(command string) string`

Appends command-specific instructions based on the command string: two named variants exist (`module_synthesis`, `architecture`); any other value receives only the base layer.

---

## Response Parsing Layer (`json_parser.go`)

### Functions (all unexported except `StripOuterMarkdownFence`, `CleanJSONResponse`, `UnmarshalJSONResponse`)

Internal helper `extractBalancedJSON` is lowercase and excluded from public surface area.

#### Business Rules

1. Markdown code fences (` ``` `) surrounding JSON must be stripped before parsing — handled by `StripOuterMarkdownFence`.
2. Balanced bracket matching required — strings containing `{`, `[`, `}`, `]` literals (including escaped characters like `\n`) must not break extraction logic — handled by internal rune-level scanner with escape state tracking inside string literals.
3. When multiple balanced JSON candidates exist in a single response, each must be attempted individually until one succeeds, or the entire trimmed string is tried as a final fallback — handled by `CleanJSONResponse`/`UnmarshalJSONResponse`.

#### Functions

| Function | Behavior | Error surface | Swallowed? |
|---|---|---|---|
| `StripOuterMarkdownFence(s string) string` | Strips surrounding markdown fences; falls back to trimmed input if none match | None — returns string only | N/A (no error return) |
| `CleanJSONResponse(s string) string` | Iterates stripped content, calls `extractBalancedJSON`, returns first successful candidate or original trimmed input unchanged | None — returns string only | Yes — unbalanced JSON inside response is swallowed silently |
| `UnmarshalJSONResponse(s string, target interface{}) error` | Multi-layered attempt: (1) collect balanced candidates, try each into target; keep last error in `lastErr`; (2) if any succeeded, return nil; otherwise try entire trimmed string; (3) terminal errors — `"failed to unmarshal JSON candidates: %w"` or `"no valid JSON found in response"` | Explicit (`error`) | Partial parse failures collected and re-emitted via `%w` on first path; "no valid JSON" covers second path |

---

## Synthesis Engine (`synthesize.go`, `chunking.go`)

### Context-Aware Chunking (`chunking.go` — all unexported)

The system processes extracted code/fact items in batches constrained by LLM context limits (`NumCtx * 3`). Three distinct processing modes exist:

#### Module Synthesis (`reduceInChunks`)

Synthesizes architectural-level nodes into architecture descriptions using a `"module_synthesis"` system prompt. Each batch represents evidence contributing to understanding the module's design intent. Response passes through `StripOuterMarkdownFence`. Errors wrapped with `"LLM error during synthesis: %w"`.

#### File Fact Consolidation (`reduceFileFacts`)

Consolidates extracted facts about a specific step within a file with deduplication and merging. System prompt built locally by concatenating `c.GetBaseSystemPrompt()` + fixed role string. Response also passes through `StripOuterMarkdownFence`. Errors wrapped with `"LLM error during file fact consolidation: %w"`.

#### Recursive Convergence (`reduceItems`)

Core business rule: reduce items in batches → collect intermediate results → recursively apply reduction until everything converges into a single output string. Multi-pass approach ensures information is progressively synthesized rather than lost across multiple LLM calls. Termination guaranteed when all data fits within one batch (short-circuits at top with `len(batches) == 1`). Recursion terminates because:
1. Single-batch case short-circuits at top (`len(batches) == 1`)
2. Recursive step processes each batch through itself again with same `maxChars`

**Graceful Truncation Policy:** When content exceeds the context window, single oversized items get truncated with `\n...[truncated]` marker appended; when multiple items exceed limits collectively, each gets capped at `maxChars / len(items)` characters before being processed as one batch. Infinite-loop guard: if batch size equals item count for all items (can't merge), falls back to per-item truncation using `allowedPerItem = maxChars / len(items)`. Still silent — no error returned.

#### Overlapping Chunk Support (`chunkTextWithOverlap`)

Utility for splitting raw text into overlapping chunks to maintain context continuity between adjacent segments—useful when feeding source material directly into LLMs without pre-processing through the chunking logic. No I/O. Silent swallow: `maxRunes <= 0` → returns full text as single chunk; `overlapRunes >= maxRunes` → clamps to half without warning; final partial chunk reaching end of runes → breaks loop with no error or truncation marker (unlike `reduceItems`).

### Hierarchical Module Synthesis (`synthesize.go` — `synthesizeNode`, all unexported)

Performs recursive synthesis of directory structures into hierarchical summaries—transforming raw source code and subdirectory metadata into consolidated documentation at each level of a module tree.

**Algorithmic Flow:**
1. **Context Check → Cache Reuse Gate**: If current node not in `affectedDirs` AND cached module summary exists, return immediately without processing. Short-circuits unaffected directories.
2. **Recursive Child Synthesis (Bottom-Up)**: Sort children alphabetically, recursively call `synthesizeNode` on each child directory; results collected into map keyed by child name.
3. **File Processing Loop**: For each file in current node: hash-first cache lookup (if precalculated hash exists and matches cached SHA256 → reuse cached facts without reading disk); read + hash fallback otherwise (read via `tools.ReadFileSafely`, compute SHA256, update cache entry if already existed with that hash); extraction pipeline on cache miss (chunk content using dynamic limit derived from `c.NumCtx × 4 characters × 0.75` with minimum of 512 tokens and max overlap cap; for each chunk iterate over every step in `cfg.ExtractionSteps`, sending to LLM with system prompt built from base + step-specific prompt; consolidate all chunks for that step via `reduceFileFacts`; append consolidated result under step's name heading).
4. **Component Assembly**: After processing files, append any non-empty child summaries to components list. If no components exist at all (no files AND no children), clear module cache and return empty string.
5. **Final Synthesis & Persistence**: Pass assembled components to `reduceInChunks` for directory path; store result in both memory cache (`cache.Modules[node.Path]`) and on disk at `<repoRoot>/<docsDir>/modules/<safe-filename>` via `tools.WriteFileSafely`; return final summary string.

**Domain concepts:** Hierarchical module synthesis (bottom-up: files contribute facts, subdirectories contribute their own summaries, parent combines both); context-aware truncation (file content limits scale with `c.NumCtx`, reserving 75% of tokens for file data and 25% for prompts/output); two-level caching (files cache individual facts keyed by SHA256, modules cache full synthesized summaries keyed by path; reuse only when node not affected); multi-step extraction pipeline (each file passes through configurable sequence of LLM steps, each producing independent fact set consolidated before combining with other files' results).

**Error handling in `synthesizeNode`:**
| Source | Pattern | Propagation |
|---|---|---|
| Context cancel | Direct return at top and inside per-file loop (`if err := ctx.Err(); err != nil { return "", err }`) | Upward, no wrapping |
| LLM call failure | Wrapped: `"LLM error extracting %s for %s: %w"` with step name + file path | Upward |
| File read failure | Logged via `logEvent("status", ...)`, skipped silently; no hash/facts/anything added to components | None (swallowed) |
| Module write failure | Wrapped: `"failed to write module documentation for %s: %w"` with dir path | Upward |
| Recursive child error | Propagated as-is (`return "", err`) | Upward |
| Empty components | Returns `("", nil)`, clears `cache.Modules[node.Path] = ""` | None (clean return) |

---

## Filesystem State Management (`cache.go`)

### Type: `MetadataCache`

```go
type MetadataCache struct {
    Files   map[string]FileCacheEntry  // virtual path → cached SHA256 + facts
    Modules map[string]string          // module identifier → summary string
}
```

Both maps initialized with `make()` when nil; populated during load/save lifecycle; serialized to disk. No in-memory locking applied within this file.

### Functions (all unexported except `IsInitialized`)

#### Core Concept: Filesystem-Based State Cache

Persists an in-memory cache to disk so expensive computations can be avoided across runs. Stored as `docsDir/.metadata.json`, holds two pieces of state:
- **File-level metadata** (`Files map`): maps virtual paths (filenames) to cached SHA256 hashes plus associated "facts" strings.
- **Module-level metadata** (`Modules map`): maps module identifiers to string values.

#### Functions

| Function | Direction | Path constructed | Notes |
|---|---|---|---|
| `loadMetadataCache()` | Read | `docsDir/.metadata.json` | Attempts to read JSON; if absent, returns freshly initialized empty cache (not error); if present but unparseable, errors. Null maps normalized to empty on load. |
| `IsInitialized(repoRoot, docsDir string) bool` | Read (existence probe) | `docsDir/.metadata.json` | Probes for existence of `.metadata.json`. Successful read → true; any non-nil error → false. N/A for missing file (returns false). |
| `saveMetadataCache()` | Write | `docsDir/.metadata.json` | Serializes struct back to JSON with indentation, writes atomically via `tools.WriteFileSafely`. Returns raw errors from marshal/write unchanged. |
| `computeSHA256(virtualPath)` | Read (virtual path) | caller-supplied virtual path | Reads file by virtual path, runs SHA-256 on content, returns hex-encoded digest. No disk I/O within this file; pure in-memory (`crypto/sha256`). |

**Error handling patterns:**
| Function | On missing cache file | On generic read/write error | On JSON parse error |
|---|---|---|---|
| `loadMetadataCache` | Returns empty initialized `*MetadataCache`, nil error | Wraps with `"failed to read metadata cache: %w"` | Wraps with `"failed to unmarshal metadata cache: %w"` |
| `IsInitialized` | N/A (returns false) | N/A — any non-nil error → false | — |
| `saveMetadataCache` | — | Returns raw error from `tools.WriteFileSafely` | Returns raw error from `json.MarshalIndent` |
| `computeSHA256` | — | Returns raw error from `tools.ReadFileSafely` | N/A (no parsing) |

---

## Utility Functions (`utils.go`)

### Function: `ToSafeMarkdownFilename(modulePath string) string`

Pure string transformation with no business logic or complex algorithmic flow. Maps a module path to a safe filename for markdown documentation.

**Conversion pipeline:**
1. Replace every `/` character in input `modulePath` with `_`.
2. If result is empty or equals `"."`, substitute with `"root"`.
3. Append `.md` and return.

**Domain concept:** Module-to-document routing — given a module identifier, produce corresponding markdown file path. Assumes caller already owns consistent `modulePath` convention; this utility only adapts that path for filesystem-safe filenames.

---

## Concurrency & State Summary

### Mutable State Across Files

| Variable | File | Mutated By | Description |
|---|---|---|---|
| `MetadataCache.Files` | `cache.go` | load/save lifecycle | Map[string]FileCacheEntry — populated during load/save; serialized to disk |
| `MetadataCache.Modules` | `cache.go` | load/save lifecycle | Map[string]string — same pattern as Files |
| `DirNode.Files` | `tree.go` | buildTree only | Appended in tree construction, read-only otherwise |
| `DirNode.Children` | `tree.go` | buildTree only | Populated via map assignment during tree construction |
| `affectedDirs` (map[string]bool) | `orchestrator.go`, `tree.go` | Recursive closures: markAllAffected, collectDirs, determineAffected, propagateAffected | Populated by recursive traversal of tree nodes; passed to synthesizeNode. Local scope only in each invocation. |
| `cache.Files` / `cache.Modules` (local) | `orchestrator.go`, `synthesize.go` | synthesizeNode → updateCacheFromHashes, buildModuleMap, etc. | Populated in RunInit; modified during synthesis in RunUpdate. Local to function invocations; no shared mutable state across goroutines visible here. |
| `cfg *config.Config` (field on Runner) | `runner.go` | Set once in NewRunner; not mutated within this file | Read through methods delegated to other packages only. External mutation possible before construction, but no writes occur here. |

### Concurrency Mechanisms: None Detected

No `sync.Mutex`, channels (`chan`), atomic operations, or other synchronization primitives appear in any of these files. The recursion in `synthesizeNode` is itself a concurrency primitive (caller may invoke from many goroutines), so map writes to `cache.Files` and `cache.Modules` are unprotected against concurrent access if called with multiple goroutines—but no such protection exists within this codebase's visible scope.

### External Communication Summary

| File | I/O Category | Targets |
|---|---|---|
| `client.go` | Network (HTTP POST) | Ollama-compatible endpoint at `{BaseURL}/api/chat`, 10-minute timeout, non-streaming |
| `json_parser.go` | None | Pure in-memory string/byte manipulation; only imports from standard library (`encoding/json`, `fmt`, `regexp`, `strings`) |
| `cache.go` | Disk (read/write) | `docsDir/.metadata.json` via `tools.ReadFileSafely` / `tools.WriteFileSafely` |
| `orchestrator.go` | Network + Disk | LLM service calls, local filesystem under `repoRoot` (md files, cache, dir tree), `.gitignore` read/write |
| `runner.go` | Disk write (lockfile) | `.gitignore` modification for lockfile entry; lock file creation/update via `security.AcquireLock`; defer cleanup on any return path |
| `synthesize.go` | Network + Disk | LLM calls per chunk per extraction step, file reads via `tools.ReadFileSafely`, final writes via `tools.WriteFileSafely` with error wrapping |
| `tree.go` | Disk read (one op) | `os.Stat(absModulePath)` for module existence check; all other operations are pure tree construction/traversal |
| `utils.go` | None | Pure string transformation, no external I/O whatsoever |