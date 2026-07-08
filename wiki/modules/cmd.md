## Module: `cmd` — CLI Entry Point & Subcommand Registry

**Responsibility:** Provides a cobra-based command-line interface with root invocation and two subcommands (`setup`, `update`). The module wires all commands into the shared `RootCmd` during package initialization in `init.go`. No exported types exist in that file.

---

### Data Flow

1. User invokes CLI → `RootCmd` dispatches to subcommand via cobra's standard command tree traversal.
2. **setup** flow: prompts user interactively for LLM model ID, Ollama URL/context size, ignore lists, and docs directory; merges into default values or existing list state; persists resulting config through `config.SaveConfig`.
3. **update** flow: detects changed files since the last documented commit (git diff-based), then triggers incremental wiki page regeneration against current source tree.

---

### Root Command (`root.go`)

- **`RootCmd`** — cobra.Command instance configured with usage text and completion options; serves as the top-level entry point for all CLI invocations.
- **`NeedsCredentialSetup()`** (package-level) — Inspects resolved configuration file state; returns `true` when a `ModelID` is absent, signaling that credential setup has not yet been completed.

---

### Setup Subcommand (`setup.go`)

#### Run Setup Flow

- **`RunSetupFlow`** — Orchestrates the interactive configuration sequence:
  - Prompts for LLM model ID and Ollama URL with optional context size parameter.
  - Collects ignore lists via comma-separated input; merges user-supplied values against existing defaults or prior list state.
  - Accepts docs directory path from stdin, falling back to an existing value on empty/failed reads.
  - Persists the aggregated configuration through `config.SaveConfig`.

#### Prompt Utilities

- **`promptAndAppend`** — Reads comma-separated input from stdin; appends tokens to either a default slice or an existing list passed as parameter; returns merged `[]string`.
- **`promptString`** — Reads single-line text from stdin, trims whitespace, and falls back to the provided existing value when input is empty or read fails.

#### Command Registration

- **`setupCmd`** (package-level variable) — Holds the cobra.Command definition for the `setup` subcommand; wired into `RootCmd` during package initialization in `init.go`.

---

### Update Subcommand (`update.go`)

- **`updateCmd`** — Unexported cobra command registered with `RootCmd`; handles incremental wiki page updates by detecting changed files since the last documented commit and triggering regeneration against current source tree.