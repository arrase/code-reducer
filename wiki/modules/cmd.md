# Architecture: `cmd` Package — CLI Command Wiring & Lifecycle Management

The `cmd` package implements the top-level cobra command hierarchy for Code-Reducer, a documentation-generation agent that writes and maintains project wikis by calling an LLM to produce markdown docs from source code. This module is responsible for registration of subcommands (`init`, `setup`, `update`), resolution of persistent configuration flags, environment validation (Git repo check), signal handling, and delegation to the engine's runner via a shared `executeCommand` function referenced but not defined within this package boundary.

## Data Flow

```
User CLI invocation
  → Cobra parses subcommand
    → cmd/init.go   : executeCommand("init")      → repository scan + wiki scaffolding
    → cmd/setup.go  : RunSetupFlow(repoRoot)       → interactive config prompts → disk write
    → cmd/update.go : executeCommand("update")     → incremental wiki regeneration
```

All three subcommand handlers route execution through a single function reference (`executeCommand`) whose implementation is not present in this package. No business logic, validation, or data processing occurs within the registration files themselves; they serve only as wiring between cobra's command framework and the out-of-scope executor.

---

## Root Command Initialization & Persistent Flags

### `RootCmd` (exported)

- Initialized with `Use="code-reducer"`, short/long descriptions, and `CompletionOptions{DisableDefaultCmd: true}`.
- Two persistent flags registered via `PersistentFlags`:
  - `--model-id` — bound to package-level variable `modelIdFlag`
  - `--num-ctx`   — bound to package-level variable `numCtxFlag`

### Initialization Sequence in `root.go`

1. **Environment validation** — `os.Getwd()` resolves the current working directory; failure is wrapped with `%w` and returned immediately: `fmt.Errorf("failed to get current working directory: %w", err)`.
2. **Configuration resolution** — merges three sources: CLI flags (`--model-id`, `--num-ctx`), environment defaults, and an optional persistent config file (loaded via `internal/config`). The resolved config drives all subsequent behavior; details of this merge are delegated outside the package boundary.
3. **Implicit setup on first run** — if the persistent config file does not exist yet *and* stdin is a terminal (`isatty.IsTerminal(os.Stdin.Fd())` / `isatty.IsCygwinTerminal(os.Stdin.Fd())`), an automatic initial configuration step triggers without requiring user intervention. On non-TTY (CI / piping), this step is skipped and the caller must configure manually via `setup`.
4. **Mode-gated lifecycle checks** — enforced only within the runner/engine layer; at registration time, no validation of project state occurs here. The two modes (`init`, `update`) are mutually exclusive on an already-initialized project; enforcement lives in the external executor.
5. **Graceful shutdown** — installs SIGINT / SIGTERM handlers via `signal.NotifyContext`. A deferred `stop()` cancels the context; any in-flight work that respects the cancelled context will exit. No error-return path exists for signal handling within this file.
6. **Execution loop** — delegates to `engine.NewRunner.Run()` with the resolved config and current directory. The runner emits typed events (`status`, `error`, default).

### Side Effects & Error Handling in `root.go`

| Operation | Direction | Mechanism | Notes |
|---|---|---|---|
| `os.Getwd()` | Read OS state | Filesystem call | Wrapped with `%w`; caller handles failure. |
| `isatty.IsTerminal(os.Stdin.Fd())` / `isatty.IsCygwinTerminal(os.Stdin.Fd())` | Read OS state | Stdio file descriptor inspection via third-party library (`go-isatty`) | Used only for the guard check before setup flow; result is not persisted. |
| `fmt.Println(ev.Message)` (2 occurrences) | Write to stdout | Standard library `fmt` package | Triggered inside the callback passed to `runner.Run()` when event type is `"status"` or default case. |
| `fmt.Fprintf(os.Stderr, "Error: %s\n", ev.Message)` | Write to stderr | Standard library `fmt` package | Triggered inside the callback when event type is `"error"`. |

**Cobra output suppression:** `RootCmd` has `SilenceErrors: true` and `SilenceUsage: true`. Errors that would otherwise be printed by cobra's default handler are suppressed — callers must handle errors explicitly (which this function does). No panic calls observed; all failure paths return errors propagated to cobra.

### Mutable State & Concurrency in `root.go`

| Variable | Scope | Mutability | Notes |
|---|---|---|---|
| `modelIdFlag` | Package-level (`var`) | Mutated once via `StringVar` in `init()` | Set by flag binding; value consumed in `executeCommand()` without further writes. |
| `numCtxFlag` | Package-level (`var`) | Mutated once via `StringVar` in `init()` | Same pattern as above. |

