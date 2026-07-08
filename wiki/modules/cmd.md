# CLI Bootstrap & Initialization Subsystem

## Root Command Entry Point (`root.go`)

**`RootCmd`** serves as the singleton cobra.Command instance representing the top-level "code-reducer" binary. It initializes the command tree, registers subcommands via `AddCommand`, and exposes the root execution context for all downstream operations. All CLI invocations are routed through this entry point before branching into specific subcommand handlers.

## Configuration Validation Gate (`root.go`)

**`NeedsCredentialSetup()`** implements a boolean state check against critical configuration required for model inference. It inspects the application's credential store (e.g., `ModelID`, API keys) to determine if mandatory setup has been completed. Returns `true` when `ModelID` is absent or empty, signaling that execution must halt and the user must run the interactive wizard before further commands are permitted.

## Interactive Setup Flow Execution (`setup.go`)

**`RunSetupFlow()`** orchestrates the full initialization sequence triggered by the validation gate. It performs sequential interactive prompts via stdin/stdout to collect:
*   **LLM Model ID**: Target inference model identifier.
*   **Ollama Base URL**: Endpoint for local LLM hosting (default: `http://localhost:11434`).
*   **Context Size**: Token limit configuration for the session window.
*   **Ignored Paths**: Filesystem patterns to exclude from indexing/processing.
*   **Documentation Directory**: Root path for generated documentation output.

Upon collection, it persists all parameters atomically to a local configuration file (typically YAML or JSON), establishing the baseline state for subsequent CLI invocations. Failure during write-back triggers an error exit code without modifying state.

## Initialization Subcommand Registration (`init.go`)

**`initCmd`** is an unexported `*cobra.Command` definition encapsulating the "init" subcommand logic. It is registered via a lowercase-named `init()` function, which adds it to the `RootCmd` tree during package initialization or explicit registration calls. The visibility constraint (unexported) enforces that this command is not directly accessible from external packages, restricting usage to internal CLI routing only.