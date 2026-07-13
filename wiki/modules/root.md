# Code-Reducer Architecture Documentation

## Module Responsibility

Code-Reducer is a CLI tool that generates and maintains project wiki documentation (`wiki/architecture.md`, `wiki/quickstart.md`, per-module API summaries) via an LLM-driven synthesis pipeline operating on source code repositories. The system runs in two modes—**init** (full generation from scratch) and **update** (incremental regeneration triggered by detected file changes). Execution proceeds through a hierarchical directory tree: leaf files are synthesized first, their summaries flow up through child directories to module-level aggregates, then converge at the repository root where global `architecture.md` and `quickstart.md` are produced.

## Data Flow Overview

```
┌───────────────┐    ┌──────────────┐    ┌─────────────────┐    ┌──────────────┐
│ cmd.RootCmd   │──►│ executeCommand│──►│ runner.Run(...)  │──►│ Config       │
│ (entry point) │   │("init"|"update")│   │                  │    │ SaveConfig   │
└───────────────┘   └──────────────┘   └─────────────────┘   └──────────────┘
```

Errors flow as wrapped Go errors through cobra's `RunE` interface. Event-emitted errors (via callback) are printed to stderr and swallowed; only the runner's return error is re-wrapped with a descriptive prefix before propagation.

---

## Subsystem: `cmd` — CLI Command Registry & Execution Engine

### Entry Point (`main.go`)

The `main` package executable delegates entirely to `github.com/arrase/code-reducer/cmd`. If execution fails, the error is printed to stderr and the process exits with code 1; on success it exits normally (code 0). No direct I/O occurs in this file.

### Root Command & Execution Loop (`cmd/root.go`)

**Precondition Validation Sequence:**

1. **Git Repository Check** — `os.Getwd()` captures current working directory; `tools.VerifyGitRepo(repoRoot)` checks for `.git` existence and equivalent state. Errors propagate through cobra as wrapped errors via `%w`.
2. **Configuration Existence** — `config.ConfigExists(repoRoot)` reads the local filesystem to confirm a configuration file is present. If missing *and* stdin is not a terminal (TTY), an implicit setup flow runs: `RunSetupFlow(repoRoot)` creates one and returns any error as-is.
3. **Mode Guard** — `engine.IsInitialized(...)` validates that the requested operation matches current state: `init` mode fails if documentation has already been initialized → suggests using `update`; `update` mode fails if initialization has not occurred yet → requires running `init` first.

**Configuration Resolution:** `config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag)` merges CLI flags (`--model-id`, `--num-ctx`) with on-disk settings and environment variables. Returns the resolved config or a raw error from the config package (no wrapping). The LLM model ID flows through but is not consumed in this file; it persists for later use by the engine.

**Signal Handling & Execution Loop:** `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` registers interrupt and termination signals on a background context. The handler calls `stop()` via deferred execution, enabling graceful shutdown propagation to the engine runner. This is the only concurrency primitive in this file; no goroutines or mutexes are used.

The resolved configuration is passed to `runner.Run(ctx, repoRoot, engine.Mode(mode), ...)`. Status events print to stdout (`fmt.Println(ev.Message)`), error events print to stderr (`fmt.Fprintf(os.Stderr, "Error: %s\n", ev.Message)`), and other events print to stdout. Errors from the runner are wrapped with `"documentation run failed: %w"` before returning.

### init Command Registration (`cmd/init.go`)

`initCmd` → unexported `*cobra.Command`, registered under `RootCmd`. The `RunE` closure delegates directly to `executeCommand("init")`. No panic or crash path is introduced here; errors propagate through cobra's command framework and exit via non-zero process code when a non-nil error is returned.

### Update Command (`cmd/update.go`)

All identifiers begin with lowercase letters and are unexported. The `RunE` closure delegates to `executeCommand("update")`. Actual implementation lives in the dependency; no panic or crash recovery is handled within this file.

### Interactive Setup Wizard (`cmd/setup.go`)

**Public API Surface:**

