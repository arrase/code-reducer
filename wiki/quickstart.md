# Code-Reducer — Architecture & Quickstart

## System Boundary

Code-Reducer is an automatic software-documentation generation system driven by Ollama-compatible large language models (LLMs). It operates on a repository's source code tree to produce markdown artifacts describing the project's architecture, module boundaries, business logic, state management, error handling patterns, and agent guidelines.

**Two operational modes:**
- **`init`** — generates documentation from scratch against the full discovered codebase
- **`update`** — performs incremental regeneration based on detected file changes

**Entry point:** `internal/engine/runner.go` → `Runner.Run(ctx, repoRoot, mode, onEvent)`

**I/O routing:** All disk I/O flows through `internal/tools`; all network calls go through the LLM client in `internal/engine/client.go`; concurrency protection is provided by OS-level file locking managed by `security.AcquireLock`.

---

## Module Interaction Map

```
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│  config      │────▶│   security   │────▶│    tools     │
│              │     │             │     │               │
│ LoadConfig   │     │ SafeResolve │     │ DiscoverCode  │
│ ResolveConfig│     │ AcquireLock │     │ ReadFileSafely│
│ SaveConfig   │     │ Unlock      │     │ WriteFileSafely│
└─────────────┘     └─────────────┘     └──────────────┘
       ▲                   ▲                 ▲
       │                   │                 │
       ▼                   ▼                 ▼
┌─────────────┐     ┌─────────────┐     ┌─────────────┐
│   engine     │◀────│  orchestrator│     │ response    │
│              │     │             │     │ parsing      │
│ client       │     │ RunInit/RunUpdate│  │ json_parser │
│ chunking     │     │ SynthesizeNode│    └─────────────┘
│ synthesize   │     └─────────────┘
└─────────────┘
```

**Flow summary:** `config` resolves runtime parameters → `security` enforces path boundaries and mutual exclusion → `tools` discovers code files and performs safe I/O → `engine` orchestrates LLM-driven synthesis → `orchestrator` coordinates the full pipeline.

---

## Configuration Resolution (`config`)

All configuration state is centralized through a four-tier priority chain: CLI flags → environment variables → YAML file (`.code-reducer.yaml`) → hardcoded defaults.

**Persistence:** Config lives at `<cwd>/.code-reducer.yaml` with `0600` owner-only permissions.

**Resolution pipeline (`ResolveConfig`):**
1. Call `LoadConfig` to read persisted state (errors swallowed — falls back to empty struct)
2. Apply CLI flag overrides in order: `modelIdFlag` → `OllamaBaseURL`, then `numCtxFlag` → `OllamaNumCtx`
3. Merge default ignore lists (`DefaultIgnores`, `DefaultIgnoredExtensions`) with user-provided values via `MergeAndDeduplicate` — ordering ensures user values take precedence when duplicates exist across the merge boundary
4. Fall back to hardcoded defaults for extraction steps if slice is empty
5. Return pointer to final resolved config; any failure path results in an empty `&Config{}`

**Four extraction steps defined by default:**

| Priority | Step | Purpose |
|---|---|---|
| 1 | API_SIGNATURES | Extract exported structs, interfaces, methods and their types |
| 2 | BUSINESS_LOGIC | Analyze domain concepts and high-level algorithmic flow |
| 3 | STATE_AND_CONCURRENCY | Identify mutable state and concurrency protections (mutexes, channels) |
| 4 | ERRORS_AND_SIDE_EFFECTS | Document external communication (I/O) and error handling patterns |

---

## Path Boundary Enforcement (`security`)

Three infrastructure contracts: path-boundary enforcement against directory-traversal attacks, process mutual exclusion via file locking, and lockfile version-control exclusion through `.gitignore` management. No synchronization primitives — concurrency safety is delegated to OS-level `O_EXCL` semantics.

**`SafeResolve(repoRoot, inputPath string) (string, error)`**
- Resolves any user-supplied path strictly inside the repository root
- If resolved absolute path escapes the root (detected via `..` prefix check), operation fails with explicit violation error
- Pure path manipulation: `filepath.Abs`, `filepath.Clean`, `filepath.Rel`. No actual disk read/write.

**`AcquireLock(repoRoot string) (*SimpleLock, error)`**
- Acquires a simple file lock in the repo root atomically (via `O_CREATE|O_EXCL`)
- If it already exists, acquisition fails with "lock held" sentinel message
- On success writes PID (newline-terminated). If PID write subsequently fails, cleanup calls happen explicitly so another process can retry.

**`EnsureGitignoreHasLockfile(repoRoot string) error`**
- Ensures `.code-reducer.lock` is present in `.gitignore`
- If ignore file doesn't exist, one is created with a single line; otherwise scans for existing entry and appends if not found
- Opens for appending with `O_APPEND|O_WRONLY`; no partial state change beyond the open handle on write failure

