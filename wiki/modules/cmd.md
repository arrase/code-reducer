# cmd Package — Technical Documentation

## Module Responsibility

The `cmd` package implements a Cobra-based CLI entry-point layer for Code-Reducer, a documentation agent that writes and maintains project wikis. The module's scope is restricted to command definition, wiring, initialization sequencing, and user-facing I/O (terminal prompts, status/error output). All executable behavior—repository scanning, wiki generation, configuration resolution—is delegated to external functions defined in other packages or files within the same package; `cmd` itself contains no domain processing logic.

## Architecture Overview

The module exposes four subcommands registered under an unexported root command (`RootCmd`). Registration is performed at package initialization via standard Go `init()` functions:

| File | Registered Command | Role |
|---|---|---|
| `cmd/init.go` | `*cobra.Command` (unexported) | `init` subcommand — delegated to `executeCommand("init")` |
| `cmd/update.go` | `*cobra.Command` (unexported) | `update` subcommand — delegates to `executeCommand("update")` |
| `cmd/root.go` | `*cobra.Command` (`RootCmd`) | Root command — wires flags, validates environment, drives event loop |
| `cmd/setup.go` | `*cobra.Command` (unexported) | Interactive setup wizard — produces `.code-reducer.yaml` config |

The actual execution handler is an unexported function `executeCommand(mode string)` referenced from each subcommand's `Run`. Its definition is external to all four files; only the delegation call sites are observable.

## Command Registration Layer

### init.go

Package-private `init()` registers the `init` subcommand with `RootCmd` at package initialization. No exported symbols, no mutable state, no concurrency primitives exist in this file. The global `initCmd *cobra.Command` is assigned once and never reassigned within its scope; it is effectively immutable for the lifetime of the process.

### update.go

Package-private `update()` registers the `update` subcommand with `RootCmd`. No exported symbols, no mutable state beyond a single unexported `*cobra.Command` variable, no concurrency primitives. The `Run` callback delegates entirely to the external `executeCommand("update")`; any errors from that call site are swallowed by Cobra's framework-level panic propagation path because the signature is `func(cmd *cobra.Command, args []string)` with no error return type.

### root.go

This file defines three observable items:

- **`RootCmd *cobra.Command`** — exported variable holding the root command instance.
- **Unexported `init()` function** — registers `updateCmd` with `RootCmd`.
- **Package-private execution flow** — parses flags, validates environment (git repo root, config file existence), resolves merged configuration (CLI flag values + persisted config), validates init/update state consistency, installs a signal handler for graceful shutdown (`SIGINT`, `SIGTERM` via `signal.NotifyContext(context.Background(), ...)`), starts the engine runner in event-loop mode, and exits with appropriate status on completion or failure.

The package-level flags `modelIdFlag` (string) and `numCtxFlag` (string) are set by Cobra's flag parser during init and read later inside `executeCommand`; they are not protected by any synchronization primitive and their mutation is observable only at the point of assignment.

## Configuration Setup — setup.go

### Public Surface Area

| Function | Input Types | Output Type |
|---|---|---|
| `RunSetupFlow` | `repoRoot string` | void |
| `promptStringList` | `reader *bufio.Reader`, `promptMsg string`, `existingList []string` | `[]string` |
| `promptString` | `reader *bufio.Reader`, `promptMsg string`, `existingVal string` | `string` |

### Algorithmic Flow

1. **Resolve current working directory** via `os.Getwd()`; fail fast with `fmt.Printf(...)` + `os.Exit(1)` on error.
2. **Load prior config** (if any) via `config.LoadConfig(repoRoot)`. A load failure is treated as "no existing config" — all fields fall back to hardcoded defaults (`existingModel`, `OllamaDefaultBaseURL`, `OllamaDefaultNumCtx`). No panic, no exit on this path.
3. **Iterate prompts** for each configuration dimension:
   - LLM Model ID → prompt string (fallback from existing value or default).
   - Ollama Base URL → prompt string.
   - Ollama Context Size → prompt string; validated via `strconv.Atoi` with condition `err == nil && n > 0`; failure keeps existing value.
   - Custom ignore paths / extension list → comma-separated prompt; empty input yields no new entries.
   - Documentation directory → prompt string (default `"wiki"`).
4. **Build the complete config object** from all collected values. Extraction steps are filled in with defaults if absent.
5. **Persist to disk** via `config.SaveConfig(repoRoot, newCfg)`. A save failure is fatal: printed + `os.Exit(1)`.

### State and Concurrency

- `setupCmd *cobra.Command` — initialized once in `init()`; never reassigned within this file.
- All other variables are function-local parameters or temporaries. The `config.Config` struct is loaded into a local variable, then replaced by a new instance before being saved; no concurrent access observable within this file's scope.

### Error Handling Classification

