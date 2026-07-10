# Architecture — Internal Packages

## Module Responsibility & Data Flow

The `internal` directory implements four orthogonal subsystems for the code analysis tool: **configuration resolution** (`config`), **pipeline orchestration** (`engine`), **sandbox enforcement** (`security`), and **filesystem/Git operations** (`tools`). Each subsystem has a single responsibility; cross-subsystem communication occurs only through the `*config.Config` value passed to engine entry points, which is read-only within `internal/engine`.

---

## Package: config — Multi-Layer Configuration Resolution

### Responsibility

The `config` package implements a four-tier precedence pipeline for resolving all configurable values. Each layer overwrites prior values when present; missing layers are transparently skipped. Three properties are resolved independently (`ModelID`, `OllamaBaseURL`, `OllamaNumCtx`). The package also structures the analysis pipeline into named **extraction steps** (`ExtractionStep`), each carrying a distinct prompt template.

All non-structural state (resolved config, local variables) is ephemeral—scoped to function calls and returned as values. No package-level mutable state survives beyond initialization. Concurrency primitives are absent; the package is single-threaded in design.

### Data Flow

```
DefaultExtractionSteps (init), DefaultIgnores, DefaultIgnoredExtensions
    │
    ▼
LoadConfig(cwd) ──► *Config{ModelID, OllamaBaseURL, OllamaNumCtx, ExtractionSteps, ...}
    │         │
    │         ├── defaults (hard-coded constants)
    │         ├── os.Getenv for CODE_REDUCER_MODEL_ID, OLLAMA_BASE_URL, OLLAMA_NUM_CTX
    │         └── strconv.Atoi on modelIdFlag, numCtxFlag (err==nil && n>0 required)
    │
    ▼
*Config returned to caller; no package-level mutation persists
```

### Types

#### `ExtractionStep`

```go
type ExtractionStep struct {
    Name   string // step identifier (e.g., "api-signatures", "business-logic")
    Prompt string // prompt template applied to this phase during analysis
}
```

Instances are held in a package-level slice (`DefaultExtractionSteps`). The slice is immutable after declaration; callers receive copies or slices of it.

#### `Config`

```go
type Config struct {
    ModelID              string            // resolved model identifier (last-wins: default → YAML → env → flag)
    OllamaBaseURL        string            // resolved base URL (default → YAML → env; CLI flag omitted)
    OllamaNumCtx         int               // resolved context size, validated positive (default → YAML → env → flag)
    DocsDir              string            // path to the documents directory under analysis
    ExtractionSteps      []ExtractionStep  // ordered list of extraction phases
    Ignore               []string          // absolute paths excluded from traversal
    IgnoreExtensions     []string          // file extensions excluded from traversal
}
```

### Constants and Defaults

#### Environment Variable Keys

| Constant | Value | Purpose |
|---|---|---|
| `CodeReducerModelIdEnvKey` | `"CODE_REDUCER_MODEL_ID"` | Overrides `Config.ModelID` when set. |
| `OllamaBaseUrlEnvKey` | `"OLLAMA_BASE_URL"` | Overrides `Config.OllamaBaseURL` when set. |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | Overrides `Config.OllamaNumCtx`; value must parse as a positive integer. |

#### Default Values

| Constant | Value | Purpose |
|---|---|---|
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | Fallback base URL when no prior layer supplies one. |
| `OllamaDefaultModelID` | `"ornith:9b"` | Default model identifier. |
| `OllamaDefaultNumCtx` | `8192` | Default context size. |

#### Configuration File

| Constant | Value | Purpose |
|---|---|---|
| `ConfigFileName` | `.code-reducer.yaml` | Filename resolved relative to the current working directory by `getConfigPath`. |

### Package-Level Immutable Defaults

Three slices are declared at package init and never mutated:

```go
var DefaultIgnores           []string // system-supplied ignore paths
var DefaultIgnoredExtensions []string // system-supplied ignored extensions
var DefaultExtractionSteps   []ExtractionStep // ordered extraction phases
```

Callers may append to these slices externally, but the package itself never reassigns them. `SaveConfig` writes the current slice state back to disk; callers must mutate before save if they need custom defaults.

### Public API Surface

#### Path Resolution and Existence Check