| Function | Signature | Purpose |
|---|---|---|
| `RunSetupFlow` | `func RunSetupFlow(repoRoot string) error` | Orchestrates the full interactive configuration flow; returns non-nil only for filesystem-level failures (`os.Getwd`, `config.SaveConfig`) |
| `promptStringList` | `func promptStringList(reader *bufio.Reader, promptMsg string, existingList []string) ([]string, bool)` | Collects comma-separated or list-style input with an existing value fallback; returns `(result, modified)` where `modified=false` signals no change |
| `promptString` | `func promptString(reader *bufio.Reader, promptMsg string, existingVal string) string` | Collects single-string input with a default shown in brackets; returns trimmed input or `existingVal` on empty/error |

**Algorithm Steps:**

1. **Repo Root Resolution** — `os.Getwd()` captures the base path at start of `RunSetupFlow`.
2. **State Loading** — `config.LoadConfig(repoRoot)` reads previously saved configuration; non-empty values (model ID, URL, context size, ignore list, docs dir) are extracted for pre-population.
3. **Prompt Sequence** — Four prompts run in order: LLM Model ID, Ollama Base URL, Context Size, Documentation Directory. Each uses the `promptString`/`promptStringList` pattern with existing values as fallbacks.
4. **System Prompt Preservation** — Existing values for extraction, synthesis, architecture, and file fact consolidation prompts are carried forward when present.
5. **Configuration Assembly** — All user inputs merge with defaults/previous state into a single `Config` struct.
6. **Persistence** — `config.SaveConfig(repoRoot, newCfg)` writes the assembled config to disk. Errors bubble up un-wrapped.

**Error Handling Strategy:** `promptString` / `promptStringList`: swallow `ReadString` errors → print warning to stderr → return existing value with no propagation beyond the function. `strconv.Atoi(ctxInputStr)`: swallow parse failure or non-positive result → fall back to `existingNumCtx`; no error propagates. Only filesystem-level failures (`Getwd`, `SaveConfig`) propagate up the call stack.

---

## Subsystem: `internal/config` — Configuration Resolution & Persistence

### Responsibility

The `config` package owns the lifecycle of a single configuration object (`*Config`) consumed by every downstream component. It provides three operations: resolve, persist, and query. All resolution is deterministic—given identical inputs, the returned `*Config` is identical. No global mutable state exists within the package; the only shared state is a package-level variable holding default extraction steps (`DefaultExtractionSteps`).

### Data Flow

```
CLI flags (modelIDFlag, numCtxFlag) ─┐
Env vars (CODE_REDUCER_MODEL_ID,     │   ┌─────────────┐
OLLAMA_BASE_URL, OLLAMA_NUM_CTX)    ─┼──►│ LoadConfig  │─► Config
YAML file (.code-reducer.yaml)      └──>│ getConfigPath│
                                        └─────────────┘

Resolved *Config ──► SaveConfig() ──► disk: .code-reducer.yaml
```

### Constants

| Constant | Value / Purpose |
|---|---|
| `CodeReducerModelIDEnvKey` | Env key `"CODE_REDUCER_MODEL_ID"` consumed by `ResolveConfig`. |
| `OllamaBaseURLEnvKey` | Env key `"OLLAMA_BASE_URL"` consumed by both `LoadConfig` and `ResolveConfig`. |
| `OllamaNumCtxEnvKey` | Env key `"OLLAMA_NUM_CTX"` consumed by `ResolveConfig`; value parsed via `strconv.Atoi`. |
| `OllamaDefaultBaseURL` | Hard-coded fallback URL `"http://localhost:11434"`. |
| `OllamaDefaultModelID` | Hard-coded fallback model identifier `"ornith:9b"`. |
| `OllamaDefaultNumCtx` | Hard-coded fallback context size `8192`. |
| `DefaultDocsDir` | Hard-coded fallback docs directory name `"wiki"`. |
| `ConfigFileName` | Config file basename `".code-reducer.yaml"`. |

### Types: `ExtractionStep` and `Config`

```go
type ExtractionStep struct {
    Name   string  // yaml:"name"
    Prompt string  // yaml:"prompt"
}

type Config struct {
    ModelID                              string
    OllamaBaseURL                       string
    OllamaNumCtx                        int
    DocsDir                             string
    SystemPrompt                        string
    ModuleSynthesisPrompt               string
    ArchitecturePrompt                  string
    FileFactConsolidationPrompt         string
    ExtractionSteps                     []ExtractionStep
    Ignore                              []string
}
```

- `Config` is the sole configuration container. Every field maps to a distinct YAML key; all four prompt categories are independent strings with no cross-field dependencies.
- `ExtractionStep` is an opaque two-string struct used only inside the slice field of `Config`. The package-level variable `DefaultExtractionSteps` holds a static instance initialized at declaration and never mutated within this file.

