# Code Retrieval Engine — Architecture Documentation

## Module Responsibility & Data Flow

This module orchestrates a code-retrieval pipeline: ingests repository files, ranks them by BM25 relevance to user queries, wraps selected context in XML-delimited containers for injection safety, serializes the request into an LLM call via an Ollama-compatible client, and deserializes the resulting JSON response.

**Data Flow:** `Query String` → `FilterFilesBM25` (rank files) → `WrapInXmlDelimiter` (sanitize content) + `AutoScaleContext` (cap context length) → `LLMClient` (serialize request via `Message`) → LLM API → `CleanJSONResponse` / `UnmarshalJSONResponse` (deserialize response).

---

## 1. Core Data Structures & Configuration (`engine.go`)

### Message

```
type Message struct {
    Role    string
    Content string
}
```

Represents a single turn in an LLM conversation with JSON-serializable `Role` and `Content` fields. Used to construct the prompt payload sent to the model via `LLMClient`.

### LLMClient

```
type LLMClient struct {
    ModelID  string
    BaseURL  string
    NumCtx   int
}
```

Holds Ollama-compatible API credentials: target model identifier, base URL for the inference endpoint, and maximum context window size. Constructed via `NewLLMClient(modelID, baseURL, numCtx)`.

### Event

```
type Event struct {
    Type    string
    Message *Message
}
```

Carries an event type label and optional payload message. Used to route internal processing events (e.g., completion signals, error flags).

### DirNode

```
type DirNode struct {
    Path      string
    Files     []string
    Children  []*DirNode
}
```

Recursive directory tree node representing the repository layout: `Path` stores the absolute path, `Files` holds a list of files at this level (with relative paths), and `Children` contains nested `DirNode` slices for subdirectories. Used to represent file system structure consumed by BM25 retrieval.

---

## 2. Context Processing Pipeline (`context.go`)

### Document

```
type Document struct {
    Content       string
    Tokens        []string
    TermFrequency map[string]int
    TokenCount    int64
}
```

Encapsulates a parsed file's content along with its token list, term frequency table (for BM25 IDF computation), and total token count. Used as the internal representation for ranking and context window management.

### EstimateTokens

Approximates the token count from raw character length assuming a 4-character-per-token heuristic:

```go
TokenCount = int64(len(content)) / 4
```

Used to pre-estimate budget before actual tokenization, avoiding unnecessary work on large files during ranking.

### Tokenize

Splits input text into lowercase alphanumeric tokens using regex matching (`[a-z0-9]+`). Produces the `Tokens` slice and populates `TermFrequency`. Used by BM25 scoring to compute term frequencies per document.

### FilterFilesBM25

Ranks repository files by BM25 relevance score against a query string and returns the top-K file paths:

```
Score = (tf(t,d) * IDF(t)) / (dl + b * avgdl)
```

Returns `[]string` of ranked file paths. Drives the retrieval step that selects which files to include in the LLM prompt.

### WrapInXmlDelimiter

Wraps selected file content in strict XML-like tags with the file path embedded:

```xml
<file path="src/main.go">
...content...
</file>
```

Prevents prompt injection by constraining user-controlled content within deterministic delimiters and embedding provenance metadata.

### AutoScaleContext

Caps the maximum context size passed to an LLM, returning a safe default of **8192** when input is non-positive:

```go
if cap <= 0 { return 8192 }
return cap
```

Applied after concatenating wrapped file contexts and user query to ensure total prompt length does not exceed the model's context window.

---

## 3. LLM Response Handling (`json_parser.go`)

### CleanJSONResponse

Extracts raw JSON content from a model response by stripping markdown code fences (`` ` ``` ``) and trimming outer delimiters:

```
response = regexReplace(response, "```", "")
response = trim(response)
```

Handles common LLM output patterns where models wrap responses in markdown-formatted code blocks.

### UnmarshalJSONResponse

Cleans a raw string response using `CleanJSONResponse` and deserializes the resulting JSON into a provided target interface:

```go
var result interface{}
json.Unmarshal(cleaned, &result)
return result.(T)  // T = concrete type from caller
```

Type-asserts the deserialized value to the expected concrete type. Caller must supply a pointer of the correct struct; misuse panics on assertion failure.