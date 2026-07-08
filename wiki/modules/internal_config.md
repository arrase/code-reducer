# internal/config — Configuration Management Module

## Responsibility & Data Flow

The `internal/config` module centralizes application configuration resolution. It abstracts three concerns: persistent config file I/O (`*.yaml`, 0600 mode), environment variable injection, and runtime parameter discovery (CLI flags → env vars → dynamic host resource scaling). The canonical data flow is **load → parse → apply → persist**, with explicit short-circuit semantics at each stage (silent skip on missing file for `LoadAndApplyConfig`; fixed return value for `GetDatabasePath`).

## Configuration Schema & File I/O

**`internal/config/env.go`**

### Structs

- **`Config`** — schema for `.code-reducer.yaml`. Holds model, Ollama, LangSmith, and LangChain configuration fields plus optional ignore lists and docs directory path.

### Functions

- **`ConfigExists(cwd string) bool`**  
  Returns `true` if a `.code-reducer.yaml` file exists in the specified working directory.

- **`LoadConfig(cwd string) (*Config, error)`**  
  Reads and parses `.code-reducer.yaml` from the given directory into a `*Config` value.

- **`SaveConfig(cwd string, cfg *Config) error`**  
  Marshals the `Config` to YAML and writes it to `.code-reducer.yaml` with mode 0600 in the specified directory.

- **`LoadAndApplyConfig(cwd string) error`**  
  Loads config (silently ignoring missing files), then sets corresponding environment variables only when they are not already present from the parent process.

### Constants

- **`ConfigFileName`** — `.code-reducer.yaml`. Persisted configuration filename.

## Environment Variable Resolution

The module defines explicit env keys and defaults for downstream consumers:

| Constant | Purpose |
|---|---|
| `CodeReducerModelIdEnvKey` | Code Reducer model identifier |
| `OllamaBaseUrlEnvKey` | Ollama base URL |
| `OllamaNumCtxEnvKey` | Ollama context size |
| `LangsmithApiKeyEnvKey` | LangSmith API key |
| `LangchainProjectEnvKey` | LangChain project name |
| `LangchainTracingEnvKey` | LangChain tracing v2 enable/disable |

### Defaults

- **`OllamaDefaultBaseURL`** — `http://localhost:11434`. Default Ollama base URL.
- **`OllamaDefaultNumCtx`** — 8192 tokens. Default Ollama context size.

## Database Path Resolution

### Functions

- **`GetDatabasePath() (string, error)`**  
  Returns the fixed database file path `code-reducer.sqlite` with no error.

## Ollama Context Size Resolution

### Functions

- **`GetOllamaContextSize(flagVal string) int`**  
  Resolves the Ollama context size by checking a CLI flag first, then an environment variable, then dynamically scaling based on host RAM.