### Variable: `DefaultExtractionSteps`

```go
var DefaultExtractionSteps []ExtractionStep
```

Package-level slice pre-populated with four entries covering API signatures, business logic, state and concurrency analysis, and errors/side effects. Read-only within the package boundary; any modification must occur outside this file.

### Functions: Configuration Resolution (`internal/config/resolve.go`)

#### `ResolveConfig(repoRoot string, modelIDFlag string, numCtxFlag string) (*Config, error)`

Merges CLI flags, environment variables, YAML config, and built-in defaults into a single resolved `*Config`. Returns nil error on success; returns both a non-nil Config and an error only when the underlying file read fails with a condition that is not "file does not exist".

**Parameters:**

| Parameter | Purpose |
|---|---|
| `repoRoot` | Repository root path. Used to derive the config file path for the disk-read step. |
| `modelIDFlag` | Explicit model ID from CLI; overrides environment and YAML only if non-empty. |
| `numCtxFlag` | Explicit context size from CLI; parsed as int with a minimum-of-1 validation gate before taking effect. |

**Resolution Priority Chains:** Every field follows an independent priority chain:

- **Model ID** — built-in default → YAML `model_id` → env `CODE_REDUCER_MODEL_ID` → CLI flag (skipped if empty).
- **Ollama Base URL** — built-in default → YAML `ollama_base_url` → env `OLLAMA_BASE_URL`. No CLI override.
- **Ollama Context Size** — built-in default → YAML `ollama_num_ctx` → env `OLLAMA_NUM_CTX` → CLI flag (parsed via `strconv.Atoi`, minimum 1). Env and CLI values that fail to parse are silently skipped; the previously-resolved fallback remains in effect.
- **DocsDir** — built-in default if unset, otherwise uses YAML-specified value. No env or CLI override path exists for this field.
- **Prompts** (system, synthesis, architecture, fact consolidation) — each has its own independent chain: built-in default → YAML override if non-empty. No CLI or env override is provided in this code.

**Error Handling:**

| Condition | Behavior |
|---|---|
| `LoadConfig(repoRoot)` returns error + not-exist | Swallowed; replaced with an empty `&Config{}` and returned alongside a nil error. |
| `LoadConfig(repoRoot)` returns any other error | Wrapped as `"failed to load configuration file: %w"` and returned. |
| Integer parsing (`strconv.Atoi`) fails | Skipped; previous fallback retained. No error propagated. |

### Functions: Configuration Persistence (`internal/config/io.go`)

#### `getConfigPath(cwd string) string`

Derives the absolute path to `.code-reducer.yaml` by combining the working directory with the fixed filename constant. Return value is a plain string; no error path exists in this signature.

#### `ConfigExists(cwd string) bool`

Returns true if the config file is present at the derived path, false otherwise. Uses stat semantics internally (`os.Stat`). No error propagation—callers cannot distinguish "file does not exist" from other OS-level errors (e.g., permission denied).

#### `LoadConfig(cwd string) (*Config, error)`

Reads raw bytes from disk at the resolved config path, then unmarshals them into a `*Config` struct via YAML parsing. Any parse failure is wrapped and returned as an error with descriptive prefix `"failed to parse yaml config: %w"`.

#### `SaveConfig(cwd string, cfg *Config) error`

Persists `*cfg` back to disk using an atomic rename pattern:

1. Marshals the in-memory config to YAML.
2. Normalizes key prefixes by inserting extra blank lines before specific top-level keys (`system_prompt`, `module_synthesis_prompt`, `architecture_prompt`, `file_fact_consolidation_prompt`, `extraction_steps`, `ignore`) — purpose of this normalization is not documented within the file boundary.
3. Creates a temporary file in the same directory via `os.CreateTemp`.
4. Writes normalized YAML to the temp file, syncs, and closes it.
5. Sets restrictive permissions on the temp file via `chmod`.
6. Renames the temp file over the original config path atomically.

Any failure at any step (read, parse, write, sync, close, chmod, rename) is wrapped with a descriptive prefix (`"failed to create temp file:"`, etc.) and returned as an error. Cleanup of the temp file runs in a defer block so removal happens even when errors occur mid-save. A redundant explicit `Close()` call follows the deferred one; closing an already-closed file returns nil, so this is non-fatal but unnecessary.

