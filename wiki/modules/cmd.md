# cmd Package Documentation

## Module Responsibility

The `cmd` package implements the CLI command surface for code-reducer using Cobra. It provides three subcommands (`init`, `update`) and an interactive setup flow that drives repository scanning, wiki page generation, configuration management, and credential resolution. Data flow proceeds from root orchestration → initialization/credential validation → configuration resolution → engine execution.

## Root Command & Orchestration

| Component | Location | Responsibility |
|-----------|----------|-----------------|
| `RootCmd` | `root.go` | Cobra root command instance; completion options disabled globally. |
| `executeCommand(mode string)` | `root.go` | Orchestrates the full documentation workflow: validates git repository, runs implicit setup if needed, resolves merged configuration, checks initialization state via `.metadata.json`, and executes the engine while handling terminal signals. |
| `NeedsCredentialSetup()` | `root.go` | Returns `true` when the resolved configuration lacks a critical LLM model ID; indicates credential setup is required before engine execution. |

**Data flow**: `RootCmd` → `executeCommand(mode)` → git validation → implicit setup (if missing) → configuration merge → `.metadata.json` check → `NeedsCredentialSetup()` gate → engine dispatch with signal handling.

## Initialization Subcommand

| Component | Location | Responsibility |
|-----------|----------|-----------------|
| `initCmd` | `init.go` | Defines the CLI subcommand that triggers repository scanning and generates the initial set of wiki markdown pages when executed. |

**Data flow**: `initCmd` → repository scan → wiki page generation → `.metadata.json` write-back (initialization state).

## Update Subcommand

| Component | Location | Responsibility |
|-----------|----------|-----------------|
| `updateCmd` | `update.go` | A cobra.Command variable that defines the "update" subcommand; scans changed files since the last documented commit and updates wiki pages. |
| `init()` | `update.go` | Package-level init function registered at startup to register `updateCmd` with the root command (`RootCmd`). |

**Data flow**: `init()` → Cobra registration → user invokes `update` subcommand → diff against last documented commit → wiki page regeneration → metadata refresh.

## Setup Flow

| Component | Location | Responsibility |
|-----------|----------|-----------------|
| `RunSetupFlow` | `setup.go` | Guides the user through an interactive setup flow to generate and save a `.code-reducer.yaml` configuration file by prompting for model ID, Ollama Base URL, context size, ignored directories/files, ignored extensions, and documentation directory. |

**Data flow**: CLI prompt → parameter collection (model ID, Ollama Base URL, context size, ignore paths/extension, doc dir) → `.code-reducer.yaml` generation → file write-back → configuration ready for `executeCommand`.

## Command Registration Order

1. Package init (`update.go`) registers `updateCmd` with `RootCmd`.
2. User invokes `init`, `update`, or setup flow via CLI.
3. Setup flow populates `.code-reducer.yaml` if absent; otherwise loads merged configuration.
4. `executeCommand(mode)` validates state and dispatches engine.