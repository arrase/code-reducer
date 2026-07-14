# Code-Reducer Architecture Documentation

## Module Responsibility & Data Flow

Code-Reducer is a local-first AI agent that generates and maintains project wikis by analyzing source code against an LLM backend. The system orchestrates multi-step operations through a Cobra command tree, where each subcommand delegates to an unexported engine via a shared execution path. All configuration resolution, synthesis pipeline orchestration, file I/O, Git validation, security enforcement, and user interaction occur across four internal packages: `internal/config`, `internal/engine`, `internal/security`, and `internal/tools`. The entry point (`main.go`) is a thin shell that delegates to the command package and terminates with exit code 1 on error.

```
main.go ──► cmd.RootCmd.Execute()
    │
    ├── cmd/init.go          (engine.ModeInit)
    ├── cmd/update.go        (engine.ModeUpdate)
    └── cmd/setup.go         (interactive reconfiguration → config.SaveConfig)
    │
internal/config ─────────────► internal/engine ──► LLM client ──► synthesis artifacts
  │                                                                    │
  ▼                                                                    ▼
ResolveConfig                  Runner.Run(ctx, repoRoot, mode)        WriteFileSafely
  │                                                                    │
  ├── LoadConfig               security.AcquireLock                   │
  ├── SaveConfig                                                       │
  └── config.Config pointer                                           │
                                                                      ▼
internal/security ─────────► internal/tools ──► disk I/O / git subprocesses
```

---

## Entry Point (`main.go`)

### `main()` — Program Initiation

Entry point. Delegates to `cmd.RootCmd.Execute()`. On error, formats the returned value via `fmt.Fprintf` and writes it to `os.Stderr`; exits with code 1. Returns no observable error path (the only return is swallowed into `os.Exit(1)`). Nil returns from `Execute()` result in default Go exit behavior (code 0). No synchronization primitives; single goroutine execution model.

### Referenced External Types

- **`RootCmd`** — type from package `github.com/arrase/code-reducer/cmd`. Method: `Execute() error`. Invoked as the sole root command; all subcommand registration and lifecycle management occurs within this package.

---

## Configuration Subsystem (`internal/config`)

### Module Responsibility

Centralized configuration schema and persistence primitives for the synthesis pipeline. Defines `Config` (runtime parameters: LLM identity, Ollama base URL/context size, docs directory, prompt templates, extraction steps, ignore lists) and `ExtractionStep` (single fact-extraction phase name + system prompt). Exposes three I/O functions (`ConfigExists`, `LoadConfig`, `SaveConfig`) that manage `.code-reducer.yaml`; a fourth function (`ResolveConfig`) merges CLI flags, environment variables, YAML config, and hardcoded defaults into a single resolved `*Config`.

### Types

#### `ExtractionStep`

Single extraction phase executed during file-fact extraction stage. Carries a human-readable name and an LLM system prompt that guides extraction behavior.

| Field | Type | Tags |
|-------|------|------|
| `Name` | `string` | `yaml:"name"` |
| `Prompt` | `string` | `yaml:"prompt"` |

#### `Config`

Central configuration struct. All fields are optional and may be left at zero values; defaults kick in during resolution if the field is empty or unset.

| Field | Type | Tags | Notes |
|-------|------|------|-------|
| `ModelID` | `string` | `yaml:"model_id"` | LLM model identifier (e.g., `"ornith:9b"`). |
| `OllamaBaseURL` | `string` | `yaml:"ollama_base_url"` | Base URL for the Ollama inference server. |
| `OllamaNumCtx` | `int` | `yaml:"ollama_num_ctx"` | Context size parameter forwarded to Ollama. |
| `DocsDir` | `string` | `yaml:"docs_dir"` | Directory path where generated documentation is written. |
| `SystemPrompt` | `string` | `yaml:"system_prompt"` | System prompt injected into the model at synthesis time. |
| `ModuleSynthesisPrompt` | `string` | `yaml:"module_synthesis_prompt"` | Prompt used during module-level synthesis. |
| `ArchitecturePrompt` | `string` | `yaml:"architecture_prompt"` | Prompt used during architecture-level synthesis. |
| `FileFactConsolidationPrompt` | `string` | `yaml:"file_fact_consolidation_prompt"` | Prompt used to consolidate overlapping file facts before synthesis. |
| `ExtractionSteps` | `[]ExtractionStep` | `yaml:"extraction_steps"` | Ordered list of extraction phases; empty slice triggers fallback to `DefaultExtractionSteps`. |
| `Ignore` | `[]string` | `yaml:"ignore"` | File paths or patterns to skip during analysis. Duplicates are removed by the resolver. |

