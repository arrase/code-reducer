# Architecture Overview — Code-Reducer

## System Boundaries & Module Interaction Map

```
┌─────────────────┐     ┌──────────────────┐     ┌──────────────────────┐
│   cmd (CLI)      │     │ internal/engine   │     │ internal/config       │
│                  │     │ (Pipeline Engine) │     │ (Config Resolution)    │
│  • root.go       │     │                  │     │                       │
│  • setup.go      │────▶│ • runner.go       │◀───│ • ResolveConfig        │
│  • init.go       │     │ • orchestrator   │     │ • LoadConfig           │
│  • update.go     │     │ • tree.go        │     │ • SaveConfig           │
│                  │     │ • chunking.go    │     │ • ExtractionSteps      │
│                  │     │ • synthesize.go  │     │ • Config               │
└─────────────────┘     └──────────────────┘     └──────────────────────┘
                              │
                              ▼
                     ┌──────────────────┐
                     │ LLM Endpoint     │
                     │ (Ollama-compat)  │
                     │ /api/chat        │
                     └──────────────────┘
```

**Execution flow:** `cmd.RootCmd.Execute()` → config resolution (`internal/config.ResolveConfig`) → engine dispatch (`engine.Runner.Run` with `"init"` or `"update"`) → LLM calls through `LLMClient`. All three packages are decoupled at boundaries: the CLI package wires subcommands and suppresses cobra defaults; the config package resolves values through a four-tier precedence pipeline; the engine package owns the map-reduce pipeline, tree construction, chunking, and synthesis.

---

## Module Responsibility & Data Flow

### `cmd` — Entry Point & Subcommand Wiring

The CLI layer registers three subcommands (`init`, `update`, `setup`) via package-level `init()` calls. Two persistent flags are bound globally: `--model-id` (via `PersistentFlags` + `modelIdFlag`) and `--num-ctx`. The root command performs environment validation, config resolution, implicit setup on first run when stdin is a TTY, mode-gated lifecycle checks delegated to the runner/engine layer, signal handling via `signal.NotifyContext`, and dispatches to `engine.NewRunner.Run()`.

All failure paths return errors propagated through cobra. Cobra's default error printing is suppressed (`SilenceErrors: true`, `SilenceUsage: true`), so callers handle errors explicitly. No panic calls are observed in this package.

**Init subcommand** registers the entry point and delegates to `executeCommand("init")`, which performs repository scan and wiki page generation outside this package boundary. All substantive logic is deferred externally.

**Update subcommand** similarly exports no public functions; it registers via `init()` and delegates to `executeCommand("update")`. The actual update logic lives outside this file.

**Setup flow** (`RunSetupFlow(repoRoot)`) generates `.code-reducer.yaml` through an interactive prompt loop: resolve existing state, prompt iteratively for each setting (model ID → string, base URL → string, context size → integer parsed via `strconv.Atoi`, custom directories/files to ignore → comma-separated list, file extensions to ignore → comma-separated list, documentation directory → string), preserve existing values on empty input, support reset semantics where `"clear"` or `"none"` wipes the list entirely, swallow read failures silently (logged to stderr, continue with existing values), and persist final config via `config.SaveConfig`.

### `internal/config` — Configuration Resolution

Implements a four-tier precedence pipeline. Each layer overwrites prior values when present; missing layers are transparently skipped. Three properties resolve independently: `ModelID`, `OllamaBaseURL`, `OllamaNumCtx`. The package also structures the analysis pipeline into named **extraction steps** (`ExtractionStep`), each carrying a distinct prompt template. All non-structural state (resolved config, local variables) is ephemeral — scoped to function calls and returned as values. No package-level mutable state survives beyond initialization. Concurrency primitives are absent; the package is single-threaded in design.

#### Configuration Loading (`LoadConfig`)

Reads YAML at the resolved path. File-read failure → pass-through error. YAML parse failure → wrapped as `"failed to parse yaml config: %w"`. Returns non-nil `*Config` on success, initialized with zero values before parsing so callers receive a populated object even for empty mappings.

#### Configuration Resolution (`ResolveConfig`)

Top-level entry point: (1) calls `LoadConfig`, swallows non-existent-file sentinel (`os.IsNotExist(err)`), initializes empty `Config{}`, other errors propagate wrapped as `"failed to load configuration file:"`; (2) layers defaults (hard-coded constants); (3) layers environment variable overrides using `os.Getenv` for `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX` — non-empty values used, empty strings fall through; env read errors swallowed; (4) applies CLI flags (`modelIdFlag`, `numCtxFlag`) — flag value parsed via `strconv.Atoi`; only `err == nil && n > 0` accepted, invalid or non-positive retain prior layer's value silently.

