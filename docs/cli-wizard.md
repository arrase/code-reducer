# CLI Commands & Setup Wizard

Code-Reducer provides a streamlined command-line interface (CLI) for configuring, initializing, and updating codebase wikis. This document details all CLI subcommands, interactive wizard behaviors, implicit setup detection, and command-line parameter overrides.

---

## CLI Subcommands Reference

Code-Reducer features three main subcommands:

```
code-reducer [subcommand] [flags]
```

### 1. `code-reducer setup`

Launches the interactive terminal setup wizard to create or update `.code-reducer.yaml`.

```bash
code-reducer setup
```

- **Purpose**: Interactively prompts for model selection, connection details, context window size, ignore patterns, and target documentation directory.
- **Behavior**: Reads existing `.code-reducer.yaml` if present and supplies current settings as prompt defaults.

---

### 2. `code-reducer init`

Generates the initial repository documentation set and metadata cache.

```bash
code-reducer init [--model-id <model>] [--num-ctx <size>] [--think <bool>] \
                  [--num-predict <n>] [--slot-num-predict <n>] \
                  [--paragraph-num-predict <n>] \
                  [--chars-per-token <f>] [--output-token-reserve <n>] \
                  [--include-tests <bool>]
```

- **Purpose**: Performs full codebase discovery, builds the hierarchical tree, extracts facts across files, and produces directory module summaries alongside root blueprints.
- **Artifacts Created**:
  - `<docs_dir>/` containing module documentation subdirectories.
  - `<docs_dir>/architecture.md` (System overview blueprint).
  - `<docs_dir>/quickstart.md` (Developer onboarding guide).
  - `<docs_dir>/.metadata.json` (SHA256 hash & facts cache).
  - `AGENTS.md` (Root AI agent navigation guide).
- **Constraints**: Fails fast if `<docs_dir>/.metadata.json` already exists. Use `update` for existing projects.

---

### 3. `code-reducer update`

Performs an incremental documentation update targeting only modified files and affected modules.

```bash
code-reducer update [--model-id <model>] [--num-ctx <size>]
```

- **Purpose**: Computes SHA256 hashes of current repository files, identifies modified, added, or deleted files against `<docs_dir>/.metadata.json`, and rebuilds only impacted modules.
- **Bottom-Up Propagation**: When a file changes, its parent directory module is rebuilt, propagating up the tree to refresh affected parent summaries.
- **Cache Invalidation**: Detects changes in `.code-reducer.yaml` extraction steps and automatically invalidates outdated file facts.
- **Constraints**: Fails fast if the project has not been initialized yet (`code-reducer init` required).
- **Failure Modes**: A generation that stopped on the generation limit is kept only up to its complete sentences: a one-line slot keeps its first complete sentence and a paragraph slot keeps the text up to its last complete sentence. The run aborts only when a slot answer holds no complete sentence, when a fact extraction call is truncated, or when an answer has no visible content, and then it aborts with the failing call and the token counts; nothing is written and nothing is cached, so re-running resumes from the last completed state. The truncation message names `num_predict`, `slot_num_predict`, `paragraph_num_predict` and `think: false`, because a reasoning model with thinking enabled charges its hidden reasoning against the same slot budget (400 tokens for a one-line slot instead of 34).

---

## Interactive Setup Wizard Flow

Running `code-reducer setup` initiates an interactive step-by-step terminal prompt flow:

```
Welcome to Code-Reducer CLI Setup
---------------------------------
Enter LLM Model ID [ornith:9b]: 
Enter Ollama Base URL [http://localhost:11434]: 
Enter Ollama Context Size [8192]: 20000
Enter directories, files, or patterns to ignore (comma-separated): screenshots, docs, examples
Enter documentation directory [wiki]: 
Configuration successfully saved to local .code-reducer.yaml file.
```

### Prompt Sequence & Defaults

1. **LLM Model ID**: Model loaded into local Ollama (Default: `ornith:9b`).
2. **Ollama Base URL**: API endpoint URL (Default: `http://localhost:11434`).
3. **Context Size (`num_ctx`)**: Context window in tokens (Default: `8192`).
4. **Ignore Patterns**: Comma-separated list of files or directories to ignore. Passing `clear` or `none` empties existing custom ignore lists.
5. **Documentation Directory**: Output directory for generated docs (Default: `wiki`).

If `.code-reducer.yaml` already exists when `setup` is executed, the wizard populates every prompt default using values from the existing YAML file, and carries every key it does not ask about (the prompts, the extraction steps, the generation budgets and `include_tests`) through to the saved file unchanged.

