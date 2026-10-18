# cmd Package Architecture

## Module Responsibility

The `cmd` package implements the CLI entry point for a documentation agent that writes and maintains project wikis. It orchestrates the documentation engine across three operational modes: **init** (first-time documentation build), **setup** (interactive configuration wizard), and **update** (incremental wiki regeneration). All commands are registered as `*cobra.Command` instances and share a common execution path through `executeCommand`, which dispatches to the engine based on the requested `engine.RunMode`.

## Data Flow

```
CLI Invocation
    │
    ▼
RootCmd (root.go)
    │
    ├── ModeInit ──► initCmd ──► executeCommand(ModeInit) ──► engine.Run(ModeInit)
    │
    ├── ModeUpdate ──► updateCmd ──► executeCommand(ModeUpdate) ──► engine.Run(ModeUpdate)
    │
    └── ModeSetup ──► setupCmd ──► RunSetupFlow ──► config.SaveConfig
```

## Command Registration

### init.go

The `init` command is registered at package scope via a global `*cobra.Command` variable. On execution, it delegates to `executeCommand` with `engine.ModeInit`. The engine mode triggers a repository scan and generates the first set of wiki markdown pages. No public API surface is exposed from this file; all identifiers are unexported.

### root.go

The root command (`RootCmd`) serves as the CLI application's top-level entry point. It is initialized once and never modified after creation. The root command binds two persistent flags (`modelIDFlag`, `numCtxFlag`) and provides the following unexported functions:

- **`init()`** — Initializes the root command and registers all subcommands.
- **`executeCommand(mode engine.RunMode)`** — Central dispatch function. Accepts a `engine.RunMode` and returns `error`. Aggregates errors from `os.Getwd()`, `tools.VerifyGitRepo`, `checkAndRunSetup`, `config.ResolveConfig`, `checkInitStatus`, and `runEngine`.
- **`checkAndRunSetup(repoRoot string)`** — Returns `error` when the configuration file is missing and stdin is not a terminal.
- **`checkInitStatus(repoRoot, docsDir string, mode engine.RunMode)`** — Returns `error` when the requested init/update mode conflicts with the project's initialization state.
- **`runEngine(repoRoot string, cfg *config.Config, mode engine.RunMode)`** — Wraps engine errors with `fmt.Errorf("documentation run failed: %w", err)`.

### setup.go

The `setup` command (`setupCmd`) implements an interactive CLI setup wizard. It generates or updates a `.code-reducer.yaml` configuration file by collecting the following values from the user:

1. LLM Model ID
2. Ollama Base URL
3. Ollama Context Size (numeric validation with default fallback)
4. Ignore patterns (comma-separated list; supports "clear"/"none" to reset)
5. Documentation directory

The flow loads any existing configuration from the current working directory (falling back to an empty `config.Config{}` if the load fails), applies defaults, prompts sequentially, and persists the result via `config.SaveConfig`.

### update.go

The `update` command (`updateCmd`) performs incremental documentation updates. It identifies files modified since the last documented commit using cached SHA-256 hashes and regenerates the corresponding wiki pages. The operation is tied to the last documented commit as the baseline. No direct I/O is performed in this file; all logic is delegated to `internal/engine` via `executeCommand(engine.ModeUpdate)`.

## Error Propagation

All functions in `cmd` return `error` type. Errors propagate up the call chain via return values. No panics or crashes are present. The cobra framework displays returned errors to the user and exits with a non-zero status code. Errors from `executeCommand` are returned directly to the cobra command framework via `RunE`.

## State and Concurrency

No synchronization mechanisms are present. Global variables (`initCmd`, `setupCmd`, `updateCmd`) are declared at package scope and initialized once; they are never modified after initialization. No mutexes, locks, atomic types, or async patterns are used.