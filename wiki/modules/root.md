# Code-Reducer — Architecture Documentation

---

## Module Responsibility & Data Flow

Code-Reducer is a terminal-based documentation-generation agent that produces and maintains technical wikis from Go source code by invoking an Ollama-compatible LLM endpoint. The module implements three phases: **initialization** (repository scan + wiki scaffolding), **interactive setup** (configuration prompts → `.code-reducer.yaml` persistence), and **incremental update** (change detection → selective regeneration). All pipeline execution routes through a single `cmd.RootCmd.Execute()` entry point, with subcommand handlers delegating to an external executor.

---

## Package: cmd — CLI Command Wiring & Lifecycle Management

### Root Command Initialization (`root.go`)

The root command registers two persistent flags via `PersistentFlags`: `--model-id` (bound to package-level `modelIdFlag`) and `--num-ctx` (bound to package-level `numCtxFlag`). On invocation, the following sequence executes:

1. **Environment validation** — `os.Getwd()` resolves the current working directory; failure is wrapped as `"failed to get current working directory: %w"` and returned immediately.
2. **Configuration resolution** — merges CLI flags, environment variables, and an optional persistent config file loaded via `internal/config`. Resolution details are delegated outside this package boundary.
3. **Implicit setup on first run** — if the persistent config file does not exist *and* stdin is a terminal (`isatty.IsTerminal(os.Stdin.Fd())` / `isatty.IsCygwinTerminal(os.Stdin.Fd())`), an automatic initial configuration step triggers without requiring user intervention. On non-TTY (CI / piping), this step is skipped and the caller must configure manually via `setup`.
4. **Mode-gated lifecycle checks** — enforced only within the runner/engine layer; no validation of project state occurs at registration time. The two modes (`init`, `update`) are mutually exclusive on an already-initialized project; enforcement lives in the external executor.
5. **Graceful shutdown** — installs SIGINT / SIGTERM handlers via `signal.NotifyContext`. A deferred `stop()` cancels the context; any in-flight work that respects the cancelled context will exit. No error-return path exists for signal handling within this file.
6. **Execution loop** — delegates to `engine.NewRunner.Run()` with the resolved config and current directory. The runner emits typed events (`status`, `error`, default).

Cobra output suppression: `RootCmd` has `SilenceErrors: true` and `SilenceUsage: true`. Errors that would otherwise be printed by cobra's default handler are suppressed — callers must handle errors explicitly. No panic calls observed; all failure paths return errors propagated to cobra.

### Interactive Configuration Setup Flow (`setup.go`)

The `RunSetupFlow(repoRoot)` function generates a `.code-reducer.yaml` configuration file through an interactive prompt loop:

1. **Resolve existing state** — attempt to load any previously saved config from the current working directory via `config.LoadConfig(repoRoot)`. If none exists, all prompts default to built-in constants (Ollama defaults for model ID, base URL, context size; `"wiki"` for docs directory).
2. **Prompt user iteratively** for each setting: LLM Model ID → string, Ollama Base URL → string, Ollama Context Size → integer (parsed via `strconv.Atoi`), custom directories/files to ignore → comma-separated list, file extensions to ignore → comma-separated list, documentation directory → string.
3. **Preserve existing values on empty input** — if the user presses Enter without typing anything, the previously loaded (or default) value is kept.
4. **Support reset semantics** — for list-type inputs, the literal strings `"clear"` or `"none"` wipe the list entirely; otherwise comma-splitting produces the new list.
5. **Error swallowing on read failures** — if stdin reading fails during any prompt (`bufio.NewReader(os.Stdin)` + `reader.ReadString('\n')`), the error is logged to stderr and execution continues using existing values (no rollback, no restart).
6. **Persist final config** — after all prompts complete, a single `config.Config` struct is constructed from user input + preserved state and written to disk via `config.SaveConfig`. The success message references a `.yaml` file path defined in the config package.

Non-destructive defaults: existing configuration is always preferred over built-in constants; new values only replace what the user explicitly provided (or left as default). Idempotent re-run: running `setup` again when a config already exists produces an incremental update rather than a full overwrite. No retry logic — `SaveConfig` is called once; if it fails, the entire flow aborts.

### Update Subcommand Registration (`update.go`)

This file exports no public functions, structs, or interfaces. The handler registers the CLI entry point via `init()` (package-level) and delegates to `executeCommand("update")`, which resolves the actual update logic outside this package boundary. All substantive logic is deferred to the external function; this file serves only as registration/wiring within the cobra command hierarchy.