##### `getConfigPath(cwd string) (string, error)`

Constructs the full path to `.code-reducer.yaml` by joining `cwd` with `ConfigFileName`. Returns a non-nil error only on I/O failure during construction; normal cases return `nil`.

##### `ConfigExists(cwd string) bool`

Returns whether the configuration file exists at the resolved path. Internally calls `os.Stat`; all errors (permission denied, I/O failures, etc.) are swallowed and treated as "does not exist". The function returns no error.

#### Configuration Loading

##### `LoadConfig(cwd string) (*Config, error)`

Reads and parses the YAML configuration file at the resolved path. Error handling:
- File-read failure (`os.ReadFile`) → returned to caller unchanged (pass-through).
- YAML parse failure → wrapped as `"failed to parse yaml config: %w"`.

Returns a non-nil `*Config` on success; the struct is initialized with zero values before parsing, so callers receive a populated object even when the file contains only valid empty mappings.

##### `ResolveConfig(repoRoot, modelIdFlag, numCtxFlag string) (*Config, error)`

Top-level entry point for full configuration resolution. Algorithm:
1. Calls `LoadConfig` to read the YAML file. If the error is a non-existent-file sentinel (`os.IsNotExist(err)`), it swallows and initializes an empty `Config{}`; all other errors propagate wrapped as `"failed to load configuration file:"`.
2. Layers defaults (hard-coded constants) over the loaded config.
3. Layers environment variable overrides using `os.Getenv` for `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, and `OLLAMA_NUM_CTX`. Non-empty values are used; empty strings fall through. Env read errors are swallowed.
4. Applies CLI flags (`modelIdFlag`, `numCtxFlag`). The flag value is parsed via `strconv.Atoi`; only `err == nil && n > 0` is accepted—invalid or non-positive values cause the prior layer's value to be retained silently.

Returns a fully resolved `*Config`. Errors are either pass-through (from `LoadConfig`) or wrapped with `"failed to load configuration file:"`. Notable: if YAML is malformed, the parse error wraps and propagates; otherwise resolution always succeeds for missing files.

#### Configuration Persistence

##### `SaveConfig(cwd string, cfg *Config) error`

Serializes the config struct back to disk using `os.WriteFile` with `0600` permissions. Error handling:
- Write success → returns `nil`.
- Parse failure (marshal YAML) → wrapped as `"failed to marshal yaml: %w"`.
- File-write failure → wrapped as `"failed to write config file: %w"`.

Writes directly (no temp-file + rename); a partial write can leave a truncated file on disk. Permission normalization is implicit—existing files with different permissions will fail the write.

---

## Package: engine — Code Documentation Pipeline Engine

### Responsibility

The `internal/engine` package implements a Map-Reduce pipeline that generates and maintains technical documentation for a Go codebase using Ollama-compatible LLM inference endpoints. The engine produces three artifacts per run: `architecture.md` (global overview), `quickstart.md` (onboarding guide), and per-module API summaries under `modules/`. Execution is gated by a repository-level lock to prevent concurrent runs against the same root, and state persistence across invocations is handled by an on-disk metadata cache.

### Data Flow

```
security.EnsureGitignoreHasLockfile(repoRoot) ──► security.AcquireLock(repoRoot) ──► defer Unlock()
    │                                                  │
    ▼                                                   ▼
*LLMClient from config.ModelID, BaseURL, NumCtx          RunInit(ctx, repoRoot, cfg, onEvent)
    │                                                          │
    ▼                                                          ▼
client.RunInit / client.RunUpdate → synthesizeNode (bottom-up tree reduction) → CallLLM per step/chunk
    │
    ▼
