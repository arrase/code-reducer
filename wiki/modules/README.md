# Code-Reducer System Architecture Reference

## System Overview
Code-Reducer is a command-line tool designed to generate structured developer documentation (wikis) from codebases by utilizing local Large Language Models (LLMs) via Ollama. The system employs a Map-Reduce engine to perform hierarchical synthesis, transitioning from individual file analysis to high-level architectural documentation. It supports incremental updates by tracking file state via SHA256 fingerprints.

---

## CLI Orchestration (`cmd` module)

### Responsibility
The `cmd` module acts as the orchestration layer, managing the CLI lifecycle, environment validation, and dispatching execution to the underlying engine. It handles user interaction for initial configuration and enforces state transitions between project initialization and incremental updates.

### Data Flow
1.  **Input Acquisition**: Captures CLI arguments and interactive user input via `os.Stdin`.
2.  **Environment Validation**: Verifies the existence of a git repository and identifies the working directory.
3.  **State Assessment**: Evaluates the current project state (e.g., via `engine.IsInitialized`) to enforce valid modes (`ModeInit` vs. `ModeUpdate`).
4.  **Configuration Resolution**: Loads and merges settings from disk and environmental sources.
5.  **Execution Dispatch**: Passes the validated `config.Config` and execution `mode` to the `engine` package.
6.  **Output Stream**: Directs status updates and errors to `stdout` and `stderr`.

### Core Components
*   **`RootCmd`**: The primary `cobra.Command` entry point.
*   **`executeCommand`**: The central controller that orchestrates directory resolution, git verification, and configuration loading.
*   **`RunSetupFlow`**: An interactive process that captures LLM Model IDs, Ollama endpoints, context sizes, and documentation directory paths.
*   **Subcommands**:
    *   `init`: Triggers a full repository scan and initial Markdown generation.
    *   `update`: Triggers incremental analysis by comparing current file states against the last documented git commit.

---

## Configuration Management (`internal/config`)

### Responsibility
This module manages the hierarchical resolution, validation, and persistence of system settings required for the LLM pipeline.

### Data Flow
1.  **Ingestion**: Aggregates data from `.code-reducer.yaml`, environment variables, and CLI flags.
2.  **Resolution**: Resolves values based on the following precedence: CLI flags $\rightarrow$ Environment variables $\rightarrow$ Configuration file $\rightarrow$ System defaults.
3.  **Validation**: Ensures numerical parameters (e.g., `OllamaNumCtx`) are integers greater than zero.
4.  **Persistence**: Serializes the `Config` struct to YAML and performs an atomic write using a temporary file and rename pattern.

### Data Structures
*   **`Config`**: The primary schema containing `ModelID`, `OllamaBaseURL`, `OllamaNumCtx`, `DocsDir`, and prompt templates for different synthesis stages (Module, Architecture, File Fact Consolidation).
*   **`ExtractionStep`**: Defines specific phases of the LLM extraction pipeline, mapping a `Name` to a specific `Prompt`.

---

## Map-Reduce Documentation Engine (`internal/engine`)

### Responsibility
The engine performs the core logic of transforming source code into structured documentation through a hierarchical reduction process. It manages file discovery, change detection, and recursive LLM-based content reduction.

### Data Flow
1.  **Acquisition**: The `Runner` secures a repository lock.
2.  **Discovery**: Files are mapped into a hierarchical `DirNode` tree structure.
3.  **Change Detection**: The `Orchestrator` compares current file SHA256 hashes against the `MetadataCache`. "Affected" status propagates up the `DirNode` tree.
4.  **Hierarchical Synthesis**:
    *   **Leaf Processing**: Files are processed via `Chunking`. If content exceeds the context window (using a 0.75 `contextWindowAllocRatio`), recursive reduction is applied.
    *   **Aggregation**: Extracted information is aggregated from the file level up to the module and directory levels.
5.  **Persistence**: Synthesized Markdown and `MetadataCache` (JSON) are written to the `docsDir`.

### Core Components
*   **`Synthesizer`**: Traverses the `DirNode` tree to decide between using cached summaries or triggering new LLM extractions.
*   **`Chunking`**: Implements recursive reduction for oversized segments. Recursion terminates if the output size is $\ge$ 95% of the input.
*   **`MetadataCache`**: Persists SHA256 fingerprints to facilitate incremental updates.
*   **`LLM Client`**: A synchronous HTTP client communicating via the `api/chat` protocol.

---

## External Tooling Configurations

### `mkdocs.yml`
Defines the deployment parameters for the generated documentation wiki.
*   **Domain Logic**: Orchestrates the Map-Reduce engine and local LLM interaction.
*   **External I/O**: Fetches frontend assets (JS/CSS) from `cdn.jsdelivr.net` and `cdnjs.cloudflare.com`.

### `sonar-project.properties`
Configures static analysis and code coverage reporting.
*   **Filtering Logic**: Excludes test files, dependencies, and auxiliary assets from source analysis.
*   **I/O**: Reads from the current directory and the `coverage.out` report.