### Init Subcommand Registration (`init.go`)

This file exports no public functions, structs, or interfaces. The handler registers the CLI entry point via `init()` (package-level) and delegates to `executeCommand("init")`, which performs the repository scan and wiki page generation outside this package boundary. All substantive logic is deferred to the external function; this file serves only as registration/wiring within the cobra command hierarchy.

---

## Package: internal/config — Multi-Layer Configuration Resolution

### Responsibility

Implements a four-tier precedence pipeline for resolving all configurable values. Each layer overwrites prior values when present; missing layers are transparently skipped. Three properties are resolved independently (`ModelID`, `OllamaBaseURL`, `OllamaNumCtx`). The package also structures the analysis pipeline into named **extraction steps** (`ExtractionStep`), each carrying a distinct prompt template. All non-structural state (resolved config, local variables) is ephemeral — scoped to function calls and returned as values. No package-level mutable state survives beyond initialization. Concurrency primitives are absent; the package is single-threaded in design.

### Types

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

#### `ExtractionStep`

```go
type ExtractionStep struct {
    Name   string // step identifier (e.g., "api-signatures", "business-logic")
    Prompt string // prompt template applied to this phase during analysis
}
```

Instances are held in a package-level slice (`DefaultExtractionSteps`). The slice is immutable after declaration; callers receive copies or slices of it.

### Constants and Defaults

| Constant | Value | Purpose |
|---|---|---|
| `CodeReducerModelIdEnvKey` | `"CODE_REDUCER_MODEL_ID"` | Overrides `Config.ModelID` when set. |
| `OllamaBaseUrlEnvKey` | `"OLLAMA_BASE_URL"` | Overrides `Config.OllamaBaseURL` when set. |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | Overrides `Config.OllamaNumCtx`; value must parse as a positive integer. |
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | Fallback base URL when no prior layer supplies one. |
| `OllamaDefaultModelID` | `"ornith:9b"` | Default model identifier. |
| `OllamaDefaultNumCtx` | `8192` | Default context size. |
| `ConfigFileName` | `.code-reducer.yaml` | Filename resolved relative to the current working directory. |

Three slices are declared at package init and never mutated:

```go
var DefaultIgnores           []string // system-supplied ignore paths
var DefaultIgnoredExtensions []string // system-supplied ignored extensions
var DefaultExtractionSteps   []ExtractionStep // ordered extraction phases
```

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

