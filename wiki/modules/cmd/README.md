# cmd Package Architecture

## Module Responsibility and Data Flow

The `cmd` package implements the CLI entry point for **Code-Reducer**, a local-first AI agent that generates and maintains project wikis by analyzing source code against an LLM engine. The module orchestrates multi-step operations through a Cobra command tree, where each subcommand delegates to an unexported engine via a shared execution path.

All subcommands funnel through the internal `executeCommand(mode engine.Mode) error` function in `root.go`, which resolves operating environment state (terminal vs non-terminal), merges persisted and CLI-override configuration, validates preconditions per mode, registers signal handlers for graceful shutdown (`SIGINT`, `SIGTERM`), instantiates an engine runner with the resolved config, invokes it against the target repo root and requested mode, and routes emitted events to stdout/stderr.

## Command Registration Pattern

The package exposes three subcommands—`init`, `update`, and `setup`—registered via unexported `var` identifiers following Go's lowercase naming convention:

- **`initCmd`** (`cmd/init.go`) — Registers the `init` subcommand under `RootCmd`. On invocation, delegates to the engine with `engine.ModeInit`. The command is a thin wrapper; no public API surface exists beyond this registration.
- **`setupCmd`** (`cmd/setup.go`) — Implements an interactive first-run / re-configuration wizard. Persists user input to `.code-reducer.yaml` via `config.SaveConfig`. No public exported elements.
- **`updateCmd`** (`cmd/update.go`) — Registers the `update` subcommand under `RootCmd`. On execution, delegates to the engine with `engine.ModeUpdate`. All actual I/O occurs inside the unshown `internal/engine` package; this file performs none.

## Root Initialization and Configuration Resolution

The module's primary initialization logic resides in `cmd/root.go`, which wires persistent flags (`--model-id`, `--num-ctx`) to package-level `string` variables via Cobra's `StringVar`. These globals are set once during `init()` and read later by `executeCommand` from a single goroutine path, eliminating the need for synchronization primitives.

On invocation, the root command performs the following sequence:

1. **Working directory resolution** — `os.Getwd()` reads the filesystem to obtain the current working directory; failure is wrapped with `fmt.Errorf("failed to get current working directory: %w", err)`.
2. **Git repository validation** — `tools.VerifyGitRepo(repoRoot)` checks whether the resolved path is a valid git repository, performing disk I/O to verify structure/validity. Error propagates directly without wrapping.
3. **Implicit setup flow** — If no configuration file exists and stdin is a terminal (determined via `isatty.IsTerminal(os.Stdin.Fd())` / `isatty.IsCygwinTerminal(os.Stdin.Fd())`), an automatic setup routine runs; otherwise the user is directed to the `setup` subcommand.
4. **Configuration merging** — `config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag)` reads merged configuration from disk, combining persisted config with CLI-override flags into a single resolved object. Error propagates directly without wrapping.
5. **Mode-specific preconditions** — The engine runner validates that `init` mode is rejected if already initialized (force use of `update`), and `update` mode is rejected if not yet initialized.
6. **Signal handling** — Handlers for `SIGINT` and `SIGTERM` are installed on a background context so the runner can terminate cleanly on user interruption.
7. **Engine invocation** — A runner is instantiated with the resolved config, invoked against the target repo root and requested mode, and events are streamed back to stdout/stderr as they occur (status, errors, regular output).

## Interactive Setup Flow

The setup wizard in `cmd/setup.go` solves the first-run / re-configuration problem by guiding users through entering all required settings so they can be persisted to a local YAML configuration file. The flow is designed to reuse any previously saved values, allowing partial re-configuration rather than starting from scratch.

### External I/O Interactions

