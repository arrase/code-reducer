# internal/engine — Module Documentation

## Module Responsibility

The `internal/engine` package implements the orchestration layer for LLM-driven code analysis workflows. It manages document ingestion, BM25-based file ranking, directory tree construction, system prompt retrieval, recursive chunk batching with markdown fence stripping, Ollama API client configuration, and JSON response parsing. Data flows from raw repository contents through tokenization → BM25 scoring → XML-wrapped context → LLM calls → parsed responses.

---

## Document Processing & BM25 Ranking

### `Document` (context.go)

Represents a file's content with term frequency statistics pre-computed for BM25 indexing:

```go
type Document struct {
    Content       string
    TermFrequency map[string]int
}
```

### `Tokenize` (context.go)

Splits raw input text into lowercase alphanumeric tokens via regex matching. Produces the token stream consumed by subsequent frequency calculation and BM25 scoring stages.

### `EstimateTokens` (context.go)

Approximates token count from character length under the assumption that one token ≈ 4 characters. Used for context size budgeting before LLM invocation.

### `FilterFilesBM25` (context.go)

Ranks repository files by relevance to a query string using the BM25 ranking algorithm; returns top-K results ordered by descending BM25 score. Input: raw file paths + corpus content. Output: ranked slice of `Document`.

---

## LLM Client Management

### `LLMClient` (engine.go)

Holds configuration parameters required to connect to the Ollama API: model ID, base URL, context size limit. Serves as the transport layer singleton for all subsequent inference calls.

### `NewLLMClient` (engine.go)

Factory constructor initializing and returning a new `*LLMClient` instance with provided settings. Called once per engine lifecycle to establish the client handle.

---

## Directory Tree Construction

### `DirNode` (engine.go)

Models a hierarchical directory tree node containing direct file paths, child subdirectory references:

```go
type DirNode struct {
    Files        []string
    Subdirectories *DirNode
}
```

### `buildTree` (engine.go)

Constructs a hierarchical directory tree from a list of file paths rooted at `"."`. Recursively partitions paths into parent directories and leaves. Used to structure context before chunked LLM synthesis.

---

## System Prompt Handling

### `GetDefaultSystemPrompt` (engine.go)

Returns task-specific system prompts for three workflow phases: file analysis, module synthesis, and architecture tasks. Selects the prompt based on current engine phase state.

### `LoadSystemPrompt` (engine.go)

Retrieves a default system prompt string with error handling; currently always returns successfully. Used as fallback when no task-specific prompt matches.

---

## Recursive Chunk Batching & Markdown Fence Stripping

### `stripOuterMarkdownFence` (engine.go)

Extracts content between triple-backtick markdown fences or JSON fences using regex matching. Strips surrounding markdown wrapper from raw LLM responses before further processing.

### `reduceInChunks` (engine.go)

Synthesizes architecture documentation by recursively batching items into LLM calls with markdown fence stripping applied post-response. Input: directory tree + accumulated results. Output: synthesized markdown document.

---

## Context Safety & Auto-scaling

### `WrapInXmlDelimiter` (context.go)

Wraps file content in XML-like delimiters tagged with the file path to prevent prompt injection attacks. Each wrapped entry is prefixed/suffixed with `<file path>` markers for safe LLM consumption.

### `AutoScaleContext` (context.go)

Caps context size at a safe default of 8192 when the provided maximum is non-positive; otherwise returns input unchanged. Guards against oversized context submissions to the LLM provider.

---

## Response Parsing Utilities

### `CleanJSONResponse(response string) string` (json_parser.go)

Extracts JSON content from markdown code fences, optionally trimming surrounding whitespace and text before or after the first opening brace/bracket to last closing brace/bracket. Handles raw Ollama responses that may embed JSON within markdown blocks.

### `UnmarshalJSONResponse(rawResponse string, target interface{}) error` (json_parser.go)

Pipelines response cleanup through `CleanJSONResponse`, then unmarshals the resulting JSON string into the provided `target` value via `encoding/json.Unmarshal`. Used to deserialize LLM-returned structured data into typed Go objects.