---

## Implicit Wizard Launching

If you run `code-reducer init` or `code-reducer update` in a workspace where `.code-reducer.yaml` does not exist, Code-Reducer handles setup dynamically based on your terminal environment:

```mermaid
graph TD
    A[Run init or update] --> B{.code-reducer.yaml exists?}
    B -- Yes --> C[Load Config & Execute Pipeline]
    B -- No --> D{Is Interactive Terminal / TTY?}
    D -- Yes --> E[Implicitly Launch Setup Wizard]
    E --> F[Save .code-reducer.yaml & Proceed]
    D -- No --> G[Fail Fast with Error Message]
```

### Terminal (TTY) vs Non-Interactive (CI/CD)

- **Interactive TTY**: If stdin is attached to a terminal, Code-Reducer automatically pauses execution, runs `RunSetupFlow()`, writes `.code-reducer.yaml`, and seamlessly proceeds with `init` or `update`.
- **Non-TTY / CI Pipeline**: If stdin is detached (e.g. Docker build or GitHub Actions), Code-Reducer halts immediately with an error:

```
Error: configuration file .code-reducer.yaml does not exist in the current directory. Please run 'code-reducer setup' to configure the application
```

---

## Parameter Overrides via CLI Flags & Environment Variables

You can temporarily override configuration values without altering `.code-reducer.yaml`.

### CLI Persistent Flags

Code-Reducer accepts persistent flags on `init` and `update`:

| Flag | Type | Description | Example |
| :--- | :--- | :--- | :--- |
| `--model-id` | `string` | Override LLM model ID | `--model-id gemma4:26b` |
| `--num-ctx` | `string` | Override Ollama context window size | `--num-ctx 16384` |
| `--think` | `bool` | Reasoning output. Required to be `false` on a reasoning model such as `ornith:9b` (default `false`) | `--think=false` |
| `--num-predict` | `int` | Client-wide cap on generated tokens per call (default `0`, Ollama decides) | `--num-predict 2000` |
| `--slot-num-predict` | `int` | Cap on generated tokens for each one-line documentation slot (default `192`) | `--slot-num-predict 256` |
| `--paragraph-num-predict` | `int` | Cap on generated tokens for each documentation paragraph slot (default `1024`) | `--paragraph-num-predict 1536` |
| `--chars-per-token` | `float` | Characters per token used to size prompt payloads (default `3.0`) | `--chars-per-token 3.5` |
| `--output-token-reserve` | `int` | Context tokens held back from prompt payloads for generation (default `1024`) | `--output-token-reserve 1536` |
| `--include-tests` | `bool` | Document test files as well (default `false`) | `--include-tests=true` |

A `--num-predict` lower than either slot cap also caps that slot kind, so one global
ceiling can be lowered without retuning the slot budgets.

#### Flag Examples

```bash
# Run initial analysis using a larger model and 16k context window
code-reducer init --model-id gemma4:26b --num-ctx 16384

# Run incremental update overriding only the model
code-reducer update --model-id ornith:9b

# Document a repository that contains its own tests
code-reducer init --include-tests=true
```

### Environment Variable Overrides

Environment variables override values in `.code-reducer.yaml` but yield to explicit CLI flags:

| Environment Variable | Target Parameter | Description |
| :--- | :--- | :--- |
| `CODE_REDUCER_MODEL_ID` | `model_id` | Overrides the LLM model ID. |
| `OLLAMA_BASE_URL` | `ollama_base_url` | Overrides the Ollama API server URL. |
| `OLLAMA_NUM_CTX` | `ollama_num_ctx` | Overrides the Ollama context window size. |
| `CODE_REDUCER_THINK` | `think` | Enables or disables reasoning output. |
| `OLLAMA_NUM_PREDICT` | `num_predict` | Overrides the client-wide generation cap. |
| `CODE_REDUCER_SLOT_NUM_PREDICT` | `slot_num_predict` | Overrides the one-line documentation slot generation cap. |
| `CODE_REDUCER_PARAGRAPH_NUM_PREDICT` | `paragraph_num_predict` | Overrides the documentation paragraph slot generation cap. |
| `CODE_REDUCER_INCLUDE_TESTS` | `include_tests` | Includes or excludes test files. |

#### Environment Variable Examples

```bash
# Point to a remote Ollama server instance
export OLLAMA_BASE_URL="http://192.168.1.100:11434"
export CODE_REDUCER_MODEL_ID="gemma4:26b"
export OLLAMA_NUM_CTX=32768

code-reducer update
```