| Path | Behavior | Notes |
|---|---|---|
| `os.Getwd()` fails | Fatal: stdout print + `os.Exit(1)` | Unwrapped error |
| `config.SaveConfig` fails | Fatal: stdout print + `os.Exit(1)` | Unwrapped error |
| `config.LoadConfig` returns error | Graceful fallback to defaults | No exit, no panic |
| `strconv.Atoi` fails in context-size prompt | Graceful fallback to existing value | Conditionally preserved |
| `reader.ReadString('\n')` fails (either helper) | Warning print + return existing value | Standard library path only |

No error wrapping. All errors originate from the standard library (`os`, `bufio`, `strconv`) or the external `config` package; none are redefined locally. No panic recovery, no sentinel values, no custom error types in this file.

## Entry Point and Execution Routing

The module's entry point is `RootCmd`. Its execution path follows a deterministic pipeline:

```
CLI invocation → parse flags & mode
    ├─ verify git repo root (tools.VerifyGitRepo), exit if invalid
    │
    ├─ check config file existence (internal/config.ConfigExists)
    │   ├─ missing + TTY       → implicit RunSetupFlow(repoRoot) [return value unobservable]
    │   ├─ missing + !TTY      → error message to stderr, os.Exit(1)
    │   └─ present              → proceed to mode validation
    │
    ├─ resolve merged config (internal/config.ResolveConfig: file + flags)
    │
    ├─ validate init/update state consistency
        ├─ "init" + already initialized → reject with last documented commit
        ├─ "update" + not initialized  → reject, suggest running init
        └─ other modes                 → proceed
    │
    ├─ install signal handler for graceful shutdown (SIGINT, SIGTERM)
    │
    ├─ start engine runner in event loop mode
    │   (propagates typed events to stdout/stderr via callback closure)
    │
    └─ on completion or failure, exit with appropriate status
```

### I/O Operations Observed from Entry Point

| # | Operation | Direction | Notes |
|---|-----------|-----------|-------|
| S1 | Print status messages to stdout | Out → Terminal | `fmt.Println(ev.Message)` inside event handler callback passed to `engine.Runner` |
| S2 | Print error events to stderr | Out → Terminal | `fmt.Fprintf(os.Stderr, "Error: %s\n", ev.Message)` for `"error"`-typed events only |
| S3 | Print arbitrary events to stdout | Out → Terminal | `fmt.Println(ev.Message)` fallback for non-status, non-error event types |
| S4 | Write error on `os.Getwd()` failure | Out → stderr | `fmt.Fprintf(os.Stderr, "Error: %v\n", err)` + `os.Exit(1)` |
| S5 | Write error after `tools.VerifyGitRepo` return | Out → stderr | Same pattern as S4 |
| S6 | Print setup-required message (missing config + non-TTY) | Out → stdout | `fmt.Printf("Error: Configuration file ... does not exist...")` + `os.Exit(1)` |
| S7 | Print init-already-ran error | Out → stderr | `"The project has already been initialized..."` + `os.Exit(1)` |
| S8 | Print not-initialized error | Out → stderr | `"The project has not been initialized yet..."` + `os.Exit(1)` |
| S9 | Print run-failure message | Out → stdout | `fmt.Printf("Documentation Run Failed: %v\n", err)` after `runner.Run` returns error — notable deviation from other errors (stdout rather than stderr) |

### Concurrency and Signal Handling

- A background context is created via `signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` for graceful shutdown on interrupt/term signals. This provides a cancellation scope; no shared state protection is observable within this file's scope.
- No `sync.Mutex`, channels, or other synchronization primitives are used anywhere in the module.

## Error Handling Patterns Across Module

| # | Pattern | Location | Details |
|---|---------|----------|---------|
| E1 | Immediate exit on unrecoverable OS error | `os.Getwd()` failure | Calls `os.Exit(1)` directly after printing to stderr; no wrapping, no context preservation |
| E2 | Immediate exit on validation failure | `tools.VerifyGitRepo` return | Same pattern as E1 |
| E3 | Conditional exit with terminal check | Missing config + non-TTY stdin | Prints user-facing hint then calls `os.Exit(1)`; swallows the underlying "no config file" condition |
| E4 | Immediate exit on engine failure | After `runner.Run` returns error | Printed to stdout (S9), then `os.Exit(1)`; error unwrapped for display only |

### Notable Observations

- **No panic recovery**: All errors are handled via explicit checks + `os.Exit(1)`. No `defer/recover` or panic handlers visible.
- **Context cancellation setup**: A background context is created with signal notifications and deferred stop, but no observable code in this file reads from or responds to that signal beyond its creation — the `defer stop()` at function end only fires when the program exits non-normally (i.e., never returns normally).
- **No error wrapping**: Every `fmt.Printf/Println` that prints an error does so with `%v` or `%s` on the raw error value. No use of `errors.Join`, `fmt.Errorf(wrap...)`, or custom sentinel types visible anywhere in this module's scope.