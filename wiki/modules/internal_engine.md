# internal/engine — Code Reduction Engine

## Module Responsibility & Data Flow

The `internal/engine` module implements the Map-Reduce pipeline for automated codebase documentation generation. It orchestrates three phases: **discover** (file-system traversal and tree construction), **synthesize** (LLM-driven hierarchical content aggregation with SHA256 caching), and **reduce** (global architecture summary assembly). The runner exposes a single entry point that acquires the lock, instantiates the LLM client, and dispatches to context-specific pipelines.

---

## File System Traversal & Tree Construction

### DirNode

```go
type DirNode struct {
    Path     string
    Files    []string   // direct files within this directory
    Children []*DirNode // subdirectories
}
```

Represents a node in the recursive directory tree. Each `DirNode` holds its absolute path, a flat list of immediate file paths, and child subtrees for nested directories.

### buildTree

Constructs a root `*DirNode` from a slice of absolute file paths by parsing each path into directory components, then recursively nesting children under their parent nodes.

### FileChange

```go
type FileChange struct {
    Path   string // relative file path
    Status string // "Added", "Modified", or "Deleted"
}
```

A single file-change record produced by diffing the current state against the baseline repository snapshot.

### determineAffected / propagateAffected

`determineAffected` walks the directory tree and marks directories as **affected** when any descendant is marked affected, a module doc is missing, or cache entries are empty. `propagateAffected` is the recursive walker that bubbles upward from leaf nodes to ancestors, ensuring every ancestor of a changed file inherits the affected flag.

### isAllowedFile

Returns `true` when the relative path passes ignore rules defined by `*security.ShouldIgnoreFile`. Paths failing this check are excluded from tree construction and subsequent synthesis.

---

## LLM Client & Communication Layer

### Message

```go
type Message struct {
    Role   string // role identifier (system / user)
    Content string // text payload for prompt construction
}
```

Represents a single turn in an LLM interaction record used to construct the full prompt input.

### LLMClient & NewLLMClient

Aggregates connection settings and HTTP transport required to communicate with an Ollama-compatible endpoint. `NewLLMClient` instantiates a new client initialized with model ID, base URL, context window size, and default timeout.

### CallLLM / StreamLLM

- **CallLLM** — Executes a synchronous non-streaming HTTP POST to the configured Ollama API endpoint; returns the raw response string or an error.
- **StreamLLM** — Initiates a streaming HTTP request, parses incoming chunks line-by-line, and invokes the supplied callback for each content segment.

### stripOuterMarkdownFence / CleanJSONResponse / UnmarshalJSONResponse

`stripOuterMarkdownFence` strips surrounding markdown code fences from input strings using regex to clean common LLM output artifacts. `CleanJSONResponse` extends this by stripping fences and isolating the first/last JSON delimiters when fences are absent. `UnmarshalJSONResponse` chains both: cleans the raw response then unmarshals into a target interface, wrapping parse errors with context.

### GetDefaultSystemPrompt / LoadSystemPrompt

`GetDefaultSystemPrompt` returns a context-specific system prompt string based on command type, defining the AI persona and task instructions for each analysis mode. `LoadSystemPrompt` delegates directly without additional error handling.

---

## Caching Layer

### FileCacheEntry & MetadataCache

- **FileCacheEntry** — Stores the SHA256 hash and associated facts for a single file within the cache structure.
- **MetadataCache** — Maintains the last documented commit identifier along with maps of cached files and modules.

### loadMetadataCache / saveMetadataCache

`loadMetadataCache` reads a metadata JSON from the docs directory, returning an initialized or parsed `*MetadataCache`. `saveMetadataCache` serializes the provided instance into a formatted JSON file at the specified metadata path.

### computeSHA256

Calculates the SHA256 hash of content located at a given virtual path and returns the hex-encoded string. Used to deduplicate synthesis results across re-runs.

---

## Core Engine Pipeline

### synthesizeNode

Traverses a directory tree node's children and files, extracts file facts through LLM analysis with SHA256 caching, then hierarchically merges all components into a consolidated summary. Each leaf is analyzed independently; intermediate nodes aggregate their descendants' summaries before returning upward.

### reduceInChunks

Recursively batches code items by character limit and synthesizes them via LLM calls to produce architecture summaries, truncating oversized content when needed. Splits the input set into chunks that fit within the context window, processes each chunk through `synthesizeNode`, then merges results bottom-up until a single root summary remains.

### RunInit (method on *LLMClient)

Executes the full Map-Reduce pipeline: discovers code files, builds the hierarchical directory tree, performs recursive synthesis via `reduceInChunks`, and generates global architecture documentation plus a quickstart guide. Returns an `Event` struct holding Type and Message for logging emitted during execution.

---

## Orchestration & Runner

### Runner / NewRunner / Run

- **Runner** — Struct holding an engine configuration pointer; primary container for executing code-reduction pipelines.
- **NewRunner** — Initializes a new `*Runner` by accepting a config pointer and storing it within the struct fields.
- **Run** — Orchestrates the full execution lifecycle: acquires lock, instantiates LLM client from config, dispatches to context-specific documentation pipeline based on repository root and command type.

---

## Event Logging

### Event

```go
type Event struct {
    Type    string // event category identifier
    Message string // human-readable description of the emitted event
}
```

Struct holding a Type string and Message string used for logging events emitted during engine execution.