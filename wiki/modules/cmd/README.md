# Module: `cmd`

## Module Overview
The `cmd` module serves as the command-line interface (CLI) orchestration layer. It manages the lifecycle of project documentation generation by coordinating user input, configuration persistence, git repository validation, and the execution of the underlying documentation engine.

## Data Flow
1.  **Input Acquisition**: CLI arguments are parsed via `RootCmd`. User-provided flags (`--model-id`, `--num-ctx`) and interactive input (`os.Stdin`) are captured.
2.  **Environment Validation**: The module verifies the presence of a git repository and determines the current working directory via `os.Getwd`.
3.  **State Assessment**: The module checks for existing configuration files and determines if the project is in an initialized state to validate the requested operation (`init` vs. `update`).
4.  **Configuration Resolution**: Configuration is loaded from disk, merged with defaults, and optionally updated via an interactive setup flow.
5.  **Execution Dispatch**: The validated configuration and mode are passed to the `engine` package to perform documentation generation or incremental updates.
6.  **Output Stream**: Results, status updates, and errors are streamed to `stdout` and `stderr`.

## Core Components

### Command Orchestration (`root.go`)
The central logic for the CLI lifecycle is contained within this file.

*   **`RootCmd`** (`*cobra.Command`): The entry point for the CLI application.
*   **`executeCommand(mode engine.Mode) error`**: The primary controller. It performs the following sequence:
    *   Resolves the working directory.
    *   Verifies the git repository status.
    *   Triggers `checkAndRunSetup` if configuration is missing and `os.Stdin` is a TTY.
    *   Resolves the final configuration.
    *   Validates that the current project state matches the requested `engine.Mode`.
    *   Dispatches execution to `runEngine`.
*   **`checkAndRunSetup(repoRoot string) error`**: Evaluates if `RunSetupFlow` is required based on the existence of configuration files and terminal availability.
*   **`checkInitStatus(repoRoot, docsDir string, mode engine.Mode) error`**: Enforces state transition rules:
    *   `ModeInit` is prohibited if the project is already initialized.
    *   `ModeUpdate` is prohibited if the project is not yet initialized.
*   **`runEngine(repoRoot string, cfg *config.Config, mode engine.Mode) error`**: Orchestrates engine execution with signal handling for `os.Interrupt` and `syscall.SIGTERM`.

### Command Definitions (`init.go`, `update.go`)
These files act as thin wrappers that map CLI subcommands to specific engine modes.

*   **`init` subcommand**: Dispatches `engine.ModeInit` to trigger the initial scanning of the repository and generation of wiki markdown files.
*   **`update` subcommand**: Dispatches `engine.ModeUpdate` to perform incremental updates by comparing current file states against the last documented git commit.

### Configuration Lifecycle (`setup.go`)
Handles the interactive provisioning of project settings.

*   **`RunSetupFlow(repoRoot string) error`**: Manages a stateful, interactive session to collect:
    *   LLM Model ID.
    *   Ollama Base URL.
    *   Ollama Context Size.
    *   File/directory ignore patterns.
    *   Documentation directory paths.
*   **Persistence**: Finalized settings are consolidated into a `config.Config` object and written to disk via `config.SaveConfig`.

## State and Side Effects

### Mutable State
*   **Global Flags**: `modelIDFlag` and `numCtxFlag` are modified during the `init()` phase and persist for the lifetime of the process.
*   **Command State**: `RootCmd` state is modified via `.SetArgs()` and `.Execute()` during testing.

### External I/O
*   **Filesystem**:
    *   Directory/file creation via `os.MkdirAll` and `os.WriteFile`.
    *   Configuration persistence via `config.SaveConfig` and `config.LoadConfig`.
    *   Git verification via `tools.VerifyGitRepo`.
    *   State check via `engine.IsInitialized`.
*   **Standard Streams**:
    *   `os.Stdin`: Used for interactive parameter collection.
    *   `os.Stdout`: Used for status messages and engine events.
    *   `os.Stderr`: Used for error reporting.

### Error Handling
*   **Propagation**: Errors are passed up the call stack via return values.
*   **Wrapping**: `os.Getwd` and `runner.Run` errors are wrapped with context using `%w`.
*   **Swallowed Errors**: 
    *   `config.LoadConfig` errors are swallowed in `RunSetupFlow` (defaults are returned instead).
    *   `strconv.Atoi` errors in `promptContextSize` are swallowed (default values are returned instead).
    *   `RootCmd.Execute()` return values are swallowed in `cmd_test.go`.