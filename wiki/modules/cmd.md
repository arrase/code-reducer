# Module: `cmd` — CLI Command Registration, Lifecycle Management, and Interactive Configuration

## Responsibility

The `cmd` package implements the command-layer for the **Code-Reducer** documentation agent. It registers Cobra subcommands (`code-reducer init`, `code-reducer update`, `code-reducer setup`) and delegates execution of each to a shared `executeCommand` handler whose implementation resides outside this package boundary. The package also owns:

- Root command construction with environment validation, signal handling, and event-driven output streaming.
- A lifecycle state machine that enforces ordering constraints between subcommands (init before update; setup as prerequisite for run).
- Interactive configuration prompts for first-time or reconfiguration scenarios.

No exported types, variables, or functions are defined in this package. All identifiers have lowercase first letters and remain internal to `cmd`.

---

## Command Registration Layer (`cmd/init.go`, `cmd/update.go`)

Both files exist solely as command wiring stubs. Each declares a local `*cobra.Command` variable (unexported) with its `RunE` closure delegating to an external `executeCommand` function:

| Identifier | Kind | Behavior |
|---|---|---|
| `initCmd` (`cmd/init.go`) | `*cobra.Command` | Registered under `RootCmd`; `RunE` invokes `executeCommand("init")`. No state mutation after declaration. |
| `updateCmd` (`cmd/update.go`) | `*cobra.Command` | Registered under `RootCmd`; `RunE` invokes `executeCommand("update")`. No state mutation after declaration. |

The domain concept for the init flow is **project initialization** — scanning a repository to generate initial wiki documentation (markdown pages). The update flow targets files changed since the last documented commit and updates corresponding wiki pages. Neither file contains business logic; both pass execution to `executeCommand` whose error handling strategy cannot be determined from this source alone because the function is out of scope here.

---

## Root Command & Lifecycle State Machine (`cmd/root.go`)

### Core Domain Concepts

The package-level variable `RootCmd` is a `*cobra.Command` initialized with:

- `Use` = `"code-reducer"`
- `Short`, `Long` descriptions (contents not exposed here)
- `DisableDefaultHelpCommand`
- `CompletionOptions{DisableDefaultCmd: true}`
- `SilenceUsage`, `SilenceErrors` both set to `true`

The root command owns four behavioral responsibilities beyond subcommand registration:

1. **Environment validation** — verifies the current working directory is a valid Git repository via an external `tools.VerifyGitRepo(repoRoot)` call; failure is hard (no soft fail).
2. **Configuration resolution** — merges three sources in order of precedence: persisted config file, CLI flag overrides (`--model-id`, `--num-ctx`), then defaults. The merged result drives all downstream behavior.
3. **Lifecycle state machine** — enforces ordering constraints between subcommands (init requires project NOT yet initialized; update requires project IS already initialized). Invalid transitions fail with explicit sentinel messages.
4. **Graceful termination** — installs signal handlers for `SIGINT` and `syscall.SIGTERM`. A context is created via `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` whose returned `ctx` and `stop` function manage lifecycle; deferring `stop()` cancels the context on return.
5. **Event-driven execution** — spawns an engine runner that emits events (status, error, etc.) which are streamed to stdout/stderr in real time rather than collected after completion. The callback passed to `runner.Run` is a side-effect only: it writes events but does not propagate them back as errors. Error state comes from `err != nil` at the end of the run path.

### Mutable State (`cmd/root.go`)

| Variable | Type | Mutated? | Notes |
|---|---|---|---|
| `modelIDFlag` (string) | package-level | Once, during flag registration in `init()` via `StringVar` | Not mutated after declaration. |
| `numCtxFlag` (string) | package-level | Once, during flag registration in `init()` via `StringVar` | Same pattern as above. |

No concurrent access to shared state occurs within this file; no locking mechanism is needed.

---

## Interactive Setup Flow (`cmd/setup.go`)

### Exported Function

| Function | Input(s) | Output |
|---|---|---|
| `RunSetupFlow(repoRoot string)` | `repoRoot string` | `error` |

No exported structs, interfaces, or methods are defined in this file. All other identifiers (`setupCmd`, `promptStringList`, `promptString`) and type references belong to imported packages or are unexported locals.

### Business Rules Resolved by This File