tools.WriteFileSafely for architecture.md, quickstart.md, modules/*.md
```

### Pipeline Orchestration

#### Runner Entry Point (`runner.go` — `Runner.Run`)

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

#### Orchestrator Pipelines (`orchestrator.go` — `RunInit`, `RunUpdate`)

##### Full Run (`RunInit`)

Executes an all-or-nothing regeneration regardless of cache state:

1. **Setup** — Load `.gitignore` via `tools.LoadGitignore(repoRoot)`; merge with user ignore list; load metadata cache from disk via `cache.loadMetadataCache()`. Failures logged as warnings, not returned.
2. **Map Phase** — Discover code files via `tools.DiscoverCodeFiles(...)`, compute SHA-256 per file via `computeSHA256(repoRoot, f)`, build directory tree via `buildTree(codeFiles)`. Hash computation errors silently skipped in loops; only stored if hash computed successfully.
3. **Mark All Affected** — Recursively flag every node as affected (init starts fresh).
4. **Reduce Phase** — Call `synthesizeNode` bottom-up on the tree to produce a root summary string and per-module summaries via LLM calls through `c.CallLLM`.
5. **Generate Standard Docs** — Produce `architecture.md` and `quickstart.md` from the root summary via two additional `CallLLM` invocations: one for architecture, one for quickstart. Writes via `tools.WriteFileSafely`; errors wrapped with context (e.g., `"failed to write quickstart.md: %w"`).
6. **Update AGENTS.md** — Read existing file via `tools.ReadFileSafely(repoRoot, "AGENTS.md")`. If missing, create with guidelines header. If present but does not contain "AI Agent Guidelines", append separator + new content block. Only writes when content actually differs. Errors: if read fails but write succeeds → `"failed to write AGENTS.md: %w"`; if both fail → wrapped errors chain.
7. **Teardown** — Persist metadata cache via `saveMetadataCache(repoRoot, docsDir, cache)`. Failure logged as warning, function always returns nil at end regardless of this failure.

##### Incremental Run (`RunUpdate`)

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

## Package: engine — LLM Client Abstraction & Response Parsing

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

### Response Parsing Layer (`json_parser.go`)

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

## Package: engine — Synthesis Engine & Chunking

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

## Package: engine — Filesystem State Management & Utilities

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

### Function: `ToSafeMarkdownFilename(modulePath string) string`

Pure string transformation with no business logic or complex algorithmic flow. Maps a module path to a safe filename for markdown documentation.

**Conversion pipeline:**
1. Replace every `/` character in input `modulePath` with `_`.
2. If result is empty or equals `"."`, substitute with `"root"`.
3. Append `.md` and return.

**Domain concept:** Module-to-document routing — given a module identifier, produce corresponding markdown file path. Assumes caller already owns consistent `modulePath` convention; this utility only adapts that path for filesystem-safe filenames.

---

## Package: security — Sandbox Enforcement & Process Locking

### Responsibility

The `security` package encapsulates three orthogonal concerns for code-reducer processes: **sandbox enforcement** (preventing path traversal beyond the repository boundary), **process-level mutual exclusion** via file-based locks, and **operational hygiene** (keeping lock state out of version control). No networking, database access, or external process communication occurs within this package.

All functions are pure-Go with no global mutable state. Every observable side effect is local to a single `*SimpleLock` receiver instance or the calling goroutine's stack. Errors follow a strict wrapping convention: security-critical violations and actionable sentinel messages return unwrapped plain strings; standard-library errors use `%w`; best-effort cleanup paths swallow non-fatal I/O failures silently.

**Data flow** proceeds in two independent tracks that share no state except through the repository root path string:

1. **Path resolution track:** `SafeResolve` → called by `AcquireLock` to canonicalize the lock file location before attempting acquisition.
2. **Lock lifecycle track:** `AcquireLock` → returns a `*SimpleLock` receiver → caller uses it for work → calls `Unlock()` for release → `EnsureGitignoreHasLockfile` runs during cleanup to append the lock filename to `.gitignore`.

### Types

#### `SimpleLock`

File-based process lock with internal mutex protection. The struct is constructed by `AcquireLock`; once acquired, `lockPath` and `file` are set exactly once under mutex scope, then only modified during `Unlock()`.

| Field | Type | Semantics |
|-------|------|-----------|
| `lockPath` | `string` | Absolute path to the `.code-reducer.lock` file. Set in constructor; zero-valued if acquisition failed or was never called. |
| `file` | `*os.File` | Opened lock file handle. Acquired with `O_CREATE\|O_EXCL`; set to `nil` inside `Unlock()`. |
| `mu` | `sync.Mutex` | Per-instance mutex protecting `closed` and the pointer-clear of `file` during unlock. |
| `closed` | `bool` | Transition flag: `false` while locked, flips to `true` once `Unlock()` completes its write operations (under mutex). |

### Public API Surface

#### Functions

##### `SafeResolve(repoRoot, inputPath string) (string, error)`

Computes the absolute path of an input relative to a repository root. The resolution is symlink-aware: ancestor components are evaluated through their physical targets via `filepath.EvalSymlinks` before being walked upward until one succeeds. The final result must lie strictly inside the resolved root—any escape beyond it returns an unwrapped string error indicating a security violation.

##### `AcquireLock(repoRoot string) (*SimpleLock, error)`

Resolves the lock file path through `SafeResolve`, then attempts atomic creation with `O_CREATE\|O_EXCL`. On success, writes the process PID to the newly created file and returns a populated `*SimpleLock`. If the file already exists (anewer process holds it), returns an unwrapped sentinel string describing that another process has acquired the lock.

##### `EnsureGitignoreHasLockfile(repoRoot string) error`

Reads `.gitignore`; if present, appends `# Code-Reducer Lockfile\n.code-reducer.lock\n` only when not already contained in the file. If `.gitignore` does not exist, creates it with append semantics (`O_APPEND\|O_CREATE\|O_WRONLY`). Errors from reading or writing are wrapped; non-existence of `.gitignore` is treated as expected and returned as `nil`.

#### Methods

##### `(l *SimpleLock).Unlock() error`

Idempotent release: acquires the internal mutex, closes the file handle (discarding any close-error), removes the lock file from disk via best-effort `os.Remove`, sets `closed = true`, then releases the mutex. A second call is safe and returns `nil`. Swallowed errors for cleanup operations are discarded; removal failures that occur after a successful close surface as wrapped errors only in the specific narrow case where both close-succeeded-and-remove-failed—but this path effectively swallows non-`IsNotExist` remove errors in practice.

### Business Rules & Domain Concepts

#### Repository Boundary Enforcement

All internal paths must resolve strictly within `repoRoot`. Any resolution that escapes beyond it returns a standalone string error: `"security violation: path traversal detected: %q"`, never wrapped. This is the one unwrapped class of non-sentinel errors in the package—security-critical, so callers detect it via exact match or `strings.Contains` rather than type assertion.

#### Symlink-Aware Path Resolution

Ancestors are evaluated through their physical targets before path reconstruction. This prevents traversal attacks that use symlinked directories to bypass the sandbox while preserving logical input structure for legitimate uses. Implementation: `filepath.EvalSymlinks(absRoot)` resolves the root once; subsequent ancestor walks use `os.Lstat` with a fallback to `EvalSymlinks` on each component until one exists, then rebuild upward from that point.

#### Process-Level File Locking

The lock file is `.code-reducer.lock`. Acquisition uses `O_EXCL` for atomic creation—no two processes can hold the lock simultaneously on the same filesystem. On success, the PID of the acquiring process is written as a single-line string (`fmt.Sprintf("%d\n", os.Getpid())`). The lockfile content therefore serves as both a record and a mechanism for stale detection: if another process exits without releasing its lock, a new acquisition attempt detects the existing file and reports an actionable error instructing manual cleanup.

#### Stale Lock Detection

Intentional behavior—the system does not attempt to detect or kill the holding process. The caller receives an unwrapped string describing that the lock is held by another process and must clean up manually. This keeps the package non-invasive; it never interacts with external processes.

#### Automated Gitignore Integration

The lockfile path is appended to `.gitignore` during unlock so it does not enter version control. Idempotent: if a line containing `code-reducer.lock` already exists, no duplicate entry is written. The lock filename itself is the only thing tracked; comment prefix provides human readability without affecting git behavior.

---

## Package: tools — Filesystem & Git Operations

### Responsibility

This module provides two complementary interfaces for repository-aware operations: safe file I/O with TOCTOU race mitigation, and Git process abstraction for executing commands within the working tree. Both modules operate on a single root path (`repoRoot`) without shared mutable state, and neither uses concurrency primitives — all functions are goroutine-safe by construction.

### `file_tools.go` — Safe File I/O & Discovery

#### Core Functions

| Function | Input | Output |
|---|---|---|
| [`ReadFileSafely`](file:///path/to/file_tools.go#L15-L64) | `repoRoot`, `virtualPath` `string` | `[]byte`, `error` |
| [`WriteFileSafely`](file:///path/to/file_tools.go#L70-L132) | `repoRoot`, `virtualPath` `string`; `content` `[]byte` | `error` |
| [`LoadGitignore`](file:///path/to/file_tools.go#L138-L164) | `repoRoot` `string` | `[]string`, `error` |
| [`ShouldIgnoreFile`](file:///path/to/file_tools.go#L170-L236) | `repoRoot`, `relPath` `string`; `gitIgnore` `*ignore.GitIgnore`; `ignoredExtensions` `[]string` | `bool` |
| [`DiscoverCodeFiles`](file:///path/to/file_tools.go#L242-L291) | `repoRoot` `string`; `ignores`, `ignoredExtensions` `[]string` | `[]string`, `error` |
| [`IsBinaryFile`](file:///path/to/file_tools.go#L297-L315) | `path` `string` | `bool` |

#### TOCTOU Race Mitigation Pattern

Both `ReadFileSafely` and `WriteFileSafely` implement a Time-Of-Check-Time-Of-Use safety pattern:

1. Resolve the virtual path to an absolute safe path via `SafeResolve`
2. Open the file descriptor first
3. Perform `lstat` on the resolved path (no symlink following)
4. Perform `fstat` on the open file descriptor (follows symlinks)
5. Verify that `lstat` and `fstat` reference the same inode — mismatch indicates a symlink race between opening and checking

Both operations reject any symlink detection as a security violation. On success, `ReadFileSafely` reads all content via buffered I/O; on success, `WriteFileSafely` truncates to zero first (safe only because symlinks were ruled out), then writes the provided content.

#### Error Handling Contract

| Function | Wrapped Errors (`%w`) | Swallowed | Sentinel Unwrapped |
|---|---|---|---|
| `ReadFileSafely` | Yes, for every I/O error | No | Symlink race check |
| `WriteFileSafely` | Yes, for every I/O error | No | Symlink race check |
| `LoadGitignore` | No | Missing file → `(nil, nil)` | None |
| `ShouldIgnoreFile` | No | `SafeResolve` err → returns `true` | None |
| `DiscoverCodeFiles` | No | Extensive — walk callback errors swallowed at each node | None |
| `IsBinaryFile` | No | All open/read errors → `false` | None |

#### File Discovery Algorithm (Multi-Layer Ignore Rules)

A file is considered "ignored" if it matches **any** of these conditions:

- Matches a compiled gitignore pattern list
- Has a path component starting with `.` or ending in `.egg-info`
- Has an extension matching the user-provided ignored extensions list (case-insensitive, supports both `.` prefix and suffix forms)
- Is detected as binary by reading up to 1024 bytes and scanning for null byte (`\x00`)

If any single check returns true, the file is excluded. This is a **union** of ignore conditions.

The primary business rule is to recursively walk the repository root and discover high-signal source code files while filtering out noise: build artifacts, dependency directories, output files, dot-prefixed hidden paths, `.egg-info` markers, binary files, and any user-defined ignore patterns (via gitignore compilation).

#### Binary Detection Rule

A file is considered binary if its first 1024 bytes contain a null byte (`\x00`). Files that cannot be opened are treated as non-binary (returns false), avoiding the loading of entire files into memory for classification.

### `git_tools.go` — Git Process Abstraction

#### Core Functions

| Function | Input | Output |
|---|---|---|
| [`RunGit`](file:///path/to/git_tools.go#L15-L64) | `repoRoot string`, variadic `args ...string` | `(string, error)` |
| [`VerifyGitRepo`](file:///path/to/git_tools.go#L70-L89) | `repoRoot string` | `error` |

#### Execution Model

Both functions spawn the `git` binary via `exec.Command("git", ...)` with `--no-pager`, restricting execution to the provided root directory. Both capture stdout and stderr into buffers synchronously. No standard library packages beyond `bytes`, `fmt`, `os/exec`, `strings` are imported.

#### Error Handling Contract

| Function | Failure Path | Behavior |
|---|---|---|
| `RunGit` | Exit code ≠ 0 | Wraps error: `git command failed: %v, stderr: %s`. Returns `(string, error)` with stdout and wrapped error. No panic