#### Configuration Persistence (`SaveConfig`)

Serializes config struct back to disk using `os.WriteFile` with `0600` permissions. Write success → `nil`. Parse failure (marshal YAML) → wrapped as `"failed to marshal yaml: %w"`. File-write failure → wrapped as `"failed to write config file: %w"`. Writes directly, no temp-file + rename; partial writes can leave a truncated file on disk.

#### Defaults & Constants

| Constant | Value | Purpose |
|---|---|---|
| `CodeReducerModelIdEnvKey` | `"CODE_REDUCER_MODEL_ID"` | Overrides `Config.ModelID` when set |
| `OllamaBaseUrlEnvKey` | `"OLLAMA_BASE_URL"` | Overrides `Config.OllamaBaseURL` when set |
| `OllamaNumCtxEnvKey` | `"OLLAMA_NUM_CTX"` | Overrides `Config.OllamaNumCtx`; must parse as positive integer |
| `OllamaDefaultBaseURL` | `"http://localhost:11434"` | Fallback base URL |
| `OllamaDefaultModelID` | `"ornith:9b"` | Default model identifier |
| `OllamaDefaultNumCtx` | `8192` | Default context size |
| `ConfigFileName` | `.code-reducer.yaml` | Filename relative to CWD |

Three slices declared at package init and never mutated: `DefaultIgnores`, `DefaultIgnoredExtensions`, `DefaultExtractionSteps`.

#### Path Resolution & Existence Check

**`getConfigPath(cwd string) (string, error)`** — constructs full path to `.code-reducer.yaml` by joining `cwd` with `ConfigFileName`. Returns non-nil error only on I/O failure during construction; normal cases return `nil`.

**`ConfigExists(cwd string) bool`** — returns whether config file exists at resolved path. Internally calls `os.Stat`; all errors (permission denied, I/O failures, etc.) are swallowed and treated as "does not exist". Returns no error.

### `internal/engine` — Code Documentation Pipeline Engine

Implements a Map-Reduce pipeline that generates and maintains technical documentation for a Go codebase using Ollama-compatible LLM inference endpoints. The engine produces three artifacts per run: `architecture.md` (global overview), `quickstart.md` (onboarding guide), and per-module API summaries under `modules/`. Execution is gated by a repository-level lock to prevent concurrent runs against the same root; state persistence across invocations is handled by an on-disk metadata cache.

#### Pipeline Orchestration — Runner Entry Point (`runner.go`)

The public entry point for all pipeline invocations: repository isolation, concurrency guard acquisition via `security.AcquireLock(repoRoot)`, LLM client construction from config, and mode-gated dispatch to the appropriate pipeline method.

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

#### Orchestrator Pipelines (`orchestrator.go`)

**Full Run (`RunInit`)** executes an all-or-nothing regeneration regardless of cache state: (1) setup — load `.gitignore` via `tools.LoadGitignore(repoRoot)`; merge with user ignore list; load metadata cache from disk via `cache.loadMetadataCache()`. Failures logged as warnings, not returned. (2) map phase — discover code files via `tools.DiscoverCodeFiles(...)`, compute SHA-256 per file via `computeSHA256(repoRoot, f)`, build directory tree via `buildTree(codeFiles)`. Hash computation errors silently skipped in loops; only stored if hash computed successfully. (3) mark all affected — recursively flag every node as affected (init starts fresh). (4) reduce phase — call `synthesizeNode` bottom-up on the tree to produce a root summary string and per-module summaries via LLM calls through `c.CallLLM`. (5) generate standard docs — produce `architecture.md` and `quickstart.md` from the root summary via two additional `CallLLM` invocations: one for architecture, one for quickstart. Writes via `tools.WriteFileSafely`; errors wrapped with context (e.g., `"failed to write quickstart.md: %w"`). (6) update AGENTS.md — read existing file via `tools.ReadFileSafely(repoRoot, "AGENTS.md")`. If missing, create with guidelines header. If present but does not contain "AI Agent Guidelines", append separator + new content block. Only writes when content actually differs. Errors: if read fails but write succeeds → `"failed to write AGENTS.md: %w"`; if both fail → wrapped errors chain. (7) teardown — persist metadata cache via `saveMetadataCache(repoRoot, docsDir, cache)`. Failure logged as warning, function always returns nil at end regardless of this failure.

