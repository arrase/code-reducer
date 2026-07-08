# Module: internal/engine

## Responsibility & Data Flow

The `internal/engine` package orchestrates the full lifecycle of automated documentation generation. It bridges raw repository state (Git diffs, file metadata) with LLM inference capabilities through a four-stage pipeline: **Indexing** (BM25 ranking and context preparation), **State Tracking** (diff parsing, cache persistence, affected node detection), **Inference** (LLM client abstraction with retry logic), and **Response Handling** (JSON extraction and deserialization).

The execution flow terminates at the `Runner` orchestrator, which acquires a lock to serialize repository writes, dispatches either an initialization or update mode based on input, and commits the resulting Git HEAD SHA upon success.

---

## Context & Indexing (`context.go`)

### Data Structures

- **Document**: Encapsulates file content alongside term frequency (TF) statistics required for BM25 scoring calculations.
- **FileCacheEntry**: Stores a single cached entry's SHA256 hash and associated facts, utilized by the metadata cache to avoid redundant recomputation.

### Functions

**Tokenize** splits an input string into lowercase alphanumeric tokens via a compiled regular expression. It provides the foundational token stream used for subsequent frequency analysis without character-level noise.

**WrapInXmlDelimiter** encapsulates provided file content in strict XML-like tags with the embedded file path. This structural wrapper mitigates prompt injection risks by isolating user-controlled data from system instructions within the LLM context window.

**FilterFilesBM25** ranks repository files by relevance to a query using the BM25 ranking algorithm. It accepts a query string and returns a slice of top-K file paths ordered by inverse document frequency weighted term scores.

---

## Repository State & Diff Engine (`engine.go`)

### Data Structures

- **MetadataCache**: Holds the last documented commit string plus maps for files and modules loaded from `.metadata.json`. Persists state across runs to track which entities have been processed.
- **FileChange**: Represents a single path/status pair (Added, Modified, or Deleted) parsed from raw git diff output.

### Functions

**loadMetadataCache** deserializes the metadata cache from `.metadata.json`, returning an empty default cache on any I/O error to ensure forward compatibility with stale or corrupted state files.

**saveMetadataCache** serializes the current `MetadataCache` instance to `.metadata.json` using indented JSON formatting for human-readable diffing during debugging.

**computeSHA25** computes the SHA256 hex digest of a file's contents at a given virtual path relative to repo root, ensuring content-addressable integrity checks independent of filesystem location changes.

**parseGitDiff** parses raw git diff output into a `[]FileChange` slice. It handles Added, Deleted, Modified, Renamed, Copied, and Type-changed entries by normalizing rename/copy operations into distinct change records for accurate state tracking.

**isAllowedFile** validates file eligibility based on ignore rules (e.g., `.git`, `node_modules`), directory filters, binary detection heuristics, and path safety resolution to prevent processing of unsafe or irrelevant paths.

**determineAffected** walks the directory tree to identify nodes marked as affected by checking changed files against cache module state and physical file existence. It returns a boolean map keyed by path.

**propagateAffected** recursively walks a `DirNode` tree, marking parent directories as "affected" when any child reports an affected status. This ensures that structural changes (e.g., adding a new directory) invalidate all ancestor documentation entries automatically.

---

## LLM Interaction Layer (`engine.go`)

### Data Structures

- **LLMClient**: Represents the abstraction over an HTTP-based chat API, holding configuration for model identity and transport parameters.

### Functions

**NewLLMClient** constructs a new `*LLMClient` with the provided model ID, base URL, context size, and an HTTP client configured with a 10-minute timeout to prevent hangs on slow providers.

**(LLMClient) CallLLM** invokes the LLM via an HTTP chat API endpoint with retry logic for transient errors (connection resets, rate limits). It returns the streamed response string accumulated from successful chunks, discarding partial responses only after exhausting the retry budget.

---

## Response Parsing (`json_parser.go`)

### Functions

**CleanJSONResponse** extracts JSON content from raw LLM output by either stripping surrounding markdown code fences or locating the first opening brace/bracket and last closing brace/bracket pair in the text, returning a trimmed JSON string. This handles common model behaviors where structured output is wrapped in conversational scaffolding.

**UnmarshalJSONResponse** chains `CleanJSONResponse` followed by standard Go deserialization into the provided target interface value (`interface{}`). It returns an error if the cleaned payload fails to unmarshal, ensuring type-safe consumption of LLM outputs downstream.

---

## Pipeline Orchestration (`runner.go`)

### Data Structures

- **Runner**: Holds a configuration pointer and orchestrates repository operations including locking, LLM client instantiation, and documentation pipeline execution. Acts as the entry point for all write operations to the documentation store.

### Functions

**NewRunner** exports a constructor that initializes a new `Runner` instance with the provided configuration, establishing initial state dependencies (lock file paths, cache locations) required for subsequent execution.

**Run** is the primary execution method. It acquires an exclusive lock on the runner's state file to serialize concurrent invocations, dispatches to either `init` or `update` modes based on input arguments, and updates the Git HEAD SHA in the config upon successful completion.