### Constants

#### Environment and CLI Keys

| Constant | Value | Type | Purpose |
|----------|-------|------|---------|
| `CodeReducerModelIDEnvKey` | `"CODE_REDUCER_MODEL_ID"` | `string` | Env var key for model ID override in resolver. |
| `OllamaBaseURLEnvKey` | `"OLLAMA_BASE_URL"` | `string` | Env var key for Ollama base URL override in resolver. |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | `string` | Env var key for context size override in resolver. |

#### Defaults

| Constant | Value | Type | Purpose |
|----------|-------|------|---------|
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | `string` | Default Ollama inference server address. |
| `OllamaDefaultModelID` | `"ornith:9b"` | `string` | Default LLM model identifier. |
| `OllamaDefaultNumCtx` | `8192` | `int` | Default context size. |
| `DefaultDocsDir` | `"wiki"` | `string` | Default documentation output directory. |
| `ConfigFileName` | `".code-reducer.yaml"` | `string` | Filename used by all three I/O functions and `ResolveConfig`. |

### Public API Surface — Types & Variables (from config.go)

#### `DefaultExtractionSteps`

Package-level variable of type `[]ExtractionStep`. Serves as the fallback extraction pipeline when the YAML config contains an empty or nil slice. Each step carries a name and prompt for one of the four fact-extraction phases: *API signatures*, *business logic*, *mutable state & concurrency*, *external I/O & error propagation*.

### Public API Surface — I/O Functions (from io.go)

#### `ConfigExists`

```go
func ConfigExists(cwd string) bool
```

Returns `true` if `.code-reducer.yaml` exists at the path formed by joining `cwd` with `ConfigFileName`. Underlying operation is a single `os.Stat` call; no error wrapping occurs. Returns only a boolean to caller.

#### `LoadConfig`

```go
func LoadConfig(cwd string) (*Config, error)
```

Reads and parses `.code-reducer.yaml` from `cwd`. On success returns the populated `*Config`. Failure modes:

| Condition | Return Value | Notes |
|-----------|--------------|-------|
| YAML parse error | `(nil, wrappedError)` | Error is wrapped with `"failed to parse yaml config: %w"`. |
| Raw OS read error (not-not-exist) | `(nil, rawError)` | `os.ReadFile` failure propagates unchanged. |

#### `SaveConfig`

```go
func SaveConfig(cwd string, cfg *Config) error
```

Serializes `cfg` to `.code-reducer.yaml` using an atomic write pattern: create temp file → write bytes → sync → close → chmod → rename over original path. File permission applied is `0600`. Failure points are wrapped individually with descriptive prefixes (`"failed to <action> config file"`). The final state is either the old file or a fully-written new file — never neither, because of the rename semantics.

### Public API Surface — Resolution (from resolve.go)

#### `ResolveConfig`

```go
func ResolveConfig(repoRoot string, modelIDFlag string, numCtxFlag string) (*Config, error)
```

Merges configuration across four precedence tiers:

1. Hardcoded defaults (`OllamaDefault*`, `DefaultDocsDir`, etc.).
2. Values parsed from `.code-reducer.yaml` at `repoRoot`.
3. Environment variables (`CodeReducerModelIDEnvKey`, `OllamaBaseURLEnvKey`, `OllamaNumCtxEnvKey`).
4. CLI flags (`modelIDFlag`, `numCtxFlag`).

Specific resolution rules:

- **`Ignore` list**: Deduplicated in-place; first occurrence wins via hash-set tracking.
- **`ExtractionSteps`**: Falls back to `DefaultExtractionSteps` when the YAML slice is empty or nil.
- **Numeric fields** (`OllamaNumCtx`): Parsed with `strconv.Atoi`; non-positive values are rejected and the prior tier's value is retained.
- **String prompts**: Empty YAML values fall back to the corresponding hardcoded default via `resolveString`.