1. **Initial setup / reconfiguration** — supports both first-time configuration (no existing config) and reconfiguration (existing config present). Both paths use the same interactive prompts uniformly.
2. **Configuration persistence with defaults** — every configurable field has a built-in default value (`config.OllamaDefault*`, `config.DefaultDocsDir`, etc.). If no prior configuration exists, these defaults become initial values; if a config already exists, its current values serve as hints and are retained unless changed.
3. **LLM model configuration** — user must specify an LLM model ID and Ollama base URL for code analysis operations. Both fields accept empty input (falling back to existing/default), allowing partial reconfiguration.
4. **Context size with validation** — non-numeric or zero values are rejected, defaulting silently to the existing value rather than failing setup.
5. **Ignore list management** — directories/files/patterns to exclude from analysis can be set as comma-separated entries. Special keywords `clear` and `none` reset the list entirely. Existing ignores persist unless explicitly cleared.
6. **Prompt preservation strategy** — all four prompt-related fields (System Prompt, Module Synthesis Prompt, Architecture Prompt, File Fact Consolidation Prompt) follow a "preserve existing" rule: only updated if user provides non-empty input; otherwise current values remain unchanged.
7. **Atomic save** — after collecting all inputs, the entire configuration is written in one operation (`config.SaveConfig`). No partial writes occur.
8. **Error tolerance during setup** — input read errors and invalid conversions are swallowed with warning messages rather than aborting setup. Existing configuration values serve as safety nets throughout.

### High-Level Algorithmic Flow

1. Load the current working directory; if it fails, propagate the error immediately (wrapped with `%w`).
2. Attempt to load an existing `.code-reducer.yaml` from that directory via `config.LoadConfig(repoRoot)`. Swallowed error → treated as "no prior config".
3. For each configuration field: display a prompt with the current value in brackets as a hint → collect user input via `reader.ReadString('\n')` on a `bufio.Reader(os.Stdin)` created in this function → validate/convert → fall back to existing/default on failure.
4. After all prompts complete, assemble a new `Config` struct combining any accepted inputs with preserved existing values.
5. Write the assembled config atomically via `config.SaveConfig(repoRoot, newCfg)`. On success, confirm completion; on write error, wrap and return as fatal exit for this flow.

### Mutable State (`cmd/setup.go`)

| Variable | Kind | Mutated After Initialization? | Notes |
|---|---|---|---|
| `setupCmd` (package-level) | `*cobra.Command` | No (assigned once in `init()`) | Internal fields (`Use`, `Short`, `Long`, `RunE`) set during construction. Not reassigned after init. |

No concurrency primitives protect mutable state; no `sync.Mutex`, channels, atomic operations, or other synchronization primitives are present.

---

## Error Handling Strategy

### Pattern 1: Wrap-and-Return (Fatal)

Used for unrecoverable setup failures — propagated up to Cobra's `RunE`:

| Failure Point | Mechanism |
|---|---|
| CWD lookup failure in `setup.go` | `fmt.Errorf("failed to get current working directory: %w", err)` |
| Config save failure | `error saving configuration: %w` with `%w` wrapping |
| Init re-run on initialized project (root.go) | Sentinel string error, no wrapping |

### Pattern 2: Swallow-and-Fallback (Non-fatal)

Used for user-input and transient I/O issues — existing values are preserved:

- `strconv.Atoi(ctxInputStr)` parse error → falls back to `existingNumCtx`
- `promptString()` stdin read error → prints warning to stderr, returns `existingVal`
- `promptStringList()` stdin read error → prints warning to stderr, returns `existingList` unchanged
- `config.LoadConfig()` error → treated as nil config; all fields default to built-in constants

### Pattern 3: Sentinel / Hard-Coded Messages (root.go)

| Condition | Message |
|---|---|
| Init re-run on initialized project | `"the project has already been initialized"` |
| Update without prior initialization | explicit message |
| Missing config for run commands (non-TTY) | soft fail prompting user to run setup first |

### Pattern 4: Event-Driven Output (root.go runner callback)

Errors are communicated via engine events (`engine.EventError`) routed to stderr rather than returned as error. Caller reads status through stdout/stderr stream instead of checking return value alone. The callback is side-effect only and does not propagate events back as errors.

### Pattern 5: Context Cancellation (signal handler + `defer stop()`)

Graceful shutdown on SIGINT/SIGTERM; runner receives cancelled context which the engine (outside this file) likely translates into an error return. If setup flow fails before reaching signal handler registration, cleanup cost is minimal. No panic observed in any of these files.

---

## Concurrency & Signal Handling

| Concern | Mechanism | Scope |
|---|---|---|
| Cooperative cancellation | `signal.NotifyContext` with `SIGINT`, `syscall.SIGTERM`; returned `ctx` and `stop()` manage lifecycle; deferring `stop()` cancels on return. | `cmd/root.go` |
| Signal-driven context propagation | Engine runner receives cancelled context which is translated into error by downstream code outside this package. | Cross-package boundary |

No mutexes (`sync.Mutex`, `sync.RWMutex`) are used in any of these files. No channels or other synchronization primitives appear directly here. Only local/flag variables are mutated once at startup; no concurrent access to shared state occurs within these files, so no locking mechanism is needed.