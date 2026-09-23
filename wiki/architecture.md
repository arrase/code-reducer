# Architecture Overview

Code-Reducer employs a hierarchical Map-Reduce architecture to transform source code into structured documentation. The system transitions from granular file analysis to high-level architectural synthesis through a recursive reduction process.

## System Boundaries

*   **Input/Interface**: The system accepts local source code within a git repository, user input via `os.Stdin`, configuration via `.code-reducer.yaml`, environment variables, and CLI flags.
*   **External Dependencies**: 
    *   **LLM Provider**: Communicates with local LLM instances via the `api/chat` protocol (Ollama).
    *   **Frontend Assets**: `mkdocs.yml` facilitates documentation deployment by fetching assets from `cdn.jsdelivr.net` and `cdnjs.cloudflare.com`.
*   **Output**: Produces structured Markdown files in a designated `docsDir` and a `MetadataCache` in JSON format.

## Module Interaction and Data Flow

### 1. Orchestration Layer (`cmd`)
The `cmd` module serves as the system entry point, managing the execution lifecycle:
*   **Validation**: Verifies the working directory is a git repository.
*   **State Enforcement**: Uses `engine.IsInitialized` to switch between `ModeInit` (full scan) and `ModeUpdate` (incremental analysis).
*   **Dispatch**: Resolves the configuration and hands off execution to the `engine`.

### 2. Configuration Management (`internal/config`)
The `config` module handles the hierarchical resolution of settings:
*   **Precedence**: CLI Flags $\rightarrow$ Environment Variables $\rightarrow$ Configuration File $\rightarrow$ Defaults.
*   **Schema**: Manages parameters for LLM interaction (e.g., `OllamaNumCtx`) and prompt templates used during the `ExtractionStep` phases.
*   **Persistence**: Ensures atomic writes to disk using a temporary file and rename pattern.

### 3. Map-Reduce Engine (`internal/engine`)
The core engine executes the hierarchical synthesis via a `DirNode` tree structure:

**Change Detection & Propagation:**
1.  **Leaf Level**: The `Orchestrator` compares current file SHA256 hashes against the `MetadataCache`.
2.  **Propagation**: If a change is detected, the "affected" status propagates up the `DirNode` tree from the file level to the directory and module levels.

**Hierarchical Synthesis (The Map-Reduce Cycle):**
*   **Map (Extraction)**:
    *   Files are processed through the `Synthesizer`.
    *   **Chunking**: If content exceeds the context window (governed by a `0.75 contextWindowAllocRatio`), the system triggers recursive reduction.
    *   **Recursion Termination**: The `Chunking` process terminates when the output size is $\ge$ 95% of the input size.
*   **Reduce (Aggregation)**:
    *   Extracted information is aggregated from individual files $\rightarrow$ Modules $\rightarrow$ Directories $\rightarrow$ High-level Architecture.

## Data Summary

| Component | Responsibility | Primary Data/Format |
| :--- | :--- | :--- |
| `cmd` | Lifecycle & Dispatch | CLI Args, `config.Config` |
| `config` | Parameter Resolution | YAML, Environment Variables |
| `engine` | Hierarchical Synthesis | `DirNode` Tree, `MetadataCache` (JSON) |
| `engine/LLM` | Content Reduction | HTTP `api/chat` |
| `Output` | Documentation | Markdown |