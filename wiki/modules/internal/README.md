# Code-Reducer System Architecture Reference

## Subsystem: `internal/config`

### Module Responsibility and Data Flow
The `internal/config` package manages the lifecycle, hierarchical resolution, and persistence of configuration settings. It enables the merging of settings from multiple sources into a unified `Config` struct to direct the LLM-based pipeline.

**Data Flow Pipeline:**
1.  **Ingestion**: Aggregation of data from the local filesystem (`.code-reducer.yaml`), environment variables, and CLI arguments.
2.  **Resolution**: Hierarchical merging based on precedence: CLI flags > Environment variables > Configuration file > System defaults.
3.  **Validation**: Numerical parameters (e.g., `OllamaNumCtx`) are validated to ensure they are non-zero and valid integers.
4.  **Output**: Returns a validated `*Config` pointer for LLM prompt construction and execution logic.
5.  **Persistence**: Configuration changes are serialized to YAML and written via an atomic rename pattern.

### Data Structures

#### `Config`
The primary operational schema for the system.

| Field | Type | Description |
|---|---|---|
| `ModelID` | `string` | Identifier for the LLM model. |
| `OllamaBaseURL` | `string` | Base endpoint for the local Ollama API. |
| `OllamaNumCtx` | `int` | Context window size for the LLM. |
| `DocsDir` | `string` | Target directory for generated documentation. |
| `SystemPrompt` | `string` | Primary system message for the LLM. |
| `ModuleSynthesisPrompt` | `string` | Prompt template for module-level synthesis. |
| `ArchitecturePrompt` | `string` | Prompt template for architectural analysis. |
| `FileFactConsolidationPrompt` | `string` | Prompt template for consolidating file facts. |
| `ExtractionSteps` | `[]ExtractionStep` | Sequence of extraction phases for the pipeline. |
| `Ignore` | `[]string` | Paths or patterns excluded from analysis. |

#### `ExtractionStep`
Defines a specific phase in the multi-step LLM extraction process.

| Field | Type | Description |
|---|---|---|
| `Name` | `string` | Identifier for the extraction phase. |
| `Prompt` | `string` | Specific prompt utilized for this phase. |

### Hierarchical Resolution and Validation
The `ResolveConfig` function determines the final state of the `Config` instance using the following precedence:
1.  **Command-line Flags**: `modelIDFlag`, `numCtxFlag`.
2.  **Environment Variables**: `CODE_REDUCER_MODEL_ID`, `OLLAMA_BASE_URL`, `OLLAMA_NUM_CTX`.
3.  **Configuration File**: Values from `.code-reducer.yaml`.
4.  **System Defaults**: Hardcoded values.

**Constraints:**
*   **Numerical Validation**: `OllamaNumCtx` must be a valid integer and greater than zero. Non-zero/non-integer environment variables result in an error.
*   **Deduplication**: The `Ignore` slice is deduplicated during resolution.
*   **Default Logic**: If `ExtractionSteps` is empty in the config file, the system defaults to the `DefaultExtractionSteps` sequence.

### I/O and Persistence
*   **`ConfigExists`**: Employs `os.Stat` to check for the presence of the configuration file. All errors are swallowed; returns `false` on error.
*   **`LoadConfig`**: Utilizes `os.ReadFile` and `yaml.Unmarshal`. Errors are wrapped and returned.
*   **`SaveConfig`**: Implements an atomic write pattern:
    1.  `yaml.Marshal` the struct to bytes.
    2.  `os.CreateTemp` a temporary file in the target directory.
    3.  Write data and invoke `os.Sync` for durability.
    4.  `os.Chmod` to set permissions.
    5.  `os.Rename` to atomically replace the target file.
    *   **Note**: The deferred `os.Remove` does not contain a `recover()`.

---

## Subsystem: `internal/engine`

### Module Responsibility and Data Flow
The `internal/engine` module is an orchestration engine using a hierarchical Map-Reduce pattern to transform source code into structured documentation via LLMs. It supports `ModeInit` (initial generation) and `ModeUpdate` (incremental updates).

**Data Flow Lifecycle:**
1.  **Initialization**: The `Runner` acquires an exclusive repository lock via `security.AcquireLock`.
2.  **Discovery**: The `Tree` logic converts file paths into a hierarchical `DirNode` structure.
3.  **Change Detection**: The `Orchestrator` compares SHA256 hashes against `MetadataCache`. Changes propagate upward through the `DirNode` tree to mark nodes as "affected."
4.  **Hierarchical Synthesis**: 
    *   **Leaf Processing**: Files undergo `Chunking` (recursive reduction if context limits are exceeded) followed by LLM extraction.
    *   **Aggregation**: Results aggregate from file $\rightarrow$ module $\rightarrow$ directory levels.
5.  **Persistence**: Writes synthesized Markdown and the `MetadataCache` (JSON) to the `docsDir`.

### Core Components

#### Orchestration and Execution
*   **Runner (`runner.go`)**: Entry point; manages execution lifecycle and repository locking.
*   **Orchestrator (`orchestrator.go`)**: Manages cache invalidation, directory pruning, and global (root-level) documentation regeneration.
*   **Synthesizer (`synthesize.go`)**: Traverses the `DirNode` tree; decides between cached summaries or new LLM extractions based on "affected" status.

#### Data Processing
*   **Chunking and Reduction (`chunking.go`)**: Implements recursive reduction for oversized items using expansion (overlapping segments), batching, and consolidation. Recursion terminates if output is $\ge 95\%$ of input.
*   **Markdown Processing (`markdown.go`)**: Strips triple-backtick or JSON fences from LLM output to extract raw content.
*   **Metadata Cache (`cache.go`)**: Persists SHA256 fingerprints. Only version `1` is supported; version mismatches trigger a clean state.
*   **Tree and Change Analysis (`tree.go`)**: Tracks file status (`StatusAdded`, `StatusModified`, `StatusDeleted`) and propagates impact up the directory hierarchy.

#### Infrastructure
*   **LLM Client (`client.go`)**: Synchronous HTTP client for the `api/chat` protocol. Performs no retry logic or circuit breaking.
*   **Constants (`constants.go`)**:
    *   `defaultHTTPTimeout`: 10 minutes.
    *   `contextWindowAllocRatio`: 0.75 (75% for content, 25% for overhead).
    *   `defaultChunkOverlap`: 800 tokens.
    *   `maxErrorBodyBytes`: 1 KB.
*   **Utilities (`utils.go