---

## Subsystem: `internal/engine` — Incremental Documentation Synthesis Engine

### Responsibility

This package orchestrates automatic generation of architecture overviews (`architecture.md`), quickstart guides (`quickstart.md`), and per-module API summaries for software repositories. It operates in two modes—**init** (full generation from scratch) and **update** (incremental regeneration triggered by file changes). The engine consumes source code through a hierarchical synthesis pipeline driven by LLM calls, persists intermediate state to disk for resumption between runs, and uses an exclusive lock file to serialize concurrent invocations.

### Data Flow

```
Runner.Run(ctx, repoRoot, mode) 
    ↓ acquires lock via security.AcquireLock(repoRoot)
orchestrator.RunInit(ctx, ...) | orchestrator.RunUpdate(ctx, ...)
    ↓ setupPipeline() loads gitignore + discovers code files + computes SHA256 hashes per file
tree.buildTree(files) → DirNode hierarchy
    ↓ synthesizeNode(root) recurses post-order: child dirs first (reusing cached summaries), then each file
client.CallLLM(...) — LLM API call for extraction step on chunked content
reduceInChunks / reduceFileFacts — recursive text synthesis with batch partitioning and overlap handling
cache.saveMetadataCache(repoRoot, docsDir, cache) — persists Files + Modules maps to disk as JSON
```

### `cache.go` — Persistent Metadata Cache

#### Types

- **`FileCacheEntry`** — Key-value pair mapping a file's SHA256 hash to its synthesized facts string. Used as the per-file cache entry in `MetadataCache.Files`.
- **`MetadataCache`** — Package-level cache state containing:
  - `Version int` — Cache format version; mismatched versions return an empty initialized structure rather than corrupt data.
  - `StepsHash string` — SHA256 of serialized extraction steps, used to invalidate the entire cache when configuration changes.
  - `Files map[string]FileCacheEntry` — Per-file hash→facts mapping for incremental updates.
  - `Modules map[string]string` — Per-directory synthesized summary storage.

#### Public API Surface

- **`IsInitialized(repoRoot, docsDir string) bool`** — Probes `{docsDir}/{metadataFileName}` existence; returns `false` on read failure (error swallowed).
- **`saveMetadataCache(repoRoot, docsDir string, cache *MetadataCache) error`** — Serializes the metadata cache to indented JSON at `{docsDir}/{metadataFileName}`. Sets `cache.Version = currentCacheVersion` before marshaling. JSON marshal errors propagate; write failures propagate as-is.
- **`computeSHA256(repoRoot, virtualPath string) (string, error)`** — Reads a source file via `tools.ReadFileSafely`, computes SHA256 hash in memory. Read errors propagated directly.

#### Internal Functions

- **`loadMetadataCache(repoRoot, docsDir string) (*MetadataCache, error)`** — Constructs an empty `MetadataCache` with fresh maps (`make(map[string]FileCacheEntry)`, `make(map[string]string)`). If the cache file exists and parses successfully with matching version, returns parsed state + nil. On `os.ErrNotExist`: returns default structure + **nil error** (silent recovery). Other read errors wrapped via `%w`. Version mismatch: returns clean cache + nil without signaling an error.

#### Thread Safety Assessment

No synchronization primitives present. The maps in `MetadataCache` are created fresh and returned as struct values, but callers receive map references pointing to shared backing storage. Concurrent access to `Files`/`Modules` from multiple goroutines constitutes a data race unless external locking exists outside this module boundary. On-disk writes via `saveMetadataCache` provide coarse-grained serialization through OS write ordering alone; no explicit mutex is used.

### `chunking.go` — Recursive Hierarchical Text Synthesis

#### Internal Functions (All Unexported)

- **`reduceWithLLM(ctx, c llmCaller, items []string, sysPrompt string, buildPrompt func(batch []string) string, logMsg func(batch []string) string, errMsg string, logEvent LogEventFunc) (string, error)`** — Feeds a batch of items to an LLM via `c.CallLLM(ctx, sysPrompt, messages, false)`. System prompt describes the synthesis task. Response markdown fences are stripped before returning. Errors from `CallLLM` wrapped as `fmt.Errorf("%s: %w", errMsg, err)` where `errMsg` is caller-supplied (e.g., `"LLM error during synthesis"`). Original error type information lost for classification purposes.