---

## Repository Discovery & Safe I/O (`tools`)

**Repository code discovery:** Walks `repoRoot` recursively via `filepath.WalkDir`, skipping directories matching `.gitignore` patterns or any dot-prefixed component (`.build`, `.svn`, etc.), then filters each leaf through `ShouldIgnoreFile`. Walk callback errors from `filepath.WalkDir` are swallowed — individual directory entries that fail to stat return `nil` to the caller.

**Safe file read/write operations:**
- `ReadFileSafely(repoRoot, virtualPath) ([]byte, error)` — resolves via `security.SafeResolve`, then reads absolute path via `os.ReadFile`. Errors wrapped with prefix `"failed to read file:"` using `%w`.
- `WriteFileSafely(repoRoot, virtualPath, content []byte) error` — TOCTOU-safe writes: resolve safe absolute path → create parent directories if missing (`0755`) → open for write+create (`O_WRONLY|O_CREATE`, mode `0644`) → call `os.Lstat(safePath)` and `f.Stat()`; compare via `os.SameFile(fiLstat, fiFstat)`. If they differ — indicating a symlink swap or race condition — return an error prefixed `"security violation"` or `"TOCTOU symlink race detected"`. Truncate (`f.Truncate(0)`), then write content.

**Gitignore handling:**
- `LoadGitignore(repoRoot) ([]string, error)` — reads `.gitignore` from `repoRoot`, scanner processes line-by-line. Empty file returns `nil, nil`; non-existence also returns `nil, nil`. All other errors are wrapped. Patterns returned are active non-comment entries.
- `ShouldIgnoreFile(repoRoot, relPath string, gitIgnore *ignore.GitIgnore, ignoredExtensions []string) bool` — applies layered ignore rules: compiles user-supplied patterns via external `github.com/sabhiram/go-gitignore` package; rejects paths with dot-prefixed components or `.egg-info` suffixes; checks file extension against provided blacklist. For unknown extensions, reads the first 1024 bytes and scans for null bytes via `IsBinaryFile`.

**Git subprocess execution:**
- `RunGit(repoRoot string, args ...string) (stdout string, err error)` — constructs an `exec.Command` for `git`, prepends `--no-pager` to user-supplied arguments, sets `cmd.Dir = repoRoot`. Captures stdout into a `bytes.Buffer`, reads it back, trims whitespace. On success returns trimmed output; on failure wraps original error with stderr text: `"git command failed: %v, stderr: %s"`.
- `VerifyGitRepo(repoRoot string) error` — two-stage validation: calls `exec.LookPath("git")`; if unavailable, returns a fresh error without wrapping. Otherwise delegates to `RunGit(repoRoot, "rev-parse", "--is-inside-work-tree")`. If inner call fails, discards its returned error entirely and returns a new message: `"not a git repository (or any of the parent directories)"`.

---

## Documentation Generation Pipeline (`engine`)

**LLM Client (`client.go`):** Provides synchronous and streaming communication with an Ollama-compatible LLM API. The client is immutable after construction; all fields are assigned only in `NewLLMClient`. Both modes share a base system prompt that establishes agent identity and operating rules.

- **`CallLLM(messages []Message) (*Response, error)`** — builds a JSON request body combining the configured model ID, user-supplied messages, and a leading system prompt message. POSTed to `<BaseURL>/api/chat` with `"Content-Type: application/json"`. On success (200), full response body read into memory and parsed once; on non-200 status, up to 1024 bytes of the error body are captured for reporting. HTTP client has a 10-minute timeout applied globally.
- **`StreamLLM(messages []Message, onChunk func(string)) error`** — identical outbound POST but with streaming enabled. Each line from the response is read via `bufio.NewReader`, trimmed, and parsed as JSON. Non-empty content segments invoke caller-supplied `onChunk` callback immediately. Loop exits on EOF or when a chunk's `Done` flag is true. Invalid JSON lines inside the stream are silently skipped — no error returned to caller unless reader itself fails.

**Response Parsing (`json_parser.go`):** Normalizes raw LLM output into parseable JSON before downstream consumption. The parser tolerates markdown code fences (with or without language tags) and handles bracket/brace mismatches within embedded text by walking character-by-character while respecting string boundaries.