### Internal Helpers (from resolve.go)

| Function | Parameters | Return Types | Description |
|----------|-----------|--------------|-------------|
| `deduplicate` | `a []string` | `[]string` | Removes duplicates from a string slice; first occurrence wins. |
| `resolveString` | `yamlVal string`, `defaultVal string` | `string` | Returns `yamlVal` if non-empty, otherwise returns `defaultVal`. Used for prompt field resolution. |

### Error Propagation Summary — config subsystem

- **config.go**: No runtime operations; no errors can be raised or swallowed here.
- **io.go**: All disk-read and disk-write functions return error tuples. YAML parse errors are explicitly wrapped with `"failed to parse yaml config: %w"`; raw OS-level read/write errors propagate unchanged through the returned tuple. Write-path errors each carry a unique prefix (`"failed to <action> temp config file"`).
- **resolve.go**: `LoadConfig` failure is only surfaced when `os.IsNotExist(err)` returns false — missing files are silently replaced with an empty `Config{}` and no error reaches the caller. All other branches (env lookups, flag parsing, deduplication) produce zero errors by design.

### External I/O Analysis — config subsystem

- **Disk**: All functions interact exclusively with `.code-reducer.yaml` under `cwd`. Writes use temp file + rename for atomicity; permissions are set to `0600`.
- **Environment / Process**: Three env vars (`CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX`) are read by `ResolveConfig`; none are written.
- **Network**: None. Constants like `OllamaDefaultBaseURL` are string literals; no HTTP client or network calls exist in this module.
- **Database/APIs**: None referenced.

---

## Engine Subsystem (`internal/engine`)

### Module Responsibility and Data Flow

Persistent, incremental documentation synthesis pipeline for software repositories. Produces three artifact classes — global architecture summaries, quickstart guides, and per-module API descriptions — from source code using an LLM backend, with change detection that limits regeneration to directories impacted by actual file modifications. State persists across invocations via a metadata cache stored as JSON under `docs/`; a global steps hash anchors cache validity to the current extraction pipeline configuration.

### Runner Entry Point (`runner.go`)

#### Mode

String-typed operation modes for the pipeline. Two exported constants:

| Constant | Value |
|----------|-------|
| `ModeInit`  | `"init"`   |
| `ModeUpdate` | `"update"` |

#### Runner

Struct holding a single pointer field, `cfg *config.Config`, assigned once in the constructor and never reassigned within this file. External mutability of the pointed-to struct is not observable from here alone.

```go
func NewRunner(cfg *config.Config) *Runner
func (r *Runner) Run(ctx context.Context, repoRoot string, mode Mode, onEvent func(Event)) error
```

`Run()` performs three operations before branching:

1. **Lockfile validation** — `security.EnsureGitignoreHasLockfile(repoRoot)` is invoked; any error is logged as a status event and execution proceeds regardless.
2. **Exclusive lock acquisition** — `security.AcquireLock(repoRoot)` returns an error that is wrapped with context (`failed to acquire repository lock: %w`) and returned immediately. A deferred unlock runs on every exit path, guaranteeing release even when the pipeline fails partway through.
3. **Client instantiation** — `newLLMClient(r.cfg.ModelID, r.cfg.OllamaBaseURL, r.cfg.OllamaNumCtx)` constructs an Ollama HTTP client; no network I/O occurs at this point.

After lock acquisition, execution branches on the mode value: invalid strings return `fmt.Errorf("unsupported mode: %s", mode)`. Otherwise, either `orch.RunInit` or `orch.RunUpdate` is invoked directly and its error (or nil) is returned to the caller.

### Orchestrator (`orchestrator.go`)

#### Types

```go
type EventType string   // "status" | "error" (unexported)

type Event struct {
    Type   EventType `json:"type"`
    Message string    `json:"message"`
}
```

Both constants and the type are unexported. The orchestrator itself is also internal; only its public method signatures appear in the package surface.

#### GenerateStandardDocs

```go
func (o *orchestrator) GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum string, cfg *config.Config, logEvent LogEventFunc) error
```