- **`reduceInChunks(ctx, c llmCaller, nodePath string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)`** — Delegates to `reduceWithLLM`. No external I/O beyond the LLM call.

- **`reduceFileFacts(ctx, c llmCaller, filePath string, stepName string, items []string, cfg *config.Config, logEvent LogEventFunc) (string, error)`** — Delegates to `reduceWithLLM`. Used per extraction step during file synthesis in `synthesize.go`.

- **`reduceItems(ctx, items []string, maxChars int, reduceFn func(batch []string) (string, error)) (string, error)`** — Core recursive reduction engine:
  1. Partitions input items into batches where total character count stays within `maxChars`. Oversized single items are truncated in-place via `items[0] = string(runes[:maxChars]) + "\n...[truncated]"` (mutation without signal).
  2. If exactly one batch exists, calls the reduction function on it directly.
  3. Otherwise, recursively reduces each sub-batch independently, concatenates results, and recurses until a single result remains — forming a binary tree of reductions.

- **`chunkTextWithOverlap(text string, maxRunes int, overlapRunes int) []string`** — Splits a single text into overlapping rune-counted chunks at fixed step size. Used for preparing input sequences requiring positional overlap between adjacent pieces during file synthesis.

#### State and Side Effects

- `reduceItems` mutates `items[0]` in-place when a single oversized item is encountered. This silent truncation provides no error signal to callers; data loss goes undetected unless output string is inspected.
- All other functions are pure (string manipulation, recursion) with the sole exception of the LLM API call inside `reduceWithLLM`.

### `client.go` — Ollama HTTP Client

#### Types

- **`Message`** — Struct with `Role string \`json:"role"\`` and `Content string \`json:"content"\``. Used as the conversational turn schema for LLM requests.
- **`ollamaRequest`, `ollamaOptions`, `ollamaResponse`** — Unexported structs holding request/response payloads.

#### Methods on `*llmClient`

- **`newLLMClient(modelID, baseURL string, numCtx int) *llmClient`** — Constructor storing model identifier, base URL, and context size. Field assignments occur once; no subsequent mutation within the file.
- **`(c *llmClient).NumCtx() int`** — Returns stored `numCtx` value. Read-only accessor.
- **`(c *llmClient).prepareOllamaRequest(ctx, systemPrompt string, messages []Message, jsonFormat bool) (*http.Request, error)`** — Prepends the system prompt as a new entry to the messages array, then serializes model ID + conversation context + processing options into JSON. Constructs HTTP POST request targeting `{baseURL}/api/chat`. JSON marshal failures wrapped with `%w`; request construction errors propagated as-is.
- **`(c *llmClient).CallLLM(ctx, systemPrompt string, messages []Message, jsonFormat bool) (string, error)`** — Executes the HTTP POST via `c.httpClient.Do` with timeout applied to the client itself (`&http.Client{Timeout: defaultHTTPTimeout}`). On 200 status: deserializes response body fully and returns generated text. On non-OK status: reads up to `maxErrorBodyBytes` (value undefined in this file; if zero, `io.LimitReader(resp.Body, 0)` blocks indefinitely until EOF rather than applying a size limit), then returns formatted error `"ollama api error: status %d, response: %s"` where the body read failure is silently swallowed — only the HTTP status and successfully-read bytes are visible in the returned error.

#### Unresolved References

- `defaultHTTPTimeout` and `maxErrorBodyBytes` referenced but not defined in this file; resolved at link time or via another package constant. If these resolve to zero values, behavior deviates from intended timeout/limit semantics.

### `constants.go` — Runtime Configuration Contract

#### Function Types

- **`LogEventFunc func(EventType, string)`** — Signature for external observer hooks into system events (type + message), enabling traceability without hardcoding logging implementation.

#### Constants and Values

This file defines the numerical boundaries governing engine behavior:
- HTTP operation timeout: 10 minutes before aborting network calls.
- Error response truncation limit: responses beyond 1 KB are truncated for non-critical error paths.
- Chunked text processing: input split into overlapping segments of ~800 characters, enabling streaming across large documents without full loading.
- Context window budgeting: only 75% of total memory reserved; minimum floor enforced at ~512 tokens regardless of available space.
- Output scaling rule: maximum generated output capped at roughly 3× input length to prevent runaway generation loops.
- Persistent state layer: engine writes `.metadata.json` snapshot and reads agent behavior from `AGENTS.md`, establishing a file-based identity layer for long-running processes.
- Directory permissions: any directories the engine creates are assigned 0755 by default, controlling filesystem boundary access.