- **`StripOuterMarkdownFence(input string) string`** — trims whitespace at entry, then applies a compiled regex to match content between ```` ``` ` (with optional `markdown`/`json` tag) and captures the inner text. No match returns input unchanged.
- **`CleanJSONResponse(input string) (string, error)`** — scans for first `{` or `[`. Walks forward tracking an `inString` flag to skip escaped characters inside quoted strings. Pushes opening delimiters onto a stack; pops on matching close. Returns the substring from start index up to and including closing character once the stack drains. If neither delimiter is found, input passes through unchanged.
- **`UnmarshalJSONResponse(input string) (interface{}, error)`** — cleaned string passed directly to `encoding/json.Unmarshal`. Errors wrapped with context: `failed to unmarshal JSON (cleaned: %q): <original>`.

**Chunk Reduction (`chunking.go`):** Recursive chunk-reduction algorithm that processes collections of code/architecture items through an LLM-based synthesis pipeline and produces a single consolidated output. Items are grouped into batches bounded by `maxChunkChars` (3× the LLM context window), then each batch is synthesized independently, and intermediate results are concatenated and reduced again until one item remains — tree-like reduction pattern.

- **`ReduceInChunks(items []string) (string, error)`** — Algorithmic flow:
  1. Empty input → returns `("", nil)` immediately
  2. Single-item truncation — when exactly one item exceeds the context window, it is mutated in-place to `<items[0][:maxChunkChars]>\n...[truncated]>`. No error returned; processing continues.
  3. Batching phase — items greedily grouped into batches where each batch's combined length stays within `maxChunkChars`. Overflow starts a new batch.
  4. Fallback truncation — if every item exceeds limits individually, each item is proportionally truncated and forced into single batch. Prevents deadlock on unbatchable inputs.
  5. Single-batch terminal — when exactly one batch exists after grouping, entire set sent directly to `CallLLM` for synthesis; result stripped of markdown fences before return.
  6. Multi-batch recursion — each batch recursively reduced via `ReduceInChunks`. Intermediate results concatenated into single list, which then triggers another recursive call until reaching base case where all items fit in one context window.

Error handling: Recursive-call errors and LLM synthesis errors propagate directly. Synthesis errors wrapped with `"LLM error during synthesis: %w"`. Truncation is silent side effect — no warning logged unless it's the only item (in which case `logEvent` invoked).

**Hierarchical Synthesis (`synthesize.go`):** Recursively walks a directory tree (`DirNode`), producing synthesized textual summaries for each node. Each directory's summary built from child summaries plus per-file facts extracted by LLM calls. Unchanged files reuse cached SHA256 hashes and previously extracted facts; changed or new files trigger fresh extraction via configurable `ExtractionSteps`.

- **`SynthesizeNode(node *DirNode, cache MetadataCache) (string, error)`** — Algorithmic flow:
  1. Context check — aborts early if context is cancelled
  2. Cache hit short-circuit — if node not affected (`affectedDirs` map) and cached summary exists, returns immediately without LLM calls
  3. Recursive child synthesis — children sorted; each synthesized in order. Non-empty summaries accumulate into `childSummaries`.
  4. File fact extraction loop — for each file: compute SHA256 via `computeSHA256`; decide cache hit vs. LLM-driven extraction; accumulate facts. Read errors on individual files swallowed (`continue`).
  5. Component assembly — file components followed by child subsystem summaries form ordered slice.
  6. Chunked reduction — all assembled components feed `ReduceInChunks` to produce directory's final summary.
  7. Cache update & persist — result stored in both `MetadataCache.Files[f]` (with new hash and facts) and `MetadataCache.Modules[node.Path]`. Module docs written under `<docsDir>/modules/<safe-filename>.md` via `tools.WriteFileSafely`; write errors wrapped.

Error propagation: LLM call failure during extraction is fatal for the node — wrapped as `"LLM error extracting %s for %s: %w"` and stops all further processing. No partial results returned. SHA256 computation or file read failures on individual files swallowed; other files still contribute components. Result may be incomplete but not an error.

**Directory Tree (`tree.go`):** Defines in-memory `DirNode` hierarchy and implements affected-directory propagation logic that distinguishes between directories containing changed files and unaffected ones, enabling incremental synthesis without re-analyzing unchanged subtrees.

- **`FileChange`** — `Path string`, `Status string` (`"Added" | "Modified" | "Deleted"`). Input primitive for tracking changes across the repository.
- **`DirNode`** — `Path string`, `Files []string`, `Children map[string]*DirNode`. Each node holds its own file list and a map of child subdirectories keyed by name.
- **Affected-Directory Propagation:** Individual directories flagged as affected if any direct file appears in change set, or if no cached module entry exists for path, or if `modules/<safe-filename>.md` does not exist at expected location under `docsDir/modules`. Once individual nodes flagged, ancestors recursively marked affected by walking upward — ensuring parent summaries reflect changes even when they themselves contain no direct file modifications.