Invoked from both `RunInit` and `RunUpdate`. Issues two sequential LLM calls: one to generate `architecture.md` and another for `quickstart.md`, each wrapped as a global artifact. Errors propagate fully (wrapped with `%w`). If the top-level directory is marked affected or standard docs are missing, this function runs; otherwise it is skipped entirely on update.

### Pipeline Context State (`orchestrator.go`)

The orchestrator maintains mutable state across its lifecycle via pointers into shared structs:

| Field | Type | Mutability | Notes |
|-------|------|------------|-------|
| `cache` | `*MetadataCache` | **Mutable** | Reassigned in `setupPipeline`, `teardownPipeline`, `RunInit`, `RunUpdate`. Fields `.StepsHash`, `.Files`, `.Modules` are updated across calls. |
| `cfg` | `*config.Config` | Potentially mutable | Read only here (`SystemPrompt`, `ArchitecturePrompt`, `ExtractionSteps`). External code may mutate the pointed-to struct at runtime. |
| `client` | `llmCaller` interface | Read-only in this file | Initialized on construction; never reassigned within these methods. |

No synchronization primitives (mutex, atomic, channel) protect access to any of these fields. Concurrent callers sharing a single context instance would race on the cache maps. All operations are sequential and single-threaded per method invocation.

### Tree Construction and Affected Detection (`tree.go`)

#### Types

```go
type ChangeStatus string   // "Added" | "Modified" | "Deleted" (unexported)

type FileChange struct {
    Path  string
    Status ChangeStatus
}

type DirNode struct {
    Path     string
    Files    []string
    Children map[string]*DirNode
}
```

#### buildTree

Constructs a nested `DirNode` hierarchy from a slice of file paths. Leaf entries are files; intermediate path segments become child nodes. No I/O, no side effects.

### Affected Detection Logic

The orchestrator walks the tree to mark directories as "affected" via four conditions:

1. **Changed-file match** — if any `FileChange` in the map matches a file under this node's subtree.
2. **Module path non-existence on disk** — `security.SafeResolve(repoRoot, modulePath)` followed by `os.Stat(absModulePath)`. If the stat error is not `os.ErrNotExist`, the branch is skipped silently.
3. **Cache emptiness** — if the corresponding entry in `cache.Modules` is empty.
4. **Child propagation** — a second recursive pass marks any parent affected whenever *any* child has already been flagged, regardless of direct match.

### LLM Client (`client.go`)

#### Public Types

```go
type Message struct {
    Role    string `json:"role"`
    Content string `json:"content"`
}
```

`Message` is the only exported type in this file. All other declarations are unexported and therefore not part of the public API:

| Element | Type | Notes |
|---|------|-------|
| `llmCaller` | interface | Unexported; methods include `CallLLM(ctx, systemPrompt, messages, jsonFormat)` returning `(string, error)` and `NumCtx() int`. |
| `llmClient` | struct | Unexported. Fields: `modelID`, `baseURL`, `numCtx`, `httpClient *http.Client`. |
| `newLLMClient` | func | Factory for `*llmClient`; referenced but not defined in this file. |
| `(c *llmClient) NumCtx()` | method | Returns the configured context window size. |
| `ollamaRequest` / `ollamaOptions` / `ollamaResponse` | structs | Unexported request/response payloads for Ollama's `/api/chat`. |
| `(c *llmClient) prepareOllamaRequest()` | private method | Builds and serializes the chat payload. |

#### External I/O

All network interactions target `{baseURL}/api/chat` via HTTP POST with JSON body (`application/json`). The client is constructed once per instance; `httpClient` is reused across calls without locking, which is safe because timeout and base URL are fixed at construction time and never reassigned within this file.

**Response handling:** 200 OK responses have their bodies read fully via `io.ReadAll`; non-OK status codes trigger a bounded read (`io.LimitReader(resp.Body, maxErrorBodyBytes)`) whose error is discarded silently — the resulting string (containing the HTTP status and truncated body) is returned to the caller instead.

**Unresolved references:** `defaultHTTPTimeout` and `maxErrorBodyBytes` are referenced in this file but not defined here; they must be package-level constants or variables declared elsewhere, otherwise compilation fails.

### Chunking and Reduction Pipeline (`chunking.go`)

#### reduceWithLLM

