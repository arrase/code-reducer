# cmd Package — Architecture Reference

## Module Responsibility & Data Flow

The `cmd` package exposes the CLI surface for **Code-Reducer**, an agent system that scans source repositories and maintains project wikis by orchestrating LLM-based analysis steps (extraction, synthesis, architecture review). The package owns three responsibilities:

1. **Entry-point orchestration** — validate environment state (Git repo presence, TTY availability), resolve configuration from disk or defaults, verify engine mode consistency with project lifecycle state (`init` requires fresh project; `update` requires existing wiki), then instantiate the engine runner and execute the documentation pipeline.
2. **Subcommand registration** — wire `init`, `update`, and `setup` cobra commands into the root command tree via package-level `init()` functions. Each subcommand is constructed once at startup and never mutated after declaration.
3. **Interactive setup wizard** — collect LLM pipeline parameters (model ID, base URL, context size, ignore patterns, docs dir) from stdin using guided prompts, preserve extraction pipeline system prompts from existing config or initialize with built-in defaults, persist the assembled `Config` struct to `.code-reducer.yaml`.

### Data Flow

```
OS environment → os.Getwd() → repoRoot string
repoRoot         → tools.VerifyGitRepo(repoRoot) → bool (git valid?)
repoRoot         → isatty.IsTerminal(os.Stdin.Fd()) / IsCygwinTerminal(...) → bool (TTY?)
repoRoot         → config.ConfigExists(repoRoot) → bool (config file present?)
repoRoot         → config.LoadConfig(repoRoot)    → Config or error
CLI flags        → modelIDFlag, numCtxFlag         → merged with loaded Config
engine.Mode      → engine.NewRunner(cfg).Run(ctx, repoRoot, engine.Mode(mode), callback)
```

Signal handlers registered via `signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)` terminate the background context; errors from `Run()` propagate upward as cancelled errors. The execution path runs sequentially on a single goroutine (flag parsing → config resolution → runner execution). No locks, mutexes, channels with concurrent senders/receivers, or atomic types are present.

---

## Public API Surface — Entry Point (`root.go`)

### `RootCmd *cobra.Command`

Singleton command instance created once at package init. Not mutated after initialization. Serves as the root of the cobra command tree; subcommands register onto it during their respective `init()` functions.

### `executeCommand(mode engine.Mode) error`

Dispatches to the engine runner with a specific mode flag:
- Verifies Git repository presence via `tools.VerifyGitRepo(repoRoot)` — errors returned as-is, no wrapping.
- Resolves configuration (existing file + CLI flags merge). If config absent and not TTY, triggers implicit setup flow guarded by terminal detection.
- Validates engine mode against project state: `init` requires uninitialized project; `update` requires existing wiki. Inconsistent modes fail fast with a descriptive error.
- Instantiates `engine.NewRunner(cfg)`, registers SIGINT/SIGTERM handlers on the background context, and invokes `Run(ctx, repoRoot, engine.Mode(mode), callback)` — wraps its return as `"documentation run failed: %w"`.
- The callback prints `EventStatus`/`EventError` to stdout/stderr; no error propagation from event emission.

### Persistent Flags

| Flag | Storage | Scope |
|------|---------|-------|
| `modelIDFlag string` | cobra flag via `StringVar` on RootCmd | Process lifetime, modified during flag parsing |
| `numCtxFlag string` | same pattern as above | Process lifetime, modified during flag parsing |

---

## Subcommand Registration — Init & Update (`init.go`, `update.go`)

Both files follow the identical thin-wrapper pattern: define an unexported `*cobra.Command`, assign its `RunE` to a function that calls `executeCommand` with a mode constant, and register it under `RootCmd`. No mutable state; no synchronization. Errors propagate through to cobra for exit-code handling.

### Init (`init.go`)

- **Purpose**: Trigger initial documentation generation — scan the target repository and produce the first set of wiki markdown pages from discovered source code.
- **Internal call path**: `RunE` → `executeCommand(engine.ModeInit)` → engine performs repository scan + wiki generation. Actual I/O (disk reads/writes, network calls) occurs inside `internal/engine`; not visible here.

### Update (`update.go`)

- **Purpose**: Incremental documentation updates — scan for source files modified since the last documented state, then update wiki pages to reflect those changes.
- **Internal call path**: `RunE` → `executeCommand(engine.ModeUpdate)` → engine handles update workflow. Same encapsulation: all I/O (file scanning, wiki page generation) is downstream in `internal/engine`.

---

## Interactive Setup Wizard (`setup.go`)

### Domain Problem

Bootstrap the user's first-time or re-configuration experience for Code-Reducer by collecting all necessary LLM pipeline parameters through guided stdin prompts. Generates and persists `.code-reducer.yaml`, used throughout the application lifecycle.

### Functions

| Function | Parameters | Return Type(s) | Description |
|----------|-----------|----------------|-------------|
| `RunSetupFlow(repoRoot string)` | repo root path | `error` | Orchestrates setup: resolve cwd, load existing config if present, prompt for each parameter in interactive order (model ID → base URL → context size with integer validation → ignore patterns → docs dir), preserve extraction pipeline system prompts from existing config or initialize with built-in defaults, assemble new `Config`, persist to disk. |
| `promptStringList(reader *bufio.Reader, promptMsg string, existingList []string)` | stdin reader, prompt message, current list of strings | `([]string, bool)` | Prompts for comma-separated list; returns entered values and whether they were modified (empty → use existing). Stdin read errors written to stderr as warnings; fallback preserves existing list. |
| `promptString(reader *bufio.Reader, promptMsg string, existingVal string)` | stdin reader, prompt message, current value | `string` | Prompts for single string value; returns entered value or default if input empty. Stdin read errors written to stderr; function returns `existingVal`. |

### Configuration Persistence Contract

| Step | Operation | Failure Behavior |
|------|-----------|-----------------|
| 1 | `os.Getwd()` → repoRoot | Wrapped via `fmt.Errorf("%w", err)` and returned to cobra `RunE` handler — setup exits non-zero, error printed to stderr. |
| 2 | `config.LoadConfig(repoRoot)` | If file absent or invalid, returns error → falls back to built-in defaults; no user-visible failure. |
| 3 | `promptString` / `promptStringList` stdin read errors | Error written to stderr as warning; function returns pre-populated default without aborting. |
| 4 | `strconv.Atoi(ctxInputStr)` parse failure or value ≤ 0 | Falls back to `existingNumCtx`. No error propagated upward for this case. |
| 5 | `config.SaveConfig(repoRoot, newCfg)` | Wrapped via `fmt.Errorf("error saving configuration: %w", err)` and returned — setup exits non-zero with descriptive message on stderr. |

### External I/O Summary

All interactions are local (disk + stdin/stderr). No network calls, database access, or API interactions occur in this module. The global `setupCmd *cobra.Command` is constructed once at startup and not concurrently accessed in normal usage.