**Incremental Run (`RunUpdate`)** executes only re-generations for directories whose contents actually changed: (1) setup — load pipeline state from metadata cache (same path as init). (2) map phase — discover code files, hash each via `computeSHA256`, build tree. (3) change detection — compare current file hashes against cached hashes in `cache.Files`: new file absent from cache → `FileChange{Status: "Added"}`; existing file with different hash → `FileChange{Status: "Modified"}`; cached file no longer in codebase → `FileChange{Status: "Deleted"`} (also removed from cache). (4) prune stale modules — remove any module summaries whose directory is no longer active, delete their markdown files on disk via `os.Remove(absModuleFile)` (error assigned to `_`, never logged). (5) propagate affected — run tree-aware propagation (`propagateAffected`/`determineAffected`) to mark directories affected by changes. Errors from these calls are discarded without capture. (6) early exit — if zero directories are affected, pipeline ends with "up to date" status, no further synthesis occurs. (7) conditional reduce — if root-level change detected or global docs don't exist on disk (`os.Stat(absArch)`/`os.Stat(absQs)`), run full synthesis; otherwise skip and reuse existing `architecture.md`/`quickstart.md`. (8) teardown — save updated cache via `saveMetadataCache(repoRoot, docsDir, cache)`.

#### Directory Tree Construction & Affected Propagation (`tree.go`)

**Type Definitions:**

| Type | Fields | Purpose |
|------|--------|---------|
| `FileChange` | `Path string`, `Status string` | Models a file operation with path and status (`Added`, `Modified`, `Deleted`). Consumed by `determineAffected`. |
| `DirNode` | `Path string`, `Files []string`, `Children map[string]*DirNode` | Hierarchical tree node holding files at that level and child directories in its subtree. Produced by `buildTree`. |

**Functions (all unexported):**

- **`buildTree(codeFiles)`** — converts a flat list of file paths into nested `*DirNode` structure, preserving directory nesting by splitting on `/`. Appends to `DirNode.Files`; populates `DirNode.Children` via map assignment.
- **`propagateAffected(tree, changeset)`** — traverses the tree recursively: if any direct child of the current node is in the changeset, mark it affected. Accumulates affected status upward through ancestors. Returns mutated `affectedDirs`.
- **`determineAffected(tree, changeset)`** — computes module path for each directory via `ToSafeMarkdownFilename`, checks existence on disk via `os.Stat(absModulePath)`, reads metadata cache (`cache.Modules[n.Path] == ""`), marks node affected if any check fails. Only `os.IsNotExist` is acted on; all other errors are silently dropped.

#### LLM Client Abstraction & Response Parsing

**Type: `LLMClient`**

```go
type LLMMClient struct {
    ModelID   string
    BaseURL   string
    NumCtx    int
    HTTPClient *http.Client  // default transport, no custom config (no proxy/redirect/TLS)
}
```

Constructed via `NewLLMClient(modelID, baseURL, numCtx)` with a 10-minute HTTP timeout. Fields set once; never reassigned within any method.

**Methods on `*LLMClient`:**

- **`CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (string, error)`** — full lifecycle: prepend system prompt as first message with role `"system"` → serialize into Ollama `/api/chat` JSON payload with model ID and context window size → non-streaming HTTP POST → response body fully read on 200 OK or truncated at 1 KB via `io.LimitReader(resp.Body, 1024)` on non-OK status. Returns only assistant content string; returns empty string on failure.

**Error behavior:**

| Failure path | Error type | Wrapping strategy |
|---|---|---|
| JSON marshal fails in request prep | `fmt.Errorf("failed to marshal request: %w", err)` | Wrapped with `%w` |
| `http.NewRequestWithContext` fails | raw error from `NewRequestWithContext` | **Not wrapped** — passed through as-is |
| HTTP Do (network-level failure) | raw error from `c.HTTPClient.Do(req)` | **Not wrapped** |
| Body read fails on 200 OK path | raw error from `io.ReadAll` | **Not wrapped** |
| JSON unmarshal fails on response | `fmt.Errorf("failed to parse response: %w", err)` | Wrapped with `%w` |
| Non-OK status (body truncated at 1 KB) | `fmt.Errorf("ollama api error: status %d, response: %s", ...)` | New error constructed; body discarded if read fails |

- **`GetBaseSystemPrompt() string`** — returns the fixed base role definition and defensive guidelines inherited by every LLM call regardless of task type.
- **`GetDefaultSystemPrompt(command string) string`** — appends command-specific instructions based on the command string: two named variants exist (`module_synthesis`, `architecture`); any other value receives only the base layer.

#### Response Parsing Layer (internal, unexported helper `extractBalancedJSON`)