```go
func reduceWithLLM(ctx context.Context, c llmCaller, items []string, sysPrompt string, buildPrompt func(batch []string) string, logMsg func(batch []string) string, errMsg string, logEvent LogEventFunc) (string, error)
```

Entry point for all LLM-mediated reductions. Calls `c.CallLLM(ctx, sysPrompt, messages, false)` with a system prompt prepended to user messages; errors are wrapped via `fmt.Errorf("%s: %w", errMsg, err)`. The original error is preserved through `%w` wrapping.

#### reduceInChunks

```go
func reduceInChunks(ctx context.Context, c llmCaller, nodePath string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)
```

Invoked from the synthesis step to consolidate a directory's file facts and child summaries. Calls `reduceWithLLM` with a prompt derived from `cfg.SystemPrompt` and the current extraction steps. Errors propagate through unchanged.

#### reduceFileFacts

```go
func reduceFileFacts(ctx context.Context, c llmCaller, filePath string, stepName string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)
```

Consolidates per-extraction-step outputs for a single file. Calls `reduceWithLLM` with a prompt built from `cfg.SystemPrompt` and the current extraction steps; errors propagate as-is.

#### reduceItems

```go
func reduceItems(ctx context.Context, items []string, maxChars int, reduceFn func(batch []string) (string, error)) (string, error)
```

Iterative map-reduce loop: accumulates items into batches that fit within `maxChars`, flushing when the next item would exceed the limit. If a single item exceeds the cap at entry, it is split with overlap rather than truncated — information preservation over compression. After each LLM pass, total output run count vs input is computed; if output ≥95% of input (integer division), recursion terminates and results are concatenated with `\n\n`. Context cancellation (`ctx.Err()`) returns early without partial results.

#### chunkTextWithOverlap

```go
func chunkTextWithOverlap(text string, maxRunes int, overlapRunes int) []string
```

Splits text into overlapping chunks of at most `maxRunes` runes with `overlapRunes` rune overlap between adjacent chunks (clamped if ≥ `maxRunes`). Returns the full text as a single chunk when `maxRunes <= 0` or `overlapRunes >= maxRunes`. No panics, no bounds errors.

### Markdown Fence Stripping (`markdown.go`)

#### Internal Implementation

```go
var markdownFenceRe regexp.Regexp
func stripOuterMarkdownFence(content string) string
```

Unexported; both the variable and function begin with lowercase letters. The regex is compiled once at package scope and never reassigned — effectively read-only after compilation.

**Algorithm:** trim whitespace → match triple-backtick fence (with optional `markdown`/`json` language tag) → return captured interior after trimming surrounding whitespace. No error return; if the pattern doesn't match, original input is returned unchanged.

### Constants (`constants.go`)

All nine named constants in this file are unexported:

| Constant | Purpose |
|----------|---------|
| `defaultHTTPTimeout` | 10-minute ceiling for remote API calls (set at package init) |
| `maxErrorBodyBytes` | 1 KB cap on error response payloads captured from failed requests |
| `defaultChunkOverlap` | 800-token overlap between consecutive text chunks during streaming/processing |
| `minNumCtxFloor` | 512 minimum threshold for context length calculations |
| `contextWindowAllocRatio` | 0.75 reserved proportion of available space for primary content |
| `maxCharsMultiplier` | 3× multiplier applied to base character limit |
| `metadataFileName`, `agentsFileName`, `defaultDirPerm` | Filesystem conventions for state persistence |

No mutable state, no external I/O, no error propagation. The single function-type alias (`LogEventFunc`) is declared but not used within this file.

### Synthesis Engine (`synthesize.go`)

Unexported types and functions only; all identifiers begin with lowercase letters.

#### External I/O Matrix

| Source | Path | Behavior on Failure |
|--------|------|---------------------|
| `tools.ReadFileSafely(p.repoRoot, f)` — first call | `<repoRoot>/<f>` | Logged as status warning; returns `"", nil`. Swallowed by caller. |
| `tools.ReadFileSafely` — second call (inside `if facts == ""`) | Same | Same swallow pattern. |
| `p.client.CallLLM(p.ctx, systemPrompt, []Message{...}, false)` | Network endpoint via `llmCaller` | Wrapped with `%w`, includes step name and file path in message. Propagated to caller as `"LLM error extracting %s for %s: %w"`. |
| `tools.WriteFileSafely(p.repoRoot, modulePath, []byte(finalSum))` | `<repoRoot>/<docsDir>/modules/<safe-filename-of-node-path>.md` | Wrapped with `%w`, includes node path. Propagated to caller as `"failed to write module documentation for %s: %w"`. |