### `json_parser.go` — Markdown Fence Stripping Utility

#### Internal Functions

- **`stripOuterMarkdownFence(content string) string`** — Best-effort extraction of raw content from strings wrapped in backtick-delimited markdown or JSON fences (3+ backticks, optional language identifier like `markdown` or `json`). Algorithm: trim whitespace → test against regex pattern for fence syntax → if match found, extract interior between opening and closing fences; otherwise return trimmed input unchanged. Regex mismatch returns original with whitespace stripped — no error signaled.

#### Side Effects

No external I/O. Only standard library `regexp` and `strings` used for in-memory processing. If a malformed regex were supplied at compile time it would panic during package initialization, but this is not handled within the function scope.

### `orchestrator.go` — Incremental Documentation Generation Orchestrator

#### Types

- **`EventType`** (`type string`) — Event classification type.
- **`EventStatus`, `EventError`** (`constant EventType`) — Predefined event status/error constants.
- **`Event struct`** — `Type EventType`, `Message string`. Carries typed event data with message payload.

#### Methods on `*orchestrator`

- **`GenerateStandardDocs(ctx, repoRoot, docsDir, rootSum string, cfg *config.Config, logEvent LogEventFunc) error`** — Generates `architecture.md` and `quickstart.md` from the synthesized root summary. Writes both files via `tools.WriteFileSafely`. Errors wrapped and propagated.

- **`RunInit(ctx, repoRoot string, cfg *config.Config, onEvent func(Event)) error`** — Full generation path:
  1. Loads `.gitignore`, user-configured ignore paths, metadata cache from disk.
  2. Discovers code files respecting ignores; errors propagated directly as returned errors.
  3. Computes SHA256 hashes per file (per-file failures logged + skipped from processing).
  4. Builds hierarchical directory tree from all discovered files.
  5. Marks every directory as "affected" for fresh generation.
  6. Traverses tree bottom-up: each node's summary becomes part of its parent's input; leaf summaries flow up through directories, then into module-level summaries, converging at repository root.
  7. Uses root-level synthesis result to generate global `architecture.md` and `quickstart.md`.
  8. Ensures `.agent-guidelines.md` exists: if missing, creates fresh; if contains "AI Agent Guidelines", appends; otherwise replaces. Read failures for this file are swallowed — code proceeds as if empty/missing. Write errors wrapped and returned.

- **`RunUpdate(ctx, repoRoot string, cfg *config.Config, onEvent func(Event)) error`** — Incremental path:
  1. Detects file changes by comparing current hashes against cached ones — classifying each as Added, Modified, or Deleted.
  2. Builds tree only from currently existing files; prunes cache entries for deleted files and removes module documentation for directories no longer present.
  3. Determines affected directories based on change set; propagates "affected" status through hierarchy.
  4. If nothing changed (zero affected directories), returns immediately with no regeneration.
  5. Otherwise re-runs hierarchical synthesis within affected modules only.

- **Private Functions** — `setupPipeline(repoRoot, cfg, logEvent) (string, []string, *MetadataCache)` loads gitignore + discovers files + computes hashes; errors from `LoadGitignore` swallowed after logging warning event. `teardownPipeline(repoRoot, docsDir, cache, logEvent, successMsg)` saves metadata cache — write failures logged as warnings but **not returned** (silently swallowed).

#### Error Propagation Summary

| Operation | Failure Handling |
|---|---|
| LLM call (`CallLLM`) | Wrapped + propagated as returned error |
| Write file (`WriteFileSafely`) | Wrapped + propagated in main path; swallowed in teardown |
| Read file (`ReadFileSafely` for `.agent-guidelines.md`) | Swallowed — falls through to write path |
| Load gitignore | Logged warning, swallowed |
| Discover files | Propagated as returned error |
| SHA256 hash per file | Per-file: logged + skipped from processing |
| MkdirAll | Wrapped + propagated |
| SafeResolve (path existence) | Errors treated as "not found", swallowed |
| os.Remove (cleanup) | Swallowed silently — stale files remain on disk if deletion fails |

