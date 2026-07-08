# Command-Line Interface Architecture

## Module Responsibility

This module implements a Cobra-based CLI application that manages LLM provider configuration and generates/maintains documentation through an incremental update pipeline. The architecture separates concerns into three phases: initialization (internal wiring), setup (interactive credential collection), and runtime operations (update commands). Data flow proceeds from `RootCmd` initialization → credential validation → setup execution or direct command dispatch.

## Command Hierarchy & Wiring

### Initialization Layer (`init.go`)

Internal-only wiring establishes the Cobra command tree without exported symbols. This layer registers subcommands with the root command, validates command dependencies at package load time, and configures persistent flag inheritance across the hierarchy.

### Root Command (`root.go`)

**`RootCmd`** — Defines the cobra root command for the CLI application. Configures usage text, completion options, and persistent flags shared by all subcommands. Serves as the single entry point for all CLI operations.

**`NeedsCredentialSetup()`** — Returns `true` if the resolved configuration lacks a model ID, indicating that credential setup is required before execution of any command. This function reads from persisted config state to gate subsequent operation flows.

### Setup Command (`cmd/setup.go`)

**`RunSetupFlow()`** — Guides the user through an interactive setup flow to collect:
- LLM Model ID
- Ollama Base URL
- Context size limit
- Ignored file paths
- Documentation directory path

All collected values are persisted into a local configuration file via `config.SaveConfig`. The function handles input validation and persists state atomically.

### Update Command (`update.go`)

**`updateCmd`** — Defines a Cobra command instance that triggers incremental documentation updates by scanning files changed since the last documented commit. Reads diff metadata from version control state to determine which documentation sections require regeneration.

**`init()`** — Registers the update command with `RootCmd` during package initialization to integrate it into the CLI's command hierarchy. This registration occurs at import time via `Command.AddCommand()`.

## Data Flow

```
CLI Invocation → RootCmd.ParseArgs() → NeedsCredentialSetup() check
    ├── true (no model ID) → RunSetupFlow() → config.SaveConfig() → re-invocation
    └── false (config present) → Dispatch to updateCmd or default command
        └── updateCmd.ScanChangedFiles() → DocumentationRegeneration()
```

## Dependency Graph

- `root.go` depends on `setup.go` for credential validation gating
- `update.go` depends on config state established by `setup.go`
- `init.go` provides the internal wiring glue between all exported components