#### Synthesis Algorithm (per `synthesizeNode`)

1. **Cache-first lookup** — check `cache.Files[f]` against precomputed SHA-256 hash; on match, reuse cached facts without re-reading the file.
2. **Recursive bottom-up traversal** — for each directory node: if unaffected and descendants are unaffected, return cached summary immediately. Sort child names alphabetically; recurse into children first; collect non-empty summaries into a map keyed by child name.
3. **Per-file extraction** — compute dynamic content limit from remaining context window capacity (allocates ~75% for file content). Split large files with `chunkTextWithOverlap`. Pass each chunk to LLM with step-specific prompt derived from `ExtractionSteps`. Strip markdown fences from responses via `stripOuterMarkdownFence`. Consolidate all outputs per extraction step using `reduceFileFacts`. Concatenate across steps into a single file fact string.
4. **Assembly** — combine extracted file facts (prefixed `"### File: basename"`) and child summaries (prefixed `"### Subsystem: name"`) into one `components` slice. If nothing exists, clear the cache entry and return empty.
5. **Final reduction and persistence** — pass all components to chunked reduction (`reduceInChunks`) producing a single consolidated summary for the directory; store in `cache.Modules[node.Path]`; write to disk under `<docsDir>/modules/<safe-filename-of-path>`.

### Utility Helpers (`utils.go`)

#### toSafeMarkdownFilename

```go
func toSafeMarkdownFilename(modulePath string) string
```

Pure string manipulation; no file operations. Returns `"README.md"` when the path is empty or `"."`; otherwise joins with `"README.md"`. Used by tree traversal and synthesis code for module-level doc paths.

#### makeLogEvent

```go
func makeLogEvent(onEvent func(Event)) LogEventFunc
```

Adapter factory returning a function that forwards to its callback only when non-nil (nil-safe pattern). No panics, no error returns.

### Error Propagation Summary — engine subsystem

| Category | Handled Pattern | Swallowed Pattern |
|----------|-----------------|-------------------|
| LLM calls (`CallLLM`) | Wrapped with `%w`, descriptive message includes step/file context; propagated to caller. | — |
| File writes (`WriteFileSafely`) | Wrapped with `%w`; propagated to caller. | — |
| Hash computation per file (`computeSHA256`) | Error logged as status warning; loop continues, file skipped from cache. | Swallowed in both `RunInit` and `RunUpdate`. |
| Cache load/save (`loadMetadataCache`, `saveMetadataCache`) | — | Logged as status event; function completes regardless of write success/failure. |
| `.gitignore` loading | — | Logged as warning; execution proceeds. |
| Module file cleanup (`os.Remove`) | — | Error ignored via `_ = os.Remove(...)`. No verification. |
| Directory creation (`os.MkdirAll`) | Wrapped with `%w`; propagated to caller. | — |
| Non-OK HTTP responses | Bounded body read; error string returned (I/O failure during bounded read silently dropped). | Partially swallowed. |

### External I/O Analysis — engine subsystem

- **Disk**: All file operations resolve user-supplied virtual paths against `repoRoot` via a security layer that prevents escaping the repository boundary. Errors from this step propagate directly to the caller without wrapping.
- **TOCTOU-Safe Writes**: `WriteFileSafely` writes to an in-directory temporary location, then atomically renames into place. Directories are created as needed with default permissions before write begins. Deferred cleanup (`tmpFile.Close`, `os.Remove`) discards errors silently — no return value reflects cleanup failures.
- **Gitignore Parsing**: Reads `.gitignore` from `repoRoot`. Non-empty, non-comment lines are collected as active patterns. A missing file returns `(nil, nil)` intentionally — not an I/O failure signal. Scanner errors are returned as-is via `scanner.Err()`.
- **Ignore Decision Logic**: A path is ignored if any component starts with a dot (hidden files) or ends with `.egg-info`, OR if it matches user-supplied gitignore patterns.
- **Recursive Code Discovery**: Walks the repository root recursively. Directories starting with a dot or ending with `.egg-info` are skipped entirely. Files passing ignore checks have their relative paths normalized to forward slash and collected as results.

