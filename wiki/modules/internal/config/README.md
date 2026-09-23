# Package `internal/config`

## Module Responsibility
The `internal/config` package manages the lifecycle, hierarchical resolution, and persistence of configuration settings for the Code-Reducer system. It provides the mechanism to merge settings from multiple sources—including command-line flags, environment variables, and configuration files—into a unified `Config` struct. This configuration directs an LLM-based pipeline through specific extraction and synthesis phases.

### Data Flow
1.  **Ingestion**: The system gathers data from the local filesystem (via `.code-reducer.yaml`), environment variables, and CLI arguments.
2.  **Resolution**: A hierarchical merge process applies overrides based on a defined priority: CLI flags > Environment variables > Configuration file values > System defaults.
3.  **Validation**: Numerical parameters, such as context size, undergo validation to ensure they meet non-zero requirements.
4.  **Output**: A validated `*Config` pointer is returned for use in LLM prompt construction and execution logic.
5.  **Persistence**: Configuration changes are serialized to YAML and written to the filesystem using an atomic rename pattern to ensure integrity.

## Data Structures

### `Config`
The primary schema representing the system's operational parameters.

| Field | Type | Description |
|---|---|---|
| `ModelID` | `string` | The identifier for the LLM model. |
| `OllamaBaseURL` | `string` | The base endpoint for the local Ollama API. |
| `OllamaNumCtx` | `int` | The context window size for the LLM. |
| `DocsDir` | `string` | The target directory for generated documentation. |
| `SystemPrompt` | `string` | The primary system message for the LLM. |
| `ModuleSynthesisPrompt` | `string` | Prompt template for module-level synthesis. |
| `ArchitecturePrompt` | `string` | Prompt template for architectural analysis. |
| `FileFactConsolidationPrompt` | `string` | Prompt template for consolidating file facts. |
| `ExtractionSteps` | `[]ExtractionStep` | A sequence of extraction phases for the analysis pipeline. |
| `Ignore` | `[]string` | A list of paths or patterns to be excluded from analysis. |

### `ExtractionStep`
Defines a single phase in the multi-step LLM extraction process.

| Field | Type | Description |
|---|---|---|
| `Name` | `string` | The identifier for the extraction phase. |
| `Prompt` | `string` | The specific prompt utilized for this phase. |

## Hierarchical Resolution

The `ResolveConfig` function implements the precedence logic to determine the final state of a `Config` instance.

### Resolution Precedence
For each parameter, the first non-default/non-empty value found in the following order is selected:
1.  **Command-line Flags** (e.g., `modelIDFlag`, `numCtxFlag`).
2.  **Environment Variables** (defined by `CodeReducerModelIDEnvKey`, `OllamaNumCtxEnvKey`, and `OllamaBaseURLEnvKey`).
3.  **Configuration File** (values stored in `.code-reducer.yaml`).
4.  **System Defaults** (hardcoded values).

### Numerical Validation
The resolution of `OllamaNumCtx` requires that the value—whether sourced from a flag, environment variable, or file—is a valid integer and greater than zero. If an environment variable is non-empty but fails `strconv.Atoi` or is $\le 0$, `ResolveConfig` returns an error.

### List and Default Initialization
*   **Ignore List**: The `Ignore` slice is deduplicated during resolution.
*   **Extraction Steps**: If the configuration file provides an empty list for `ExtractionSteps`, the system defaults to the `DefaultExtractionSteps` slice.

## I/O and Persistence

### Filesystem Operations
The module utilizes a `ConfigFileName` (`.code-reducer.yaml`) within a specified directory to manage persistence.

*   **`ConfigExists`**: Checks for the existence of the configuration file using `os.Stat`. All errors encountered during this operation are swallowed, and the function returns `false`.
*   **`LoadConfig`**: Reads the configuration file via `os.ReadFile` and unmarshals the content via `yaml.Unmarshal`. Errors from file reading or YAML parsing are wrapped and returned to the caller.
*   **`SaveConfig`**: Implements an atomic write pattern to prevent file corruption:
    1.  Serializes the `Config` struct to YAML bytes via `yaml.Marshal`.
    2.  Creates a temporary file in the target directory using `os.CreateTemp`.
    3.  Writes the data and ensures durability via `os.Sync`.
    4.  Sets file permissions via `os.Chmod`.
    5.  Closes the file and uses `os.Rename` to atomically replace the target file.
    *   **Note**: The deferred cleanup (via `os.Remove`) in `SaveConfig` does not contain `recover()`; panics during the deferred execution are not handled.

## Constants and Defaults

### Environment Variable Keys
| Key | Purpose |
|---|---|
| `CODE_REDUCER_MODEL_ID` | Overrides the LLM `ModelID`. |
| `OLLAMA_BASE_URL` | Overrides the `OllamaBaseURL`. |
| `OLLAMA_NUM_CTX` | Overrides the `OllamaNumCtx`. |

### Default Extraction Pipeline (`DefaultExtractionSteps`)
When no custom extraction steps are provided, the system executes these four stages:
1.  **`API_SIGNATURES`**: Extracts public types, functions, and methods.
2.  **`BUSINESS_LOGIC`**: Synthesizes the primary domain problem and high-level algorithmic steps.
3.  **`STATE_AND_CONCURRENCY`**: Identifies mutable state and synchronization mechanisms. Returns "No mutable state" if none are found.
4.  **`ERRORS_AND_SIDE_EFFECTS`**: Details external I/O interactions and error propagation. Returns "No external side effects" if no I/O is detected.