Error handling: `SafeResolve` errors in `determineAffected` caught and ignored; entire block skipped silently. `os.Stat(absModulePath)` errors: only `os.IsNotExist(err)` triggers the affected flag; other Stat errors (permission denied, etc.) treated as non-existence and silently ignored.

**Orchestrator (`orchestrator.go`):** Automatic documentation generation pipeline for software repositories. Coordinates code discovery, tree building, hierarchical synthesis, metadata cache operations, standard doc regeneration, AGENTS.md maintenance, and module cleanup. Emits typed `Event` objects (type + message) so callers can observe pipeline progress without parsing log output.

- **RunInit Flow:**
  1. Load `.gitignore`, apply configured ignore lists, scan repository for source-code files
  2. Convert flat file list into hierarchical `DirNode` tree rooted at repo root via `buildTree`
  3. Walk the tree bottom-up; each node summarized by LLM invocation. Root node's summary becomes `rootSum`.
  4. Invoke LLM to write `architecture.md` and `quickstart.md`, passing `rootSum` as context. Errors wrapped with `"failed to generate ...: %w"`.
  5. Append or create "AI Agent Guidelines" section in `AGENTS.md`; if file exists and already contains the marker, skip. Write errors swallowed.
  6. Persist cache via `saveMetadataCache` with a `"local"` commit marker.

- **RunUpdate Flow:**
  1. Re-discover code files using identical filtering logic
  2. Detect changes via SHA256 comparison against cached hashes in `MetadataCache.Files`. Differences classify into `"Added"`, `"Modified"`, or `"Deleted"`.
  3. For every changed file, walk upward from its parent to root directory, marking each ancestor as affected. Deleted files removed from cache immediately; modules whose directories no longer active purged via `os.Remove` (errors ignored).
  4. Rebuild tree and prune cache so it reflects only currently existing code
  5. Propagate affect markers through tree via `propagateAffected`. If no directories are affected, short-circuit with "no modifications" message; return without further LLM calls.
  6. Synthesize only affected nodes via `SynthesizeNode`, skipping unaffected subtrees entirely — reusing cached summaries for them.
  7. Regenerate `architecture.md`/`quickstart.md` if they are missing or any top-level directory was affected; otherwise reuse existing files.
  8. Persist updated cache with `"local"` commit marker.

Cross-cutting rules: **Gitignore stacking** — `.gitignore`, configured ignore lists, and the `docsDir` itself all applied together when discovering code files. **Safe file resolution** — all paths resolved through `security.SafeResolve` before any filesystem operation. **Atomic writes** — documentation written via `tools.WriteFileSafely`. **Module cleanup** — modules whose directories no longer appear in active set removed from disk; removal errors ignored (`_ = os.Remove`).

---

## Runner & Utilities

**Runner (`runner.go`):** Orchestrator for code analysis or documentation pipelines. Manages lifecycle execution from initialization through completion, handling state transitions based on provided mode string. Ensures repository integrity by verifying lockfile is excluded from version control before executing pipeline logic.

- **`NewRunner(cfg *config.Config) *Runner`** — factory for creating new runner instance
- **`Run(ctx context.Context, repoRoot string, mode string, onEvent func(Event)) error`** — main entry point. Acquires exclusive lock on repository; if acquisition fails, returns immediately with wrapped error. Deferred unlock releases the lock unconditionally regardless of how function exits.

Mode dispatch: `"init"` → calls `client.RunInit(ctx, repoRoot, r.cfg, onEvent)`; `"update"` → calls `client.RunUpdate(ctx, repoRoot, r.cfg, onEvent)`; any other value → returns `fmt.Errorf("unsupported mode: %s", mode)` as fatal error.

Error handling summary:

| Step | Behavior on Failure |
|---|---|
| `EnsureGitignoreHasLockfile` | Swallowed; logged as warning via `onEvent`. Execution continues. |
| `AcquireLock` | Fatal, wrapped: `"failed to acquire repository lock: %w"`. Caller sees original error wrapped. |
| `client.RunInit` / `RunUpdate` | Propagated raw without wrapping. Deferred lock cleanup still runs. |
| Unsupported mode | Fatal, wrapped: `"unsupported mode: %s"`. |

**Utilities (`utils.go`):** Maps internal identifiers (module paths) onto safe, human-readable filenames for markdown output. Operates at boundary between engine and documentation generation system.

- **`ToSafeMarkdownFilename(modulePath string) string`**:
  1. All filesystem separator characters replaced with underscores (`_`). Normalizes platform-specific path separators into single ASCII character.
  2. If resulting string empty or equals `"."`, replaced with literal string `"root"`.
  3. `.md` suffix appended, signaling output is a markdown document file.

Mapping deterministic and pure — same input always yields same filename. No error return for invalid inputs. All transformations best-effort with silent normalization.