---

## Security Subsystem (`internal/security`)

### Module Responsibility and Data Flow

Provides two isolated concerns for repository-level operations: (1) safe resolution of user-supplied filesystem paths to prevent traversal escapes, and (2) file-based mutual exclusion ensuring only one instance of code-reducer holds a lock at any time. A third utility integrates the lockfile into version control so stale state is ignored by `git`. Data flow begins with callers invoking `SafeResolve` or `AcquireLock`, both of which return errors to downstream consumers for branching logic. Sentinel errors from `errors.go` are returned via standard Go `error` returns and must be detected using `errors.Is()` or `errors.As()`. No wrapping occurs; propagation is contract-level, not exception-style.

### Error Contract Markers (`errors.go`)

#### Sentinel Definitions

| Variable | Condition | Mechanism |
|---|-----------|-----------|
| `ErrPathTraversal` — `"security violation: path traversal detected"` | Returned when a resolved path falls outside the repository root boundary | Standard Go error return value. Not wrapped; no panic. Detected via `errors.Is()` / `errors.As()`. |
| `ErrLockHeld` — `"lock is already held by another process"` | Returned when another code-reducer instance holds the file lock | Standard Go error return value. Not wrapped; no panic. Detected via `errors.Is()` / `errors.As()`. |

Both errors are raw `*errors.errorString` values constructed using the standard library's `errors.New`. They carry no algorithmic logic and serve solely as contract markers for callers to branch on. No exception-style wrapping occurs in this file; any processing logic resides elsewhere in the module.

### Path Sanitization (`security.go`)

#### Public API: `SafeResolve`

**Signature:** `func SafeResolve(repoRoot string, inputPath string) (string, error)`

**Responsibility:** Cleans an input path and ensures it lies strictly inside the repository root. Resolves symlinks on existing ancestor parts to block symlink-based traversal.

**Algorithm Steps:**
1. Accept a `repoRoot` absolute reference and a relative or absolute `inputPath`.
2. Resolve symlinks on existing ancestor directory components so that symlink-based traversal is blocked.
3. Walk up from the target until hitting an existing physical ancestor (skipping non-existent intermediate components).
4. Reconstruct the full resolved path from that ancestor plus the skipped suffixes.
5. Verify the final resolved path lies strictly inside the repository root; reject if it does not.

**Error Propagation:** Errors from `filepath.Abs`, `EvalSymlinks(absRoot)`, and `EvalSymlinks(current)` are wrapped with context and returned directly. The `os.Lstat` loop terminates on first success or when reaching the filesystem root (`parent == current`). Non-not-exist errors during stat are wrapped and returned immediately. If `filepath.Rel` returns an error OR the relative path starts with `".."`, a wrapped `ErrPathTraversal` is returned.

### Process Locking (`security.go`)

#### Public API: `AcquireLock`

**Signature:** `func AcquireLock(repoRoot string) (*SimpleLock, error)`

**Responsibility:** Acquires an exclusive lock file (`.code-reducer.lock`) using O_EXCL for atomicity.

**Algorithm Steps:**
1. Call `SafeResolve` to sanitize the path; errors propagate to caller unchanged.
2. Open a new file with O_EXCL; failure indicates another process holds the lock or a stale file exists.
3. On open failure: check `os.IsExist(err)`. If the lock file already exists, return a specific message instructing manual cleanup; otherwise wrap with path context and original error.
4. Write the current PID to the lock file for identification/verification.
5. On write failure after successful open: clean up by calling `f.Close()` then `os.Remove`, then return the write error.

**Return Type:** `*SimpleLock` — a struct holding per-instance state (`lockPath`, `file *os.File`, `mu sync.Mutex`, `closed bool`). No public methods other than `Unlock()`.

#### Struct Method: `(*SimpleLock) Unlock`

**Signature:** `func (l *SimpleLock) Unlock() error`

**Responsibility:** Releases the lock by closing the file and removing it. Idempotent and thread-safe.

