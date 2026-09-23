# Internal Engine Architecture Documentation

## Module Responsibility and Data Flow

The `internal/engine` module is an automated documentation orchestration engine that employs a hierarchical Map-Reduce pattern to transform repository source code into structured technical documentation using Large Language Models (LLMs). It facilitates both initial documentation generation (`ModeInit`) and incremental updates (`ModeUpdate`) by implementing a change-detection pipeline based on file integrity hashes and directory-level impact analysis.

### Data Flow Lifecycle
1.  **Initialization & Locking**: The `Runner` acquires an exclusive repository lock and ensures the `.gitignore` contains the lockfile.
2.  **Discovery & Tree Construction**: `Tree` logic converts a flat list of file paths into a hierarchical `DirNode` structure.
3.  **Change Detection**: The `Orchestrator` compares current file SHA256 hashes against the `MetadataCache`. Changes are mapped to specific `FileChange` objects and propagated upward through the `DirNode` tree to identify "affected" directory nodes.
4.  **Hierarchical Synthesis**:
    *   The `Synthesizer` performs a bottom-up traversal of the affected tree branches.
    *   **Leaf Processing**: Files are read, content is processed via `Chunking` (recursive reduction if context limits are exceeded), and LLM extraction is performed.
    *   **Aggregation**: Results from files and subdirectories are aggregated and passed to the LLM for higher-level synthesis.
5.  **Persistence**: Synthesized Markdown documentation and the updated `MetadataCache` are written to the filesystem.

---

## Orchestration and Execution Control

### Runner (`runner.go`)
Serves as the primary entry point for the module. It manages the execution lifecycle, handles repository-level synchronization via `security.AcquireLock`, and delegates core logic to the `Orchestrator`. It supports two operational modes: `ModeInit` and `ModeUpdate`.

### Orchestrator (`orchestrator.go`)
Coordinates the end-to-end pipeline. It manages the transition from file discovery to hierarchical synthesis. It handles the logic for cache invalidation, directory pruning for deleted files, and determines whether global documentation (e.g., `architecture.md`) requires regeneration based on the impact of changes detected in the root directory.

---

## Hierarchical Synthesis and Data Processing

### Synthesizer (`synthesize.go`)
Implements the bottom-up reduction algorithm. It traverses the `DirNode` tree, determining for each node whether to use cached summaries or perform new LLM-based extractions based on the "affected" status. It manages the recursive aggregation of file-level facts into module-level and directory-level summaries.

### Chunking and Reduction (`chunking.go`)
Addresses LLM context window constraints through a recursive reduction strategy:
*   **Expansion**: Splits oversized items into overlapping segments.
*   **Batching**: Groups items to optimize context window utilization.
*   **Recursive Reduction**: Applies LLM-based consolidation to batches.
*   **Convergence Check**: Stops recursion if a reduction step fails to decrease the input size by more than 5% (i.e., output $\ge$ 95% of input).

### Markdown Processing (`markdown.go`)
Provides utilities for cleaning LLM output, specifically stripping surrounding triple-backtick Markdown or JSON fences to extract raw content.

---

## State, Change Detection, and Cache Management

### Metadata Cache (`cache.go`)
Provides a persistence layer for tracking extracted documentation facts and file integrity.
*   **Versioning**: Only version `1` is accepted; version mismatches trigger a clean cache state.
*   **Integrity**: Uses SHA256 fingerprints of raw file content at the virtual path relative to the repository root.
*   **Persistence**: Serializes state to a JSON file in the specified `docsDir`.

### Tree and Change Analysis (`tree.go`)
Implements dependency impact analysis within the directory hierarchy.
*   **Status Tracking**: Tracks files as `StatusAdded`, `StatusModified`, or `StatusDeleted`.
*   **Upward Propagation**: If a child node (file or directory) is marked as affected, its status is recursively propagated to all ancestor `DirNode` objects.
*   **Impact Detection**: Identifies affected nodes based on missing cache entries, modified hashes, or missing documentation files.

---

## Infrastructure and Communication

### LLM Client (`client.go`)
An HTTP client targeting the Ollama `api/chat` protocol. 
*   **Interface**: Abstraction via `llmCaller` allows for decoupling the engine from the specific transport.
*   **Behavior**: Executes synchronous POST requests. It performs no retry logic on transport-level failures and contains no circuit-breaking mechanisms.

### Configuration and Constants (`constants.go`)
Defines operational parameters, including:
*   **Timeouts**: `defaultHTTPTimeout` (10 minutes).
*   **Context Management**: A `contextWindowAllocRatio` of 0.75 reserves 75% of the context window for primary content, leaving 25% for metadata/overhead.
*   **Chunking**: `defaultChunkOverlap` (800 tokens).
*   **Limits**: `maxErrorBodyBytes` (1 KB) caps error response parsing.

### Utilities (`utils.go`)
Provides filesystem path sanitization for generating `README.md` files and a nil-safe event logging adapter.