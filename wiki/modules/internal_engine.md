# Internal Engine: AI-Powered Documentation Synthesis System

## Module Responsibility

This module implements an end-to-end pipeline for generating and maintaining Markdown architecture documentation from code repositories using large language model (LLM) inference. The system performs recursive directory traversal with SHA256-based caching, incremental change detection via metadata persistence, and LLM-driven synthesis of per-node summaries that are aggregated into global architecture files.

**Data Flow:** Configuration → `Runner` instantiation → repository discovery → `DirNode` tree construction → recursive `synthesizeNode` (LLM extraction + cache lookup) → root summary → standard docs generation → metadata serialization via `MetadataCache`.

---

## LLM Client Abstraction (`client.go`)

The core inference interface wraps HTTP communication with an Ollama-compatible backend.

### Message Struct

```go
type Message struct {
    Role   string // "system", "user", "assistant"
    Content string
}
```

Stores a single turn in the conversation payload for serialization into API requests.

### LLMClient

Holds configuration parameters (`modelID`, `baseURL`, `contextLength`) alongside an HTTP client used to communicate with the Ollama backend.

- **NewLLMClient** — Constructs and returns a new `LLMClient` instance initialized with provided model identifier, API endpoint, and maximum context size.
- **CallLLM** — Sends a non-streaming request to the language model via HTTP; returns the full text response or an error upon failure.
- **StreamLLM** — Initiates a streaming session; processes incoming chunks by calling a user-defined function for each content segment until the stream completes.
- **GetDefaultSystemPrompt** — Returns a contextualized system prompt string based on current operation mode, supporting tasks like code extraction, module synthesis, and architecture documentation.

---

## Caching Layer (`cache.go`)

Provides persistent state management for incremental updates by storing file-level integrity hashes and global cache metadata.

### FileCacheEntry

Stores the SHA256 hash and facts string for individual files within the cache. Enables detection of content changes without re-reading source files during update cycles.

### MetadataCache

Aggregates global cache state including:
- Last documented commit reference
- File entries (path → `FileCacheEntry` mapping)
- Module mappings

### Cache Operations

- **loadMetadataCache** — Loads or creates an empty `MetadataCache` by reading from a `.metadata.json` file path relative to the docs directory.
- **saveMetadataCache** — Serializes the provided `MetadataCache` into indented JSON and writes it to the specified metadata cache path.
- **computeSHA256** — Computes the SHA256 hex digest of content read from a virtual file path relative to the repository root.

---

## Directory Tree Representation (`tree.go`)

Encodes the discovered code structure as a navigable tree for recursive processing.

### FileChange

A struct holding `Path` and `Status` fields representing a single file change operation ("Added", "Modified", or "Deleted"). Used during incremental update cycles to report detected differences.

### DirNode

Tree node struct containing:
- Directory path
- List of files (with their SHA256 hashes)
- Child nodes keyed by subdirectory name

---

## Synthesis Engine (`synthesize.go`)

Implements the recursive directory traversal that synthesizes Markdown summaries for code modules. The sole function `synthesizeNode` is unexported and processes child directories and files with LLM extraction, caching results via SHA256 hashing to avoid redundant work on unchanged nodes.

---

## JSON Response Parsing (`json_parser.go`)

Strips markdown/JSON code fences from raw LLM responses before deserialization.

- **StripOuterMarkdownFence** — Strips surrounding markdown or JSON code fences from input strings; returns the inner content.
- **CleanJSONResponse** — Removes markdown code fences, then uses brace/bracket matching to extract a complete JSON object or array from remaining text.
- **UnmarshalJSONResponse** — Cleans raw response string with `CleanJSONResponse` and deserializes it into provided target interface value.

---

## Pipeline Orchestrator (`orchestrator.go`)

Coordinates the full Map-Reduce pipeline for repository initialization and incremental updates.

### Event Struct

Lightweight struct carrying `Type` (string) and `Message` (string), used to pass pipeline status updates back through callback interfaces.

### GenerateStandardDocs

Exported method on `*LLMClient` that generates global architecture and quickstart Markdown files from a root summary via LLM calls; writes results safely to disk.

### RunInit

Exported method on `*LLMClient` executing the full Map-Reduce pipeline for repository initialization:
1. Discovers code
2. Builds directory tree (`DirNode`)
3. Synthesizes root summary through hierarchical merging
4. Regenerates standard docs
5. Updates AGENTS.md with AI agent guidelines

### RunUpdate

Exported method on `*LLMClient` for incremental operation:
1. Detects file changes (added/modified/deleted) by SHA256 comparison against metadata cache
2. Determines affected directories via tree traversal
3. Re-synthesizes summaries only where needed
4. Conditionally regenerates architecture or quickstart docs

---

## Configuration and Runner (`runner.go`)

### NewRunner

Factory constructor that instantiates and returns a new `*Runner` with provided configuration pointer. Serves as the entry point for initializing the documentation generation system.