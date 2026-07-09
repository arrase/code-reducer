# Module: `cmd` — Code-Reducer CLI Entry Point & Command Registry

## Responsibility

The `cmd` package provides the Cobra-based CLI surface area for interacting with the Code-Reducer documentation engine. It owns three concerns:

1. **Command registration** — wiring subcommands (`init`, `update`) to the root command tree via unexported `init()` functions in `cmd/init.go` and `cmd/update.go`.
2. **Pre-flight environment validation & configuration resolution** — verifying the working directory is a git repository, resolving merged configuration from file + CLI flags, and detecting project initialization state (last documented commit SHA).
3. **Engine orchestration** — spawning an engine runner with signal handlers active, feeding typed events (`"status"`, `"error"`) to stdout/stderr via `fmt` family functions.

All substantive behavior lives in the unexported `executeCommand(mode string)` function referenced from both subcommand files; neither file contains its own processing logic beyond delegation.

## Data Flow

```
User invokes "code-reducer init|update"
        │
        ▼
cmd/init.go ── Run() ──► executeCommand("init")
cmd/update.go ── Run() ──► executeCommand("update")
        │                          │
        │                          ▼
        │              root.go: Validate → Resolve Config → Query Last Commit SHA
        │                                      │
        │                                      ▼
        │                              Route by mode (init|update)
        │                                      │
        │                                      ▼
        └──────────── RootCmd.AddCommand(initCmd/updateCmd)
                                  │
                                  ▼
                          engine.NewRunner().Run(callback)
```

## 1. Command Registration

### `cmd/init.go` — Init Subcommand Entry Point

- Declares an unexported `initCmd = &cobra.Command{...}` and a package-level `init()` function that registers it via `RootCmd.AddCommand(initCmd)` during package initialization.
- The exported `Run` handler calls `executeCommand("init")` with no error check on the return value — any failure from downstream logic is swallowed at this layer.

### `cmd/update.go` — Update Subcommand Entry Point

- Mirrors the pattern of `init.go`: unexported `updateCmd = &cobra.Command{...}` and a package-level `init()` function that registers it under the root command tree.
- The exported `Run` handler delegates to `executeCommand("update")`; no error handling is performed here.

### `cmd/root.go` — Root Command & Lifecycle Management

The package-level variable `RootCmd *cobra.Command` is initialized once via a struct literal and never reassigned. An unexported `init()` function registers persistent flags (`--model-id`, `--num-ctx`) on `RootCmd`.

## 2. Pre-flight Validation & Configuration Resolution (root.go)

### Environment Prerequisites

| Check | Mechanism | Failure Path |
|---|---|---|
| Current working directory exists | `os.Getwd()` | Fatal — prints `"Error getting current working directory: %v"`, exits with code 1 |
| Git repository validity | `tools.VerifyGitRepo` (outbound call) | Fatal — prints `"Error: %v"` (raw error from verifier), exits with code 1 |
| Configuration file existence | `config.ConfigExists` (disk stat) | Fatal if missing AND stdin is not a terminal — instructs user to run `code-reducer setup`; swallowed if non-interactive context, proceeds without config |

### Mode Routing & Initialization State Detection

After validation passes, the system queries the last documented commit SHA from git history and routes execution based on the mode argument:

| Mode | Pre-condition Violation | Error Message |
|---|---|---|
| `init` | Project already initialized | `"Error: The project has already been initialized..."` → exit 1 |
| `update` | Project not yet initialized | `"Error: The project has not been initialized yet..."` → exit 1 |

### Configuration Resolution

The effective configuration merges three sources in priority order at runtime (not on init): persisted `.code-reducer.yaml`, explicit CLI flags (`--model-id`, `--num-ctx`), and built-in defaults. This allows per-invocation overrides without modifying stored state.

## 3. Interactive Setup Flow (cmd/setup.go)

### Entry Point: `RunSetupFlow(repoRoot string)`

