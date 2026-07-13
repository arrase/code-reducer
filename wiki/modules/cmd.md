# Module: `cmd` — CLI Command Orchestration and Entry Point

## Responsibility

The `cmd` module defines the root cobra command (`RootCmd`) for **Code-Reducer**, a documentation agent that writes and maintains project wikis. It registers subcommands (`init`, `update`, `setup`), resolves configuration from merged sources (flags + file), routes engine events to stdout/stderr, handles OS signals for graceful shutdown, and delegates all substantive work to the unshown `executeCommand` entry point.

## Data Flow

```
User invokes code-reducer CLI
    │
    ├── 1. Register subcommands via init() / update.go (package-level)
    │
    ├── 2. Parse persistent flags (--model-id, --num-ctx) on RootCmd
    │
    ├── 3. Resolve current working directory; fail if not a git repo
    │
    ├── 4. Check configuration state
    │       ├── No config + TTY → RunSetupFlow(repoRoot)
    │       └── Config exists → Continue
    │
    ├── 5. Merge configuration sources: flags + file-based config (config.ResolveConfig)
    │
    ├── 6. Determine operation mode (init / update / run) and validate state
    │       ├── init → verify not already initialized
    │       ├── update → verify project initialized
    │       └── other modes → proceed to engine
    │
    ├── 7. Install signal handler for INT/TERM via signal.NotifyContext
    │
    ├── 8. Instantiate documentation engine and execute (engine.NewRunner.Run)
    │       └── Engine emits typed events during processing
    │               ├── Status → fmt.Println(ev.Message) [stdout]
    │               └── Error → fmt.Fprintf(os.Stderr, "Error: %s\n", ev.Message) [stderr]
    │
    └── 9. Return result (success or wrapped error via fmt.Errorf("…: %w", err))
```

## Subcommand Registration

### `init` — Documentation Initialization

The `cmd init` subcommand is defined and registered during package initialization in `init.go`. It delegates all processing to the unshown `executeCommand("init")`, which performs repository scanning and generates an initial set of wiki markdown pages. The command itself does not perform any I/O, error wrapping, or logic beyond registration and delegation.

### `update` — Incremental Wiki Update

The `cmd update` subcommand is defined in `update.go`. On invocation, it calls `executeCommand("update")`, which scans the repository for files changed since the last documented commit and regenerates corresponding wiki pages. The actual scanning and regeneration logic resides outside this file; `update.go` only defines the entry point.

### `setup` — Interactive Configuration Setup

The `cmd setup` subcommand is exported as `RunSetupFlow(repoRoot string) error`. It provides an interactive CLI flow that guides users through configuring all application settings, persisting them to `.code-reducer.yaml`. The configuration captures nine fields: model ID, base URL, context size, ignore patterns, docs directory path, and four distinct system prompts (extraction steps, module synthesis, architecture description, file-fact consolidation).

The setup flow operates as follows:
1. Attempt to load existing config via `config.LoadConfig(repoRoot)`. Errors are silently swallowed; defaults apply if loading fails.
2. Prompt user sequentially for each setting. Existing values appear in brackets as hints inside the prompt. Empty input or `clear`/`none` retains the existing/fallback value. Ignore patterns accept comma-separated values and replace the entire list on any non-empty input.
3. Assemble final configuration by merging user inputs with preserved existing values (prompts update only if new content is provided; ignores fully override).
4. Persist via `config.SaveConfig(repoRoot, newCfg)`. Success confirms the file path; failure returns a wrapped error `"error saving configuration: %w"`.

## Configuration Resolution and Validation

### State Initialization

Package-level variables are declared and assigned exactly once at initialization:
- `modelIDFlag` (`string`) — set via flag parsing on RootCmd. Only read subsequently.
- `numCtxFlag` (`string`) — same as above; never reassigned after declaration.
- `RootCmd` (`*cobra.Command`) — initialized with a literal; no subsequent writes occur in this file.

### Configuration Lifecycle Modes

The module enforces two distinct modes for project setup:
- **Init mode**: Creates/initializes documentation infrastructure (wiki structure). Fails if already initialized, requiring `update` to refresh later.
- **Update mode**: Refreshes existing documentation. Fails if the project has not been initialized yet.

### Configuration Resolution Priority

The merged configuration is resolved from three sources:
1. Persistent command-line flags (`--model-id`, `--num-ctx`)
2. File-based config (from project directory)
3. These are merged together into a single resolved config object via `config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag)`

## Signal Handling and Graceful Shutdown

A signal handler captures `INT` (`os.Interrupt`) and `TERM` (`syscall.SIGTERM`) signals via `signal.NotifyContext`. A deferred call to `stop()` ensures the context is cancelled when a signal arrives. The engine runner's returned error is then wrapped as `"documentation run failed: …"`.

## Error Handling Patterns

### Wrap Pattern (100% of observed paths)

Every error originates from a sub-function call, is immediately wrapped with `fmt.Errorf("…: %w", err)`, and propagated to the next level or returned to cobra's main handler. Examples include:
- `os.Getwd()` failure → wrapped as `"failed to get current working directory: …"`
- `tools.VerifyGitRepo` error → passed through unchanged (already a wrapped/typed error)
- `RunSetupFlow` error → passed through unchanged
- `config.ResolveConfig` error → passed through unchanged
- `engine.NewRunner(cfg).Run(...)` result → wrapped as `"documentation run failed: …"`

### Sentinel / Early-Return Logic

The function returns immediately if the working directory cannot be obtained (`os.Getwd()` fails). It returns early with a typed error from `tools.VerifyGitRepo` without wrapping. If stdin is not a terminal and config is missing, it returns an explicit sentinel message: `"configuration file … does not exist… Please run 'code-reducer setup'…"`. "Init already done" → returns wrapped error telling user to use `update`. "Not initialized yet" (during update) → returns wrapped error telling user to run `init` first.

### Nil Error Path

If `executeCommand` returns `nil`, the command succeeds silently. No logging or confirmation output is emitted in this file.

## Concurrency Characteristics

No mutexes, channels, goroutines, atomics, or other concurrency primitives are used in any of these files. All operations execute sequentially on the main thread. Concurrency relies solely on `context.Context` cancellation via OS signals; no shared-mutable-state synchronization primitives are present.