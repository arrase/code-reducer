# CLI Architecture: Code Reducer Command Module

## Module Responsibility

The `cmd` package implements the top-level Cobra command hierarchy for the Code Reducer CLI tool. It owns three operational flows: **initialization** (`init`), **configuration setup** (`setup`), and **update operations** (`update`). Data flow terminates at an engine abstraction that resolves execution semantics per mode argument. Configuration state persists via `.code-reducer.yaml`, validated against LLM credential requirements before any engine invocation.

## Command Registration & Entry Point

### `init.go` — Init Command Instantiation

```go
var initCmd *cobra.Command // package-level singleton
func init() { /* registration */ }
```

- **`initCmd`** instantiates the Cobra subcommand for `init`, binding usage documentation and execution logic that triggers repository scanning and wiki markdown page generation.
- **`init()`** registers `initCmd` on the root command hierarchy at package initialization, ensuring the parser discovers it without explicit user invocation.

### `root.go` — Root Command & Execution Delegation

```go
type root struct { /* Cobra.Command */ }
func executeCommand(mode string) error
func NeedsCredentialSetup(cfg *config.Config) bool
```

- **`RootCmd`** exposes the primary Cobra command instance; all subcommands (`init`, `update`) are children of this node.
- **`executeCommand()`** performs three-stage validation: (1) git repository integrity check, (2) configuration resolution from persisted state, (3) initialization-state verification. It then delegates to an engine based on the supplied mode argument.
- **`NeedsCredentialSetup()`** inspects the resolved project configuration; returns `true` if critical LLM model credentials are absent. Used by `executeCommand()` to gate engine invocation until credential setup completes.

## Configuration Setup Flow

### `setup.go` — Interactive Config Generation

```go
func RunSetupFlow() error
```

- **`RunSetupFlow()`** drives an interactive terminal session that prompts the user for configuration parameters and persists them via `config.SaveConfig`. Output: `.code-reducer.yaml` written to project root. This function is invoked only when credential setup is required or initial configuration does not exist.

## Update Command — Internal Implementation

### `update.go` — Unexported Package-Private Operations

```go
// No exported identifiers. All functions begin with lowercase letters.
```

- Contains no exported functions, variables, structs, interfaces, or data structures. Identifiers (`updateCmd`, internal helpers) are package-private, meaning update-specific logic is scoped to the `cmd` package and not exposed externally.