The sole exported identifier in this file. Takes the repository root directory path as input; performs no return value.

### Algorithmic Steps

1. **Resolve working directory** — Fail fast with a message if `os.Getwd()` returns an error (calls `os.Exit(1)`).
2. **Load existing configuration** — Calls `config.LoadConfig(repoRoot)`. If the read fails and `cfg == nil`, setup continues silently as if no prior config exists (load errors are swallowed, not reported to user).
3. **Prompt user in sequence** — For each setting (model ID, Ollama URL, context size, ignore paths, ignore extensions, docs directory), prints `"Label [existingValue]:"`. If no existing value is present, only the label is printed.
4. **Validate and sanitize input**:
   - Context size parsed via `strconv.Atoi`; on failure or non-positive result, falls back to built-in default (`config.OllamaDefaultNumCtx`) with no warning print.
   - Ignore lists split on commas, trimmed per token; empty tokens discarded.
5. **Persist configuration** — Calls `config.SaveConfig(repoRoot, newCfg)`. On write failure, prints error and calls `os.Exit(1)`.

### Business Rules for Setup Flow

- **Non-destructive defaults**: Empty string input reverts every field to its stored or built-in default; prior configuration is never silently overwritten with blanks.
- **Graceful degradation on stdin errors**: Terminal read failures in `promptString` and `promptStringList` print a warning line but return the existing value unchanged — no abort.
- **Idempotent re-runs**: Re-executing setup preserves whatever was previously configured, only changing fields the user explicitly modifies.

### I/O Summary for Setup Flow

| Operation | Direction | Notes |
|---|---|---|
| `config.LoadConfig` | Read | Failure silently swallowed; setup proceeds with empty defaults |
| `config.SaveConfig` | Write | Failure → stderr message, calls `os.Exit(1)` |
| Stdout via `fmt` | Outbound | Welcome banner, per-prompt labels, warning lines, success confirmation |
| Stdin via `bufio.Reader` | Inbound | Both prompt functions consume one line per interaction via `ReadString('\n')` |

## 4. Engine Orchestration & Signal Handling (root.go)

### Signal Registration

The main execution loop registers interrupts (`os.Interrupt`) and termination signals (`syscall.SIGTERM`) via `signal.NotifyContext`, deferring their cleanup handlers to ensure the engine can be interrupted cleanly during long-running documentation tasks.

### Engine Runner Invocation

After validation, config resolution, and mode routing pass:
- `engine.NewRunner` is created (implementation outside this file).
- `runner.Run(callback)` is invoked with a callback that handles three event types (`"status"`, `"error"`, default): all print to stdout or stderr via `fmt` family — no state changes, no logging to file, no metrics emission.

### Error Handling Strategy for Execution Path

| Condition | Message Format | Output Stream |
|---|---|---|
| `runner.Run` returns non-nil error | `"Documentation Run Failed: %v"` | stdout (unlike other fatal paths which use stderr) |
| Any validation failure above | Varies per condition | stderr, exits code 1 |

## 5. Concurrency & State Analysis

| Concern | Status |
|---|---|
| Package-level mutable state | None — `RootCmd`, `initCmd`, `updateCmd` are all assigned once at init and never modified after construction |
| Concurrent primitives (mutex, channels, goroutines) | Absent across all four files |
| I/O operations directly in these files | Minimal — only disk stat/read for config checks, stdout writes via `fmt`, signal registration; no network calls, no direct file access beyond config package |

## 6. External Dependencies Referenced

- **`github.com/spf13/cobra`** — CLI framework used for command tree construction and flag parsing across all four files.
- **`tools.VerifyGitRepo`** — referenced in `root.go`; implementation outside this module.
- **`config.ConfigExists`, `config.LoadConfig`, `config.SaveConfig`** — config package references; implementations outside this module.
- **`engine.NewRunner`, `runner.Run`** — engine package references; implementations outside this module.