1. Markdown code fences (` ``` `) surrounding JSON must be stripped before parsing — handled by `StripOuterMarkdownFence`.
2. Balanced bracket matching required — strings containing `{`, `[`, `}`, `]` literals (including escaped characters like `\n`) must not break extraction logic — handled by internal rune-level scanner with escape state tracking inside string literals.
3. When multiple balanced JSON candidates exist in a single response, each is attempted individually until one succeeds, or the entire trimmed string is tried as a final fallback — handled by `CleanJSONResponse`/`UnmarshalJSONResponse`.

**Functions:**

| Function | Behavior | Error surface | Swallowed? |
|---|---------|---------------|------------|
| `StripOuterMarkdownFence(s string) string` | Strips surrounding markdown fences; falls back to trimmed input if none match | None — returns string only | N/A (no error return) |
| `CleanJSONResponse(s string) string` | Iterates stripped content, calls `extractBalancedJSON`, returns first successful candidate or original trimmed input unchanged | None — returns string only | Yes — unbalanced JSON inside response is swallowed silently |
| `UnmarshalJSONResponse(s string, target interface{}) error` | Multi-layered attempt: (1) collect balanced candidates, try each into target; keep last error in `lastErr`; (2) if any succeeded, return nil; otherwise try entire trimmed string; (3) terminal errors — `"failed to unmarshal JSON candidates: %w"` or `"no valid JSON found in response"` | Explicit (`error`) | Partial parse failures collected and re-emitted via `%w` on first path; "no valid JSON" covers second path |

#### Synthesis Engine & Chunking (internal, all unexported)

The system processes extracted code/fact items in batches constrained by LLM context limits (`NumCtx * 3`). Three distinct processing modes exist:

**Module Synthesis (`reduceInChunks`)** — synthesizes architectural-level nodes into architecture descriptions using a `"module_synthesis"` system prompt. Each batch represents evidence contributing to understanding the module's design intent. Response passes through `StripOuterMarkdownFence`. Errors wrapped with `"LLM error during synthesis: %w"`.

**File Fact Consolidation (`reduceFileFacts`)** — consolidates extracted facts about a specific step within a file with deduplication and merging. System prompt built locally by concatenating `c.GetBaseSystemPrompt()` + fixed role string. Response passes through `StripOuterMarkdownFence`. Errors wrapped with `"LLM error during file fact consolidation: %w"`.

**Recursive Convergence (`reduceItems`)** — core business rule: reduce items in batches → collect intermediate results → recursively apply reduction until everything converges into a single output string. Multi-pass approach ensures information is progressively synthesized rather than lost across multiple LLM calls. Termination guaranteed when all data fits within one batch (short-circuits at top with `len(batches) == 1`). Recursion terminates because: (1) single-batch case short-circuits at top (`len(batches) == 1`); (2) recursive step processes each batch through itself again with same `maxChars`.

**Graceful Truncation Policy:** when content exceeds the context window, single oversized items get truncated with `\n...[truncated]` marker appended; when multiple items exceed limits collectively, each gets capped at `maxChars / len(items)` characters before being processed as one batch. Infinite-loop guard: if batch size equals item count for all items (can't merge), falls back to per-item truncation using `allowedPerItem = maxChars / len(items)`. Still silent — no error returned.

**Overlapping Chunk Support (`chunkTextWithOverlap`)** — utility for splitting raw text into overlapping chunks to maintain context continuity between adjacent segments — useful when feeding source material directly into LLMs without pre-processing through the chunking logic. No I/O. Silent swallow: `maxRunes <= 0` → returns full text as single chunk; `overlapRunes >= maxRunes` → clamps to half without warning; final partial chunk reaching end of runes → breaks loop with no error or truncation marker (unlike `reduceItems`).

#### Hierarchical Module Synthesis (`synthesizeNode`, all unexported)

Performs recursive synthesis of directory structures into hierarchical summaries — transforming raw source code and subdirectory metadata into consolidated documentation at each level of a module tree.

**Algorithmic Flow:** (1) context check → cache reuse gate: if current node not in `affectedDirs` AND cached module summary exists, return immediately without processing. Short-circuits unaffected directories. (2) recursive child synthesis (bottom-up): sort children alphabetically, recursively call `synthesizeNode` on each child directory; results collected into map keyed by child name. (3) file processing loop: for each file in current node — hash-first cache lookup (if precalculated hash exists and matches cached SHA-256 → reuse cached facts without reading disk); read + hash fallback otherwise (read via `tools.ReadFileSafely`, compute SHA-256, update cache entry if already existed with that hash); extraction pipeline on cache miss (chunk content using dynamic limit derived from `c.NumCtx × 4`...).