**Algorithm Steps:**
1. Capture close error in an `err` variable.
2. Attempt `os.Remove`. Only replace `err` if (a) the previous operation succeeded AND (b) remove fails with a non-not-exist error.
3. Result: if close succeeds but remove fails, that error is returned; if close fails but remove also fails, only the close error is surfaced.

### Gitignore Integration (`security.go`)

#### Public API: `EnsureGitignoreHasLockfile`

**Signature:** `func EnsureGitignoreHasLockfile(repoRoot string) error`

**Responsibility:** Ensures that `.code-reducer.lock` is present in `.gitignore`.

**External I/O Operations:**
- `filepath.Abs` / `EvalSymlinks`: Resolve absolute paths and evaluate symlinks on the repository root/ancestor (shared with `SafeResolve`)
- `os.Lstat` (loop): Walk up directory hierarchy to find closest physically-existing ancestor during path traversal validation
- `filepath.Rel`: Verify resolved target lies strictly inside resolved root
- `os.OpenFile(... O_EXCL)`: Atomic create of lock file; failure indicates another process holds the lock or a stale file exists
- `f.Write([]byte{...})`: Write current PID to lock file for identification/verification
- `os.ReadFile`: Read existing `.gitignore` contents before appending entry
- `os.CreateTemp` + `tmpFile.Write`: Atomic write pattern — build new content in temp file, then rename over original
- `tmpFile.Sync`: Force flush to disk before close/rename
- `os.Chmod`: Set permissions on temp file matching default (0644)
- `os.Rename`: Atomically replace `.gitignore` with updated version

**Error Propagation:** SafeResolve errors propagate directly. Non-not-exist read failures are wrapped with `"error reading .gitignore"` context. All temp file operations (`CreateTemp`, `Write`, `Sync`, `Close`, `Chmod`) return wrapped errors to caller on failure. Final `os.Rename` error is returned if it fails.

### Module-Level Observations — security subsystem

- **No network/database interactions:** Confirmed by full code inspection. All I/O targets are local paths under `repoRoot`.
- **Mutable state:** None at the package level. The file contains only `const` values and function-local variables; no package-level or class-level properties are modified.
- **Shared/instance state:** `SimpleLock` holds per-instance fields (`lockPath`, `file *os.File`, `mu sync.Mutex`, `closed bool`). `Unlock()` modifies `l.closed = true` under its own internal mutex. This is instance-scoped state, not shared global state.
- **Package-level constant:** `defaultFilePerm = 0644` — used by temp file permission setting in the gitignore integration path.

---

## Tools Subsystem (`internal/tools`)

### Module Responsibility

Provides two subsystems: a repository-aware file I/O and discovery layer, and a Git command abstraction. Both operate on a `repoRoot` boundary to prevent path traversal outside the codebase. No mutable state exists in either subsystem; all operations are stateless per-call.

### File Tools (`file_tools.go`)

#### Public API

| Function | Signature |
|----------|-----------|
| ReadFileSafely | `func ReadFileSafely(repoRoot, virtualPath string) ([]byte, error)` |
| WriteFileSafely | `func WriteFileSafely(repoRoot, virtualPath string, content []byte) error` |
| LoadGitignore | `func LoadGitignore(repoRoot string) ([]string, error)` |
| ShouldIgnoreFile | `func ShouldIgnoreFile(relPath string, gitIgnore *ignore.GitIgnore) bool` |
| DiscoverCodeFiles | `func DiscoverCodeFiles(repoRoot string, ignores []string) ([]string, error)` |

#### Data Flow

1. **Safe Path Resolution**: All file operations resolve user-supplied virtual paths against `repoRoot` via a security layer that prevents escaping the repository boundary. Errors from this step propagate directly to the caller without wrapping.
2. **TOCTOU-Safe Writes**: `WriteFileSafely` writes to an in-directory temporary location, then atomically renames into place. Directories are created as needed with default permissions before write begins. Deferred cleanup (`tmpFile.Close`, `os.Remove`) discards errors silently — no return value reflects cleanup failures.
3. **Gitignore Parsing**: Reads `.gitignore` from `repoRoot`. Non-empty, non-comment lines are collected as active patterns.