| Interaction | Direction | Mechanism | Notes |
|---|---|---|---|
| Stdin reading (user input) | Read | `bufio.NewReader(os.Stdin)` — used by `promptString` and `promptStringList` | Synchronous terminal reads; no buffering to external system |
| Current working directory | Read | `os.Getwd()` in `RunSetupFlow` | Local filesystem query; error is wrapped with `%w` on failure |
| Config file load | Read | `config.LoadConfig(repoRoot)` | Reads `.code-reducer.yaml` from disk at `repoRoot`; result used as fallback values for prompts. Load failure is treated as "no existing config"; existing values default to zero-state equivalents (`existingCfg` remains nil). No error returned; flow continues normally. |
| Config file save | Write | `config.SaveConfig(repoRoot, newCfg)` | Writes updated configuration to disk; error is wrapped with `%w` on failure. This is the only path that returns a non-nil error from `RunSetupFlow`, propagating directly to the cobra command's `RunE`. |

No network calls, database access, or API interactions are present in this file.

### Prompt Functions

- **`promptString(reader *bufio.Reader, promptMsg string, existingVal string) string`** — Reads stdin input; if no valid input is given (user cancels or clears), returns the `existingVal`. Stdin read errors are logged to `os.Stderr` with a warning message, then the function returns the existing/fallback value instead of propagating upward. No exception escapes this function.
- **`promptStringList(reader *bufio.Reader, promptMsg string, existingList []string) ([]string, bool)`** — Reads stdin input; accepts comma-separated list values or supports `"clear"` / `"none"` to reset the ignore list entirely. Returns updated or existing list and a boolean indicating whether the user modified it (`true`) or kept existing values (`false`). Stdin read errors follow the same pattern as `promptString`.

### Algorithm Steps for RunSetupFlow

1. Determine repository root by resolving the current working directory.
2. Load existing configuration if present; extract its values as starting points so the user only needs to change what they want to change. If load fails, existing values default to zero-state equivalents (`existingCfg` remains nil).
3. Prompt for LLM Model ID — display current value as hint; accept new input or fall back to the default Ollama model identifier when empty.
4. Prompt for Ollama Base URL — same pattern: show existing, return existing on empty input.
5. Prompt for Context Size (numCtx) — read string input, parse to integer with validation; if invalid or zero, revert to the previously loaded value.
6. Prompt for Ignore Patterns — accept comma-separated list of directory/file/pattern strings; support `"clear"` / `"none"` to reset the ignore list entirely; otherwise keep prior values when no new input is provided.
7. Prompt for Documentation Directory Path — show existing path as hint, return previous value on empty response.
8. Prompt for Extraction Steps — if a prior config existed with extraction steps defined, reuse them; otherwise start from built-in defaults.
9. Prompt for System Prompts (four separate fields: system prompt, module synthesis prompt, architecture prompt, file fact consolidation prompt) — preserve any previously set values when the user leaves input empty or omits it.
10. Assemble and persist configuration — construct a new `Config` struct combining fresh user input with preserved defaults/previous values; write to disk via the config loader.

## Error Propagation Summary

- **Wrapped errors**: `os.Getwd()` failure is wrapped with `%w`; `config.SaveConfig` failure is wrapped with `"error saving configuration: %w"`.
- **Direct pass-through**: Errors from `VerifyGitRepo`, `RunSetupFlow` (excluding save), and `ResolveConfig` are returned as-is without wrapping.
- **Contextual wrap**: The engine runner's error (`runner.Run(...)`) is wrapped with `"documentation run failed: %w"`.
- **Silenced errors**: `RootCmd.SilenceErrors: true` means any unhandled errors from Cobra's command execution (e.g., flag parsing failures, subcommand errors) are swallowed and not printed.
- **Signal context cancellation**: If the context is cancelled via signal before `runner.Run()` returns an error, the resulting error would be propagated through the `err != nil` check as a wrapped "documentation run failed" message.

## State and Concurrency Assessment

No mutable state exists in `cmd/init.go`, `cmd/update.go`, or `cmd/setup.go`. The package-level `string` variables (`modelIDFlag`, `numCtxFlag`) are set once during initialization and read from a single goroutine path by `executeCommand`, so no synchronization mechanisms (locks, mutexes, sync primitives) are required. No race conditions can be identified from this code alone.