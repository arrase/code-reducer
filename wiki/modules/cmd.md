# cmd Package — CLI Orchestration Layer

## Module Responsibility

The `cmd` package implements the top-level command-line interface (CLI) using Cobra. It provides a root command with persistent global flags (`--model-id`, `--num-ctx`) that propagate to all subcommands, registers an interactive setup flow for initial configuration generation, and exposes an `update` subcommand for synchronizing wiki pages against repository changes since the last documented commit.

## Data Flow

```
user input (CLI args) → RootCmd.ParseFlags() → executeCommand(mode, userMessage)
    │                                              │
    ├── config resolution                         ├── signal handling
    ├── repository validation                     └── engine execution + status/error event logging
    └── NeedsCredentialSetup() check              ←  merge config resolution
```

## Initialization and Package Setup

The package-level `init()` function registers two persistent flags on the root command:

- **`--model-id`** — LLM model identifier (user-provided)
- **`--num-ctx`** — Ollama context window size (user-provided)

Both are stored in package-level string variables (`modelIdFlag`, `numCtxFlag`) and remain accessible throughout the CLI lifecycle. All identifiers in `init.go` are lowercase and private; no exported symbols exist from this file.

## Root Command Entry Point

**`RootCmd`** — Cobra command instance serving as the top-level entry point for all subcommand dispatching. The root command is constructed once during package initialization; subsequent invocations route to `executeCommand`.

### Execution Orchestration

**`executeCommand(mode, userMessage)`** orchestrates the full documentation workflow:
1. **Repository validation** — verifies the target repository state
2. **Implicit setup flow** — triggers interactive config generation when no `.code-reducer.yaml` exists (via `RunSetupFlow`)
3. **Merged configuration resolution** — combines global, repo-level, and user-provided settings
4. **Mode-specific checks** — validates required arguments for `init`/`update` modes
5. **Signal handling** — installs SIGINT/SIGTERM handlers to gracefully terminate engine execution
6. **Engine execution** — invokes the documentation engine with status/error event logging

### Credential Setup Check

**`NeedsCredentialSetup()`** returns a boolean indicating whether critical configuration is missing by resolving the current config state and checking whether a model ID has been set. Used during mode-specific validation to prompt credential setup when necessary.

## Interactive Configuration Flow

**`RunSetupFlow`** — guides the user through an interactive session that generates or updates the local `.code-reducer.yaml` configuration file. It reads inputs via `bufio.Scanner`/prompt (package-private) and persists them by calling `config.SaveConfig`. This function is invoked implicitly when no existing config is detected during `executeCommand`.

## Update Subcommand

**`updateCmd`** — a Cobra command defining the "update" subcommand. Responsibilities:
- Scans changed files since the last documented commit (diff against HEAD of the main branch)
- Updates corresponding wiki pages with an optional user-supplied message argument
- Routes through `executeCommand` for full orchestration (config resolution, signal handling, engine execution)

## Component Summary

| File | Exported Symbols | Role |
|---|---|---|
| `init.go` | none | Package-private init registration of persistent flags |
| `root.go` | `RootCmd`, `modelIdFlag`, `numCtxFlag`, `executeCommand`, `NeedsCredentialSetup()` | CLI entry point, orchestration engine, credential validation |
| `setup.go` | `RunSetupFlow` | Interactive config generation/update flow |
| `update.go` | `updateCmd` | Wiki page sync subcommand |