# cmd Package — Technical Architecture

## Module Responsibility

The `cmd` package implements a CLI application that scans a repository structure and generates/maintains wiki documentation pages. It acts as the orchestration layer for two operational modes—**initialization** (`init`) and **update**—each routed through a shared dispatcher (`executeCommand`). The module validates preconditions (Git repo presence, configuration existence, credential setup), resolves merged configuration from multiple sources, delegates execution to an engine component, and propagates final errors or surfaces progress events via stdout/stderr.

## Public Surface Area

The package exposes no exported identifiers. All entry points—`RootCmd`, `initCmd`, `updateCmd`, `setupCmd`—are unexported cobra command instances registered at init time. The only externally callable function is `RunSetupFlow(repoRoot string)`, which serves as the interactive setup wizard entry point.

## Internal Components

### Command Registration Layer (`cmd/init.go`)

Package-level global variables are initialized once and never mutated within this file:

- `initCmd` — cobra command instance for the `init` subcommand
- `updateCmd` — cobra command instance for the `update` subcommand (defined in a separate file but referenced here)
- `RootCmd` — root cobra command, shared across all files in the package

The `init` subcommand's `.Run` closure delegates to `executeCommand("init")`. The `update` subcommand follows the same delegation pattern. Both commands are registered with `RootCmd.AddCommand` during init; any registration failure is silently swallowed without error propagation.

### Root Command Orchestration (`cmd/root.go`)

This file implements the primary entry point for all non-setup CLI invocations. It performs a linear sequence of precondition checks and configuration resolution before delegating to an engine runner:

1. **Working directory validation** via `os.Getwd()`—failure prints error to stdout, exits with code 1
2. **Git repository verification** via `tools.VerifyGitRepo(repoRoot)`—error formatted to stderr, exit code 1
3. **Configuration existence check** via `config.ConfigExists(repoRoot)`—used only for the TTY guard branch
4. **Terminal detection** via `isatty.IsTerminal`/`IsCygwinTerminal` on stdin fd—determines whether implicit setup flow is permitted or a non-TTY error path fires
5. **Implicit setup trigger**—when no config exists and stdin is interactive, invokes the setup wizard before proceeding; otherwise exits with an instruction to run setup first
6. **Configuration resolution** via `config.ResolveConfig(repoRoot, ...)`—merges persistent flags, environment/file-based config, and user overrides into a single effective config object
7. **Metadata file read**—`os.ReadFile(metadataPath)` followed by `json.Unmarshal` into a local struct containing `lastSHA` (last documented commit) and other state; parse failures are silently swallowed (no log, no exit)
8. **Mode isolation validation** between `init` and `update`—rejects running `init` when already initialized (`hasInit` true), rejects `update` when not initialized (`hasInit` false)
9. **Credential validation**—verifies a model identifier is non-empty; failure triggers setup flag but does not abort
10. **Engine runner instantiation and execution** via `engine.NewRunner(cfg).Run(ctx, repoRoot, mode, ...)`—any returned error is printed to stderr and causes `os.Exit(1)`

Local mutable state within `executeCommand` includes `meta`, `cfg`, `repoRoot`, `lastSHA`, `hasInit`—all scoped to the function, single-threaded. Global variables (`modelIdFlag`, `numCtxFlag`) are read-only after their initial assignment in the init closure and never mutated elsewhere in this file.

### Interactive Setup Wizard (`cmd/setup.go`)

Implements incremental configuration generation for `.code-reducer.yaml`. The flow reads existing config first, then prompts per-field with pre-populated values—only changed fields get updated; untouched values persist unchanged:

- **LLM model selection** defaults to `gemma4:26b-a4b-it-qat`; users may override to any Ollama-compatible model ID
- **Ollama connection parameters**—base URL (defaults to standard port) and context window size in tokens; invalid/empty input falls back to defaults
- **File/directory exclusion rules** maintained as a list of directories and file extensions; first setup collects fresh values, subsequent runs show existing entries with comma-separated append support; empty input leaves the current list intact
- **Documentation directory** defaults to `wiki`; user may change freely
- **Extraction steps**—a fixed sequence loaded once on first setup; preserved without prompting on subsequent runs

The function reuses a local `reader` (`bufio.NewReader(os.Stdin)` + `ReadString('\n')`) per call. Configuration is persisted via `config.SaveConfig(repoRoot, newCfg)`.

### Update Command Registration (`cmd/update.go`)

A thin registration layer that wires up the `update` cobra subcommand with `RootCmd.AddCommand` during init. The command's `.Run` closure delegates to `executeCommand("update")`. No business logic, validation rules, or algorithmic steps are implemented in this file—the actual behavior is defined elsewhere. No I/O operations occur here beyond the registration call itself.

## Error Handling Patterns

The module exhibits three distinct error-handling strategies:

| Pattern | Locations | Behavior |
|---|---|---|
| **Sentinel exit** | `root.go` (CWD failure, git check, config-missing+non-TTY, init-already-ran, update-not-init, runner failure); `setup.go` (Getwd failure, SaveConfig failure) | `fmt.Fprintf(os.Stderr, ...)` followed by `os.Exit(1)`. No error wrapping. Process terminates immediately with exit code 1. |
| **Silent swallow** | `root.go` (metadata file read/unmarshal); `setup.go` (LoadConfig errors, Atoi parse failures); `update.go` (all paths including Run closure return value discarded by delegation) | Errors are captured but not logged or reported; execution continues with empty/zero values. No user notification. |
| **Warning + fallback** | `setup.go` (stdin read errors in promptAndAppend/promptString) | Warning message printed to stdout, function returns a sensible default rather than propagating the error. Flow continues normally. |

No panic recovery (`recover()`), no custom sentinel types, no structured error wrapping visible within any of these files. Context cancellation is registered via `signal.NotifyContext(os.Interrupt, syscall.SIGTERM)` in `root.go` with `defer stop()`; cancellation side effects are swallowed unless the engine runner surfaces them.

## Data Flow

```
User Input (stdin) ──┬──► Cobra Dispatch (RootCmd.Execute)
                      │
                      ├──► initCmd.Run ──► executeCommand("init")
                      │                       │
                      │                       ├──► config.ResolveConfig(repoRoot, ...)
                      │                       ├──► engine.NewRunner(cfg).Run(ctx, repoRoot, "init", ...)
                      │                       └──► Progress events → stdout/stderr via callback
                      │
                      ├──► updateCmd.Run ──► executeCommand("update")
                      │                            (implementation external)
                      │
                      └──► setupCmd.Run (RunSetupFlow(repoRoot))
                                       │
                                       ├──► config.LoadConfig(repoRoot) ──┐
                                       ├──► Per-field prompts via reader   │
                                       ├──► config.SaveConfig(repoRoot, newCfg)
                                       └──► Success confirmation → stdout
```

Configuration resolution merges three sources—persistent flags on the command, environment/file-based config resolution, and user-provided overrides—into a single effective config before any work begins. The metadata file tracks last documented commit SHA for resume capability; its absence or malformed content is treated as a no-op state reset.