No other fields or structs are mutated within this file after their initial assignment. No mutexes, channels, atomic operations, or other concurrency primitives appear here; the signal notification context (`signal.NotifyContext`) is used for cancellation but does not protect shared mutable state—each `executeCommand` invocation runs sequentially and consumes its own local variables.

---

## Interactive Configuration Setup Flow (`setup.go`)

### Exported Functions

| Function | Signature | Notes |
|---|---|---|
| `RunSetupFlow` | `(repoRoot string) error` | Interactive setup flow that generates a `.code-reducer.yaml` configuration file. |
| `promptStringList` | `(reader *bufio.Reader, promptMsg string, existingList []string) []string` | Reads comma-separated list input from stdin. Returns existing values on I/O error or empty/"clear"/"none" input. |

### Internal (Unexported) Functions

| Function | Signature | Notes |
|---|---|---|
| `init` | `()` | Registers `setupCmd` with `RootCmd`. |
| `promptString` | `(reader *bufio.Reader, promptMsg string, existingVal string) string` | Reads single-line text input from stdin. Returns existing value on I/O error or empty input. |

### Internal State

| Identifier | Type | Notes |
|---|------|---|
| `setupCmd` | `\*cobra.Command` | The `setup` sub-command registered with the root cobra command. |

### High-Level Algorithmic Flow in `RunSetupFlow`

1. **Resolve existing state** — attempt to load any previously saved config from the current working directory via `config.LoadConfig(repoRoot)`. If none exists, all prompts default to built-in constants (Ollama defaults for model ID, base URL, context size; `"wiki"` for docs directory).
2. **Prompt user iteratively** for each setting:
   - LLM Model ID → string
   - Ollama Base URL → string
   - Ollama Context Size → integer (parsed from the current value formatted as a string via `strconv.Atoi(ctxInputStr)`)
   - Custom directories/files to ignore → comma-separated list
   - File extensions to ignore → comma-separated list
   - Documentation directory → string
3. **Preserve existing values on empty input** — if the user presses Enter without typing anything, the previously loaded (or default) value is kept.
4. **Support reset semantics** — for list-type inputs, the literal strings `"clear"` or `"none"` wipe the list entirely; otherwise comma-splitting produces the new list.
5. **Error swallowing on read failures** — if stdin reading fails during any prompt (`bufio.NewReader(os.Stdin)` + `reader.ReadString('\n')`), the error is logged to stderr and execution continues using existing values (no rollback, no restart).
6. **Persist final config** — after all prompts complete, a single `config.Config` struct is constructed from user input + preserved state and written to disk via `config.SaveConfig`. The success message references a `.yaml` file path defined in the config package.

### Key Business Rules

- **Non-destructive defaults:** Existing configuration is always preferred over built-in constants; new values only replace what the user explicitly provided (or left as default).
- **Idempotent re-run:** Running `setup` again when a config already exists produces an incremental update rather than a full overwrite.
- **Graceful degradation on I/O errors:** Any stdin read failure does not abort setup; it silently falls back to existing state.

### Side Effects & Error Handling in `setup.go`

**Disk reads (filesystem):**
- `os.Getwd()` — reads current working directory from filesystem. Returns error if unavailable; error is wrapped and propagated: `fmt.Errorf("failed to get current working directory: %w", err)`.
- `config.LoadConfig(repoRoot)` — reads a YAML config file from disk at `repoRoot`. If this fails, `existingCfg` remains `nil` (no panic).

**Disk writes (filesystem):**
- `config.SaveConfig(repoRoot, newCfg)` — writes the new configuration to disk. On error, wraps and returns: `"error saving configuration: %w"`. No retry or fallback.

**Stdin reads (user input):**
- `bufio.NewReader(os.Stdin)` + `reader.ReadString('\n')` — called 6 times total across `promptString` and `promptStringList`: model ID prompt, base URL prompt, context size prompt, custom directories/files to ignore, custom file extensions to ignore, documentation directory.

**Stdout writes:**
- Welcome banner (`fmt.Println`)
- Each prompt label (e.g., `"Enter LLM Model ID [existing]:"`)
- Success confirmation: `Configuration successfully saved to local <ConfigFileName> file.`

**Stderr writes:**
- On every `ReadString` failure, prints warning to stderr with the error value and falls back to existing/default values. No log level; just a single-line notice per call site.

### Error Handling Patterns in `setup.go`

