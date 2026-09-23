# Code-Reducer Technical Overview

## System Architecture

Code-Reducer is a command-line orchestration tool that transforms source code into structured documentation using a Map-Reduce synthesis pattern. The system operates within a local boundary, interacting with the local filesystem and local Large Language Models (LLMs) via the `api/chat` protocol.

### Module Interaction & Data Flow

The system architecture is composed of three primary internal layers and external configuration files:

#### 1. Orchestration Layer (`cmd`)
Acts as the entry point and lifecycle controller.
*   **Validation**: Performs environment checks (git repository existence and working directory identification).
*   **State Management**: Evaluates `engine.IsInitialized` to determine if the operation is a full `init` or an incremental `update`.
*   **Dispatch**: Validates the environment and passes the `config.Config` to the `engine` package.

#### 2. Configuration Layer (`internal/config`)
Manages hierarchical parameter resolution with the following precedence: 
`CLI flags` $\rightarrow$ `Environment variables` $\rightarrow$ `.code-reducer.yaml` $\rightarrow$ `System defaults`.
*   **Key Parameters**: `ModelID`, `OllamaBaseURL`, `OllamaNumCtx`, and `DocsDir`.
*   **Extraction Steps**: Defines `ExtractionStep` objects mapping specific phases of the synthesis pipeline to prompt templates.

#### 3. Map-Reduce Engine (`internal/engine`)
The core computational component responsible for hierarchical content reduction.
*   **Discovery & Mapping**: Files are mapped into a `DirNode` tree structure.
*   **Change Detection**: Uses `MetadataCache` to compare current file SHA256 fingerprints against previous states. Changes propagate "Affected" status up the `DirNode` tree.
*   **Hierarchical Synthesis**:
    *   **Leaf Processing**: Uses `Chunking` for segments exceeding the context window (using a `0.75` `contextWindowAllocRatio`).
    *   **Recursion**: If a segment is oversized, recursive reduction is applied. Recursion terminates if the output size is $\ge$ 95% of the input.
    *   **Aggregation**: Summaries are aggregated from file-level $\rightarrow$ module-level $\rightarrow$ directory-level.

### External Integration

*   **Documentation Rendering**: `mkdocs.yml` facilitates the deployment of the generated wiki, pulling frontend assets from `cdn.jsdelivr.net` and `cdnjs.cloudflare.com`.
*   **Static Analysis**: `sonar-project.properties` manages the exclusion of test files and dependencies during analysis and consumes `coverage.out` reports.

---

## Quickstart Reference

### Execution Modes

| Mode | Command | Purpose |
| :--- | :--- | :--- |
| **Initialization** | `init` | Performs a full repository scan and generates initial Markdown documentation. |
| **Incremental** | `update` | Analyzes only files where SHA256 fingerprints have changed relative to the `MetadataCache`. |

### Configuration Requirements

To function, the environment must provide:
1.  **Ollama Endpoint**: A running local LLM instance accessible via the configured `OllamaBaseURL`.
2.  **Valid Model**: A `ModelID` configured in the `Config` struct.
3.  **Context Window**: An integer `OllamaNumCtx` greater than zero.

### Data Persistence
*   **Documentation**: Synthesized Markdown files are written to the path defined in `DocsDir`.
*   **State**: `MetadataCache` (JSON) is maintained in the `docsDir` to enable incremental updates.