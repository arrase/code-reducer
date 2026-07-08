# Module: `cmd` — CLI Entry Point & Command Lifecycle

## Responsibility

The `cmd` package implements the Cobra-based command-line interface for **code-reducer**. It manages the lifecycle from application bootstrap through credential validation, interactive setup, configuration loading, repository locking, and LLM engine execution with live event streaming. The module registers subcommands at package init time and delegates orchestration to a root command that enforces sequential state transitions.

## Data Flow

```
init.go (package init) 
    ↓ registers "init" subcommand
RootCmd (root.go — Cobra root)
    ↓ executeCommand()
NeedsCredentialSetup() → bool
    ├─ false ───→ acquire lock → load config → run LLM engine + stream events
    └─ true ────→ RunSetupFlow() → save .code-reducer.yaml → re-attempt executeCommand()
```

## Subcommand Registration

### `init` (init.go)

Executes at package initialization. Registers the `init` subcommand on the root Cobra command, enabling a self-healing bootstrap path where users can invoke `code-reducer init` to trigger interactive setup if credentials are absent.

## Root Command & Execution Orchestrator

### `RootCmd` (root.go)

The top-level Cobra command for the `code-reducer` CLI. Owns the application's command tree and serves as the entry point for all user-invoked operations. Delegates to `executeCommand()` on invocation; does not perform work directly.

### `executeCommand` (root.go)

Orchestrates the main execution flow:

1. **Credential gating** — calls `NeedsCredentialSetup()`. If no `MODEL_ID` environment variable is set, the function returns `true`, signaling that critical credentials are missing.
2. **Lock acquisition** — acquires a repository-level lock to serialize concurrent invocations and prevent state corruption during setup or engine execution.
3. **Configuration loading** — reads `.code-reducer.yaml` from the working directory. If the file is absent, triggers `RunSetupFlow()` instead of proceeding.
4. **LLM engine invocation** — runs the model inference pipeline with live event streaming for real-time output consumption by the caller.

### `NeedsCredentialSetup` (root.go)

Boolean predicate that evaluates the absence of a required environment variable (`MODEL_ID`). Returns `true` when credentials are unconfigured, causing `executeCommand` to redirect into setup mode rather than execution.

## Interactive Configuration Flow

### `RunSetupFlow` (setup.go)

Guides the user through an interactive configuration session. Prompts for missing values and writes a `.code-reducer.yaml` file to disk. Upon completion, returns control to `executeCommand`, which re-acquires the lock and reloads configuration before proceeding with engine execution.