| Failure point | Behavior |
|---|---|
| `os.Getwd()` | Wrapped into `fmt.Errorf("failed to get current working directory: %w", err)` and returned up the stack. |
| `config.LoadConfig(repoRoot)` | Nil-check only; if error occurs, existing values default to `OllamaDefault*` constants. No wrapping, no logging, no retry. Effectively swallowed. |
| `strconv.Atoi(ctxInputStr)` | Nil-check only; on parse failure or non-positive result, falls back to `existingNumCtx`. Swallowed silently. |
| `reader.ReadString('\n')` in prompt functions | Written to stderr as `"Warning: error reading input (%v), <fallback>"`. Returns existing value (or empty for list). No propagation — caller never sees the error. |
| `config.SaveConfig(repoRoot, newCfg)` | Wrapped with `%w`. Returned up the stack via `RunE` on the cobra command, so cobra will print it to stderr and exit non-zero. This is the only path that surfaces errors to the user without extra wrapping. |

### Notable Observations in `setup.go`

- **No panic anywhere** — all I/O failures are handled (silently or with a warning).
- **Two error strategies:** some paths *propagate* (`Getwd`, `SaveConfig`), others *swallow and fallback* (`LoadConfig`, `Atoi`, prompt readers). This inconsistency is intentional: setup tolerates missing config, but treats save failures as fatal.
- **No retry logic** — `SaveConfig` is called once; if it fails, the entire flow aborts.
- **Stdin errors are not logged to a structured logger** — they go directly to stderr with a single `fmt.Fprintf`.

---

## Update Subcommand Registration (`update.go`)

### Public Surface Area

This file exports **no public functions**, **no structs**, and **no interfaces**.

### Internal Symbols

| Symbol | Kind | Notes |
|---|------|---|
| `updateCmd` | `\*cobra.Command` | Local variable, not exported |
| `init()` | function | Lowercase; registered via Go's `init()` convention |

### Business Domain Concept

**Business Rule (stated):** When a project documentation system is updated incrementally—by scanning changed files since the last documented commit—the wiki pages should be regenerated accordingly.

**High-Level Algorithmic Flow:**
1. The user invokes an `update` subcommand on the root CLI (`RootCmd`).
2. Cobra parses and validates the command invocation.
3. The handler delegates to `executeCommand("update")`, which resolves the actual update logic (implementation not present in this file).

**Notes:** This file only registers the CLI entry point; no business logic, validation, or data processing is contained here.

### Side Effects & Error Handling in `update.go`

| Concern | Status |
|---|---|
| I/O (network/disk/DB) | Not present; delegated to external function (`executeCommand`) whose implementation is not present in this package boundary. |
| Error wrapping | None — raw pass-through of `executeCommand("update")` return value via `RunE`. |
| Sentinel errors | None created here. |
| Panic handling | Absent — if `executeCommand` panics, it propagates up unhandled (standard cobra behavior). |

---

## Init Subcommand Registration (`init.go`)

### Public Surface Area of `cmd/init.go`

**Exported items:** None.

### Business Domain Concept

CLI-driven documentation scaffolding tool (wiki generator). When the user runs `init`, the system scans the repository and generates an initial set of wiki markdown pages. This is a one-time setup operation for project documentation.

### Algorithmic Flow

1. User invokes the `init` subcommand on the CLI.
2. The command delegates to `executeCommand("init")`.
3. That function (defined elsewhere in this module) performs the repository scan and wiki page generation.

**Note:** All substantive logic is deferred to `executeCommand`, which is not included in this file. This file serves only as registration/wiring for the subcommand within the cobra command hierarchy.

### Side Effects & Error Handling in `init.go`

| Pattern | Used? |
|---|---|
| Wrap / aggregate errors | ❌ |
| Sentinel error | ❌ |
| Panic | ❌ |
| Pass-through delegation | ✅ — raw result of `executeCommand("init")` returned directly with no transformation. |

### Mutable State & Concurrency in `init.go`

**No mutable state.** The only variable (`initCmd`) is initialized once at package scope and never reassigned. No concurrent access or modification occurs within this file.

---

## Package-Level Summary

| Concern | Implementation Strategy |
|---|---|
| Command registration | Each subcommand file declares a local `\*cobra.Command` variable, registers it via `init()` (package-level), and wires its `RunE` callback to delegate to the shared `executeCommand(string) error` reference. |
| Configuration persistence | Handled by an external config package (`internal/config`). The `cmd` package only triggers the interactive flow in `setup.go` or reads flags in `root.go`. |
| Engine execution | Delegated to `engine.NewRunner.Run()`; event callbacks are defined inline in `root.go` for stdout/stderr routing. |
| Concurrency model | Sequential-only within this package boundary. Signal handling uses a cancellation context but does not protect shared mutable state. No channels, mutexes, or atomic types appear anywhere in the four files. |
| Error propagation strategy | Mixed: some paths propagate raw errors (`init`, `update`), others wrap and return sentinel messages (`root.go`'s git check), and still others swallow I/O failures silently with fallback values (`setup.go`). The only path that reaches cobra's default error printer is the final save failure in `setup.go`. |