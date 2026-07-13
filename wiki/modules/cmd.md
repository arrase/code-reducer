# cmd Package — CLI Command Registry & Execution Engine

## Module Responsibility

The `cmd` package implements the top-level CLI entry point for Code-Reducer, a tool that generates and maintains project wiki documentation via an LLM engine. It registers cobra commands (`init`, `update`), validates preconditions (Git repo presence, initialization state), resolves configuration from flags + on-disk settings + interactive prompts, and delegates execution to the engine runner while streaming status/error events through stdout/stderr.

---

## Data Flow Overview

```
┌─────────────┐     ┌──────────────┐     ┌─────────────────┐     ┌──────────────┐
│ RootCmd      │────▶│ executeCommand│────▶│ engine.Run(...)  │────▶│ Config       │
│ (entry point) │    │("init"|"update")│   │                  │    │ SaveConfig    │
└─────────────┘     └──────────────┘     └─────────────────┘     └──────────────┘
      │                    │                     │                      │
      ▼                    ▼                     ▼                      ▼
  Git check            mode guard           event callback        file write
```

Errors flow as wrapped Go errors through cobra's `RunE` interface. Event-emitted errors (via callback) are printed to stderr and swallowed; only the runner's return error is re-wrapped with a descriptive prefix before propagation.

---

## init Command Registration (`cmd/init.go`)

### API Surface

- `initCmd` → unexported `*cobra.Command`, registered under `RootCmd`
- `init()` → package-level `init` function that binds the command to cobra's registry

### Execution Path

The `RunE` closure delegates directly to `executeCommand("init")`. The actual implementation is not contained in this file. Errors propagate through cobra's command framework and exit via non-zero process code when a non-nil error is returned. No panic or crash path is introduced here.

---

## Root Command & Execution Loop (`cmd/root.go`)

### Public API

| Identifier | Type | Scope |
|---|---|---|
| `RootCmd` | `*cobra.Command` | package-level; initialized inline, never reassigned |

All other declarations (`modelIDFlag`, `numCtxFlag`, `executeCommand`) are unexported.

### Precondition Validation Sequence

1. **Git Repository Check** — `os.Getwd()` captures the current working directory; `tools.VerifyGitRepo(repoRoot)` checks for `.git` existence and equivalent state. Errors from this path propagate through cobra as wrapped errors via `%w`.
2. **Configuration Existence** — `config.ConfigExists(repoRoot)` reads the local filesystem to confirm a configuration file is present. If missing *and* stdin is not a terminal (TTY), an implicit setup flow runs: `RunSetupFlow(repoRoot)` creates one and returns any error as-is.
3. **Mode Guard** — `engine.IsInitialized(...)` validates that the requested operation matches the current state:
   - `init` mode fails if documentation has already been initialized → suggests using `update` instead
   - `update` mode fails if initialization has not occurred yet → requires running `init` first

### Configuration Resolution

`config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag)` merges CLI flags (`--model-id`, `--num-ctx`) with on-disk settings and environment variables. Returns the resolved config or a raw error from the config package (no wrapping). The LLM model ID flows through but is not consumed in this file; it persists for later use by the engine.

### Signal Handling & Execution Loop

`signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)` registers interrupt and termination signals on a background context. The handler calls `stop()` via deferred execution, enabling graceful shutdown propagation to the engine runner. This is the only concurrency primitive in this file; no goroutines or mutexes are used.

The resolved configuration is passed to `runner.Run(ctx, repoRoot, engine.Mode(mode), ...)`. Status events print to stdout (`fmt.Println(ev.Message)`), error events print to stderr (`fmt.Fprintf(os.Stderr, "Error: %s\n", ev.Message)`), and other events print to stdout. Errors from the runner are wrapped with `"documentation run failed: %w"` before returning.

---

## Interactive Setup Wizard (`cmd/setup.go`)

### Public API Surface

| Function | Signature | Purpose |
|---|---|---|
| `RunSetupFlow` | `func RunSetupFlow(repoRoot string) error` | Orchestrates the full interactive configuration flow; returns non-nil only for filesystem-level failures (`os.Getwd`, `config.SaveConfig`) |
| `promptStringList` | `func promptStringList(reader *bufio.Reader, promptMsg string, existingList []string) ([]string, bool)` | Collects comma-separated or list-style input with an existing value fallback; returns `(result, modified)` where `modified=false` signals no change |
| `promptString` | `func promptString(reader *bufio.Reader, promptMsg string, existingVal string) string` | Collects single-string input with a default shown in brackets; returns trimmed input or `existingVal` on empty/error |

### Algorithm Steps

1. **Repo Root Resolution** — `os.Getwd()` captures the base path at start of `RunSetupFlow`.
2. **State Loading** — `config.LoadConfig(repoRoot)` reads previously saved configuration; non-empty values (model ID, URL, context size, ignore list, docs dir) are extracted for pre-population.
3. **Prompt Sequence** — Four prompts run in order: LLM Model ID, Ollama Base URL, Context Size, Documentation Directory. Each uses the `promptString`/`promptStringList` pattern with existing values as fallbacks.
4. **System Prompt Preservation** — Existing values for extraction, synthesis, architecture, and file fact consolidation prompts are carried forward when present.
5. **Configuration Assembly** — All user inputs merge with defaults/previous state into a single `Config` struct.
6. **Persistence** — `config.SaveConfig(repoRoot, newCfg)` writes the assembled config to disk. Errors bubble up un-wrapped.

### Error Handling Strategy

- `promptString` / `promptStringList`: swallow `ReadString` errors → print warning to stderr → return existing value with no propagation beyond the function
- `strconv.Atoi(ctxInputStr)`: swallow parse failure or non-positive result → fall back to `existingNumCtx`; no error propagates
- Only filesystem-level failures (`Getwd`, `SaveConfig`) propagate up the call stack

---

## Update Command (`cmd/update.go`)

### API Surface

All identifiers (`updateCmd`, etc.) begin with lowercase letters and are unexported. No public surface exists in this file.

### Execution Path

The `RunE` closure delegates to `executeCommand("update")`. The actual implementation is not contained in this file; errors propagate through cobra's command framework as returned by the delegate. No panic or crash recovery is handled within this file.