### `runner.go` — Pipeline Execution Wrapper with Locking

#### Types and Constants

- **`Mode type string`** — Operation mode of the documentation pipeline.
- **`ModeInit`, `ModeUpdate`** (`const Mode`) — Init mode constant and update mode constant.
- **`Runner struct`** — Orchestrates execution of the documentation pipeline. Holds immutable reference to configuration set once via `NewRunner`.

#### Methods on `*Runner`

- **`NewRunner(cfg *config.Config) *Runner`** — Constructor storing configuration values (model ID, base URL, context size). No field reassignment within this file after construction.
- **`(r *Runner) Run(ctx, repoRoot string, mode Mode, onEvent func(Event)) error`** — Main entry point:
  1. Attempts to add lockfile entry to `.gitignore` via `security.EnsureGitignoreHasLockfile(repoRoot)`; failure swallowed — only logged via callback.
  2. Acquires exclusive repository lock via `security.AcquireLock(repoRoot)`. Failure propagated with wrapping: `"failed to acquire repository lock: %w"`.
  3. Defer unlock for after pipeline completes (success or error). No explicit error handling for unlock failures visible in this file.
  4. Instantiates LLM client and orchestrator based on stored configuration values.
  5. Dispatches either `RunInit` or `RunUpdate` depending on mode string match; unknown modes return `"unsupported mode: %s"` without wrapping.

### `synthesize.go` — Recursive Context-Aware Synthesis Engine

#### Internal Structures and Functions

- **`pipelineContext struct`** — Holds shared state for a synthesis run: LLM client, repository root, docs directory, configuration, extraction steps, affected directories set, precalculated hashes map. Locally allocated per method call in `RunInit`/`RunUpdate`.
- **`synthesizeNode(p *pipelineContext, node *DirNode) (string, error)`** — Recursive synthesis walking the directory tree:
  1. Post-order traversal: process children before files. For each child directory, recursively call `synthesizeNode`; if child is not affected and has a cached summary, reuse directly.
  2. Per-file processing loop: compute or retrieve SHA256 hash (from precalculated hashes map or read-and-hash). On cache hit (hash matches stored entry), return cached facts without reading the file. On cache miss, read file content; if multiple chunks needed based on dynamic context window size × 4 chars/token × allocation ratio, split with overlap. For each chunk, run LLM extraction across all configured `ExtractionSteps`, building consolidated fact per step via `reduceFileFacts`.
  3. Component assembly: build list of components — file facts (prefixed by filename) followed by child directory summaries (prefixed by subsystem name). If no components exist, cache empty string and return early.
  4. Final reduction: combine all assembled components into single consolidated summary for current directory using `reduceInChunks`. Cache result under `p.cache.Modules[node.Path]` and write to disk at `<docsDir>/modules/<safe-filename-of-path>`.

#### External Interactions

- **Disk Read**: `tools.ReadFileSafely(p.repoRoot, f)` reads individual source files when cache misses occur. Errors logged as warnings via callback but do not propagate — facts for that file remain empty string; processing continues to next file.
- **Disk Write**: `tools.WriteFileSafely(p.repoRoot, modulePath, []byte(finalSum))` writes synthesized documentation at `<repoRoot>/<cfg.DocsDir>/modules/<safe-filename>`. Failures wrapped with context: `"failed to write module documentation for %s: %w"`. Propagates up through synthesis stack.
- **Network/API**: `p.client.CallLLM(p.ctx, systemPrompt, []Message{...}, false)` makes LLM API calls per extraction step and chunk combination during file synthesis. Multiple sequential calls made per file (one per `cfg.ExtractionSteps`), multiple files processed before moving to child summary reduction.

#### Thread Safety Assessment

No explicit locks/mutexes visible in this file. Context (`p.ctx`) checked at boundaries for cancellation detection but does not prevent concurrent map writes to the cache. If multiple goroutines invoke `synthesizeNode` with different `pipelineContext` instances sharing the same underlying `*MetadataCache`, both `cache.Files` and `cache.Modules` maps will be concurrently written without locking — a data race unless external synchronization exists outside this module boundary.

### `tree.go` — Directory Tree Building and Affected Detection

#### Types

- **`FileChange struct`** — `Path string` (file path), `Status string` (change status: `"Added"`, `"Modified"`, `"Deleted"`).
- **`DirNode struct`** — `Path string` (directory path), `Files []string` (list of file paths in this directory), `Children map[string]*DirNode` (child directory nodes keyed by name).

