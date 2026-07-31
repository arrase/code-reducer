# cmd Package: CLI Orchestration Layer

## Responsibility

This package implements the command-line interface for `code-reducer`, an interactive LLM-driven documentation agent. It routes user input through a lifecycle of three engine modes (`init`, `update`, and an additional mode resolved by `executeCommand`) while managing project state transitions—configuring setup when no configuration exists, validating git repository requirements, loading flags, checking initialization status against the requested mode, executing the engine with signal handling, and streaming events to stdout/stderr.

## Data Flow

```
User Terminal ──► RootCmd (cobra) ◄──► init() / RunSetupFlow(repoRoot)
                                                              │
                                                              ▼
                                              executeCommand(Mode)
                                        ┌──► checkAndRunSetup(repoRoot)
                                        │    Check git repo, config existence, TTY check
                                        │    If no config + stdin is TTY → RunSetupFlow
                                        │
                                        ├──► checkInitStatus(repoRoot, docsDir, Mode)
                                        │    Validates init marker presence; enforces:
                                        │      - ModeInit fails if already initialized
                                        │      - ModeUpdate fails if not yet initialized
                                        │
                                        ├──► runEngine(repoRoot, cfg, Mode)
                                        │    Registers SIGINT/SIGTERM handlers
                                        │    Instantiates runner
                                        │    Streams events (status → stdout, error → stderr)
                                        │
                                        └──► executeCommand returns engine.Error or nil
```

## Command Registration and Delegation

The `init()` function registers a `*cobra.Command` named `update` under the root command. Its `RunE` handler delegates to `executeCommand(engine.ModeUpdate)`. The same delegation pattern applies elsewhere: `setup.go` defines `*cobra.Command setupCmd`, whose `RunE` invokes `RunSetupFlow(repoRoot)` and propagates errors upward through cobra's exit path.

## Flag Registration

The package-level variables `modelIDFlag` (string) and `numCtxFlag` (string) are registered on the root command via `StringVar`. They persist for the lifetime of the process and are read by downstream functions including `executeCommand`, `checkAndRunSetup`, and `runEngine`. No synchronization primitives—locks, mutexes, atomics, channels—are used.

## Initialization State Validation

`checkInitStatus(repoRoot, docsDir string, mode engine.Mode) error` validates that the project has been initialized before allowing non-init operations. It calls `engine.IsInitialized(repoRoot, docsDir)` to check for init marker files in the docs directory. User-facing errors are created inline via `fmt.Errorf(...)`; no wrapping of previous errors occurs—error chains terminate at this point.

## Interactive Configuration Setup

`RunSetupFlow(repoRoot string) error` guides the user through setting up `.code-reducer.yaml`. It loads any previously saved configuration to preserve preferences across sessions, then walks each configurable domain (model identity, LLM endpoint URL, context size limit, ignore patterns, documentation directory path, four system prompt templates), prompting via `promptString` and `promptStringList`. For every field, empty input or a clear/none directive falls back to the prior value. The context size specifically validates numeric input via `strconv.Atoi`; on parse failure or non-positive values it silently returns `existingNumCtx` with no error propagation—this is intentional fallback behavior only.

## Engine Execution and Signal Handling

`runEngine(repoRoot string, cfg *config.Config, mode engine.Mode) error` registers interrupt/terminate handlers (`os.Interrupt`, `syscall.SIGTERM`) via `signal.NotifyContext(context.Background(), ...)`. The context is stored in a local variable and deferred stop on exit. It instantiates the engine runner and executes documentation generation while streaming events of three types—status (stdout), error (stderr, prefixed with "Error: "), and other—to stdout or stderr as appropriate.

## Error Propagation Summary

| Source | Pattern |
|---|---|
| `engine.IsInitialized` | New `fmt.Errorf(...)`; chain terminates here |
| `RunSetupFlow` read failures | Wrapped with prefix (`"error reading model ID: %w"`); propagated to cobra exit |
| `config.SaveConfig` failure | Wrapped as `"error saving configuration: %w"`; returned from `RunE` |
| `engine.ExecuteCommand` return | Pass-through; no wrapping |
| Git verification (`tools.VerifyGitRepo`) | Returned verbatim; chain terminates |
| Config resolution | Passed through unchanged |
| `RootCmd.Execute()` in tests | Discarded (`_ = RootCmd.Execute()`); known pattern for CLI exit-code testing |

## Test Coverage

`cmd_test.go` exercises the public API surface indirectly. It validates flag parsing via package-level variables, prompt input handling via buffered readers simulating stdin, and initialization state management through filesystem operations confined to `t.TempDir()`.