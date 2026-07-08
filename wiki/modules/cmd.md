# cmd Package

## Responsibility

The `cmd` package implements the Cobra command hierarchy for Code-Reducer's CLI application. It defines the root entry point (`RootCmd`) and registers three subcommands—`init`, `setup`, and `update`—each responsible for a distinct phase of the tool lifecycle: initial registration, interactive configuration generation, and runtime updates respectively. Package-level `init()` functions wire all subcommands into the command tree during import.

## Data Flow

```
RootCmd ──────────────┬── init (subcommand)
                      ├── setup (RunSetupFlow → .code-reducer.yaml)
                      └── update (subcommand)
```

- **`init()`** functions in both `init.go` and `update.go` execute during package initialization. The one in `init.go` registers the `init` subcommand; the one in `update.go` registers the `update` subcommand. Both are exported so the command tree is populated before any user invocation.
- **`RootCmd`** serves as the top-level Cobra command. It holds global usage text and disables default completions to enforce explicit completion generation where needed.
- **`NeedsCredentialSetup()`** inspects the current process environment for critical credential variables (e.g., `OLLAMA_HOST`, `LLM_MODEL_ID`). Returns `true` when one or more are absent, signaling that `setup` must be invoked before any authenticated operation.
- **`RunSetupFlow(repoRoot string)`** drives an interactive setup sequence: it prompts the user for LLM model ID, Ollama connection parameters (host, port), context size, ignored path patterns, and documentation directory location. The collected values are serialized into `.code-reducer.yaml` at `repoRoot`.

## Subcommands

### init

```go
func init() { /* registers "init" subcommand with RootCmd */ } // init.go
```

Registers the `init` subcommand under `RootCmd` during package initialization. The cobra command is bound to a run function that executes when the user invokes `code-reducer init`.

### setup

```go
func RunSetupFlow(repoRoot string) { /* orchestrates interactive CLI setup */ } // setup.go
```

Orchestrates an interactive configuration flow. Prompts sequentially for:
1. **LLM model ID** — identifies the target large-language-model endpoint.
2. **Ollama connection parameters** — host, port, and any TLS flags required to reach the local Ollama server.
3. **Context size** — token budget passed to the model during inference.
4. **Ignored paths** — glob patterns for directories/files excluded from analysis.
5. **Documentation directory** — location where generated documentation is written.

The resulting configuration is persisted as `.code-reducer.yaml` at `repoRoot`. This step is a prerequisite when `NeedsCredentialSetup()` returns `true`.

### update

```go
func init() { /* registers "update" subcommand with RootCmd */ } // update.go
```

Registers the `update` subcommand under `RootCmd` during package initialization. The cobra command is bound to its run function and becomes available at the top level alongside `init`, `setup`.