#### Internal Functions

- **`buildTree(repoRoot, files []string) *DirNode`** — Walks each path's directory components to create nested `DirNode` objects with files listed under parent directories. Mutated during tree construction only; single-threaded build path.
- **`determineAffected(p *pipelineContext, node *DirNode) bool`** — For each node: checks if any file in its `Files` list matches a changed file (marks as affected); resolves expected module path via safe filename conversion and secure resolution; if resolved path does not exist on disk, marks as affected (indicating missing module); checks external metadata cache (`cache.Modules`); if no cached value recorded for this directory's path, marks as affected.
- **`propagateAffected(p *pipelineContext) map[string]bool`** — Walks tree again; any node whose subtree contains an affected descendant gets itself marked as affected. Parent directories inherit children's impact.

#### External I/O and Error Propagation

- `determineAffected`: calls `os.Stat(absModulePath)` after resolving a module path via `security.SafeResolve`. Only the "not exist" case is handled; if `SafeResolve` returns an error or `Stat` returns a non-not-exist error (e.g., permission denied, I/O fault), it is not checked and never propagated to callers.
- All other functions (`propagateAffected`, `buildTree`) are pure in-memory operations using only string manipulation via standard library imports — no actual read/write calls.

### `utils.go` — Internal Utility Helpers

#### Internal Functions (Both Unexported)

- **`toSafeMarkdownFilename(path string) string`** — Converts a module path into a filesystem-safe markdown filename by replacing `/` with `_`, defaulting to `root.md` when the result is empty or just `.`. Single-step transformation using only in-memory string manipulation (`strings.ReplaceAll`). Silently returns computed string regardless of input — no error signaling.
- **`makeLogEvent(onEvent func(Event)) LogEventFunc`** — Produces a `LogEventFunc` that forwards an `EventType` and message string to an optional callback, with nil-safety for the callback. Returns a ready-to-use closure without error handling.

#### Side Effects

No external side effects. Both functions perform only in-memory operations: path separator replacement, default value fallback, and closure wrapping. No disk, network, database, or API interactions occur.

---

## Subsystem: `internal/security` — Security Boundaries & Inter-Process Exclusivity

### Responsibility

Enforces security boundaries and inter-process exclusivity during Code-Reducer operations. The module prevents directory-traversal attacks by constraining resolved paths within the repository root, ensures shared resources are not accessed concurrently via file locking, and integrates with version control to keep lock state out of tracked content. No mutable package-level state exists; all concurrency control is instance-scoped.

### Data Flow

The two sentinel errors defined in `errors.go` (`ErrPathTraversal`, `ErrLockHeld`) serve as the terminal failure signals for path-validation and lock-contention scenarios. Callers receive these values through standard Go error-return patterns and propagate them via `if err != nil` checks; no panic, crash, or custom wrapping occurs within this package. The actual I/O operations that trigger these errors reside in other files not shown here.

### Type: `SimpleLock`

```go
type SimpleLock struct {
    lockPath string
    file     *os.File
    mu       sync.Mutex
    closed   bool
}
```

**State characteristics:**

- **`lockPath string`** — read-only after construction; never reassigned within this package. Assigned exclusively by the constructor.
- **`file *os.File`** — modified inside `Unlock()` (`l.file = nil`) and assigned once in `AcquireLock()`.
- **`mu sync.Mutex`** — value-type mutex embedded on each instance. Acquired and released only within `Unlock()` via defer.
- **`closed bool`** — modified inside `Unlock()` (`l.closed = true`).

#### `Unlock() error`

Closes the underlying file descriptor, removes the lockfile atomically (ignoring errors if the file no longer exists), and sets internal state to closed. Safe to call multiple times or from different goroutines without deadlocking. Returns a non-nil error only when both close and remove fail; otherwise returns nil. No panic path observed.

#### `SafeResolve(repoRoot string, inputPath string) (string, error)`

Resolves an arbitrary input path against the repository root boundary. Walks upward via `os.Lstat` until hitting the first physically-existing directory or reaching the root (`parent == current`). Symlinks are evaluated on existing ancestor components to defeat symlink-based traversal attacks. Non-existent suffix components are appended after the walk completes. If the relative path starts