Top-level entry point for full configuration resolution:
1. Calls `LoadConfig` to read the YAML file. If the error is a non-existent-file sentinel (`os.IsNotExist(err)`), it swallows and initializes an empty `Config{}`; all other errors propagate wrapped as `"failed to load configuration file:"`.
2. Layers defaults (hard-coded constants) over the loaded config.
3. Layers environment variable overrides using `os.Getenv` for `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, and `OLLAMA_NUM_CTX`. Non-empty values are used; empty strings fall through. Env read errors are swallowed.
4. Applies CLI flags (`modelIdFlag`, `numCtxFlag`). The flag value is parsed via `strconv.Atoi`; only `err == nil && n > 0` is accepted — invalid or non-positive values cause the prior layer's value to be retained silently.

Returns a fully resolved `*Config`. Errors are either pass-through (from `LoadConfig`) or wrapped with `"failed to load configuration file:"`. Notable: if YAML is malformed, the parse error wraps and propagates; otherwise resolution always succeeds for missing files.

#### Configuration Persistence

##### `SaveConfig(cwd string, cfg *Config) error`

Serializes the config struct back to disk using `os.WriteFile` with `0600` permissions. Error handling:
- Write success → returns `nil`.
- Parse failure (marshal YAML) → wrapped as `"failed to marshal yaml: %w"`.
- File-write failure → wrapped as `"failed to write config file: %w"`.

Writes directly (no temp-file + rename); a partial write can leave a truncated file on disk. Permission normalization is implicit — existing files with different permissions will fail the write.

---

## Package: internal/engine — Code Documentation Pipeline Engine

### Responsibility

Implements a Map-Reduce pipeline that generates and maintains technical documentation for a Go codebase using Ollama-compatible LLM inference endpoints. The engine produces three artifacts per run: `architecture.md` (global overview), `quickstart.md` (onboarding guide), and per-module API summaries under `modules/`. Execution is gated by a repository-level lock to prevent concurrent runs against the same root, and state persistence across invocations is handled by an on-disk metadata cache.

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

The `cfg *config.Config` field is read-only within this file; all mutations to the runner's configuration originate before construction. The lock returned by `security.AcquireLock(repoRoot)` is a local variable with deferred cleanup — its internal type and mutual exclusion semantics are not observable from this source alone.

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

### LLM Client Abstraction & Response Parsing

#### Type: `LLMClient`

```go
type LLMMClient struct {
    ModelID   string
    BaseURL   string
    NumCtx    int
    HTTPClient *http.Client  // default transport, no custom config (no proxy/redirect/TLS)
}
```

Constructed via `NewLLMClient(modelID, baseURL, numCtx)` with a 10-minute HTTP timeout. Fields set once; never reassigned within any method.

#### Methods on `*LLMClient`

##### `CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (string, error)`

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

##### `GetBaseSystemPrompt() string`

Returns the fixed base role definition and defensive guidelines inherited by every LLM call regardless of task type.

##### `GetDefaultSystemPrompt(command string) string`

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

### Synthesis Engine & Chunking (`chunking.go`, `synthesize.go`)

#### Context-Aware Chunking (all unexported)

The system processes extracted code/fact items in batches constrained by LLM context limits (`NumCtx * 3`). Three distinct processing modes exist:

##### Module Synthesis (`reduceInChunks`)

Synthesizes architectural-level nodes into architecture descriptions using a `"module_synthesis"` system prompt. Each batch represents evidence contributing to understanding the module's design intent. Response passes through `StripOuterMarkdownFence`. Errors wrapped with `"LLM error during synthesis: %w"`.

##### File Fact Consolidation (`reduceFileFacts`)

Consolidates extracted facts about a specific step within a file with deduplication and merging. System prompt built locally by concatenating `c.GetBaseSystemPrompt()` + fixed role string. Response also passes through `StripOuterMarkdownFence`. Errors wrapped with `"LLM error during file fact consolidation: %w"`.

##### Recursive Convergence (`reduceItems`)

Core business rule: reduce items in batches → collect intermediate results → recursively apply reduction until everything converges into a single output string. Multi-pass approach ensures information is progressively synthesized rather than lost across multiple LLM calls. Termination guaranteed when all data fits within one batch (short-circuits at top with `len(batches) == 1`). Recursion terminates because:
1. Single-batch case short-circuits at top (`len(batches) == 1`)
2. Recursive step processes each batch through itself again with same `maxChars`

**Graceful Truncation Policy:** When content exceeds the context window, single oversized items get truncated with `\n...[truncated]` marker appended; when multiple items exceed limits collectively, each gets capped at `maxChars / len(items)` characters before being processed as one batch. Infinite-loop guard: if batch size equals item count for all items (can't merge), falls back to per-item truncation using `allowedPerItem = maxChars / len(items)`. Still silent — no error returned.

##### Overlapping Chunk Support (`chunkTextWithOverlap`)

Utility for splitting raw text into overlapping chunks to maintain context continuity between adjacent segments — useful when feeding source material directly into LLMs without pre-processing through the chunking logic. No I/O. Silent swallow: `maxRunes <= 0` → returns full text as single chunk; `overlapRunes >= maxRunes` → clamps to half without warning; final partial chunk reaching end of runes → breaks loop with no error or truncation marker (unlike `reduceItems`).

#### Hierarchical Module Synthesis (`synthesizeNode`, all unexported)

Performs recursive synthesis of directory structures into hierarchical summaries — transforming raw source code and subdirectory metadata into consolidated documentation at each level of a module tree.

**Algorithmic Flow:**
1. **Context Check → Cache Reuse Gate**: If current node not in `affectedDirs` AND cached module summary exists, return immediately without processing. Short-circuits unaffected directories.
2. **Recursive Child Synthesis (Bottom-Up)**: Sort children alphabetically, recursively call `synthesizeNode` on each child directory; results collected into map keyed by child name.
3. **File Processing Loop**: For each file in current node: hash-first cache lookup (if precalculated hash exists and matches cached SHA256 → reuse cached facts without reading disk); read + hash fallback otherwise (read via `tools.ReadFileSafely`, compute SHA-256, update cache entry if already existed with that hash); extraction pipeline on cache miss (chunk content using dynamic limit derived from `c.NumCtx × 4