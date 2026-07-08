# internal/engine — Module Architecture

## Responsibility and Data Flow

The `internal/engine` package implements a two-stage retrieval–generation pipeline: **BM25 lexical scoring** of candidate files followed by **LLM-driven synthesis** that consumes ranked content. The module ingests raw file paths, tokenizes their contents, ranks them against user queries using the BM25 formula, and streams LLM responses with retry logic for transient HTTP failures (429/500–504). A `Runner` orchestrates end-to-end execution: it acquires a lock, instantiates an `LLMClient`, and drives documentation generation within a bounded context window.

---

## BM25 Ranking & Indexing (`context.go`)

### Constants

| Constant | Purpose |
|----------|---------|
| `k1` | BM25 term-frequency saturation parameter (default 1.5). Controls the asymptotic growth of TF contribution per occurrence. |
| `b` | BM25 document-length normalization factor (default 0.75). Reduces the weight assigned to documents as their length approaches the corpus mean. |

### Types

**`Document`** — Indexable unit holding a file path, raw content, tokenized tokens, per-term frequencies, and total token count (`n`). Populated before ingestion into the BM25 scorer.

**`DocScore`** — Temporary key-value pair pairing each candidate path with its computed BM25 relevance score during ranking phase.

### Functions

- **`Tokenize`** — Splits input text into lowercase alphanumeric word tokens via regular expression matching (`[a-z0-9]+`). Returns a slice of trimmed token strings.
- **`FilterFilesBM25`** — Orchestrates the full scoring pipeline: loads each candidate file, tokenizes content, computes smoothed IDF for query terms, scores every document with `TF · IDF`, sorts results by descending relevance, and returns the top *K* paths.
- **`EstimateTokens`** — Approximates token count by dividing character length by 4 (assumes ~4 characters per average English word). Used for context-budget estimation when exact counting is unavailable.
- **`WrapInXmlDelimiter`** — Encapsulates file path and content inside strict `<file_content>` XML-like tags to mitigate prompt-injection risks during LLM consumption.
- **`AutoScaleContext`** — Returns a safe default of 8192 characters when the caller provides zero or negative context limits; otherwise passes through unchanged.

---

## LLM Client & Interaction (`engine.go`)

### Types

**`Message`** — Represents an LLM message with `role` and `content` fields for chat interaction (system, user, assistant).

**`LLMClient`** — HTTP-based client struct configured with model ID, base URL, context length, and timeout. Wraps synchronous (`CallLLM`) and streaming (`StreamLLM`) invocation methods.

### Functions

- **`NewLLMClient`** — Constructs an `LLMClient` instance from a configuration pointer containing model ID, base URL, context length, and timeout parameters.
- **`CallLLM`** — Invokes a synchronous LLM call with retry logic for transient HTTP errors (status 429 Too Many Requests; 500–504 server/transport-level failures). Returns the parsed response or an error.
- **`StreamLLM`** — Streams the LLM response in real time by reading line-by-line from the HTTP stream and invoking a callback function per chunk received.

### Prompt Loading

- **`Event`** — Represents an event occurrence with a type identifier and associated message string (used for logging pipeline events).
- **`GetDefaultSystemPrompt`** — Returns a system prompt tailored to one of four commands: `extract_file`, `module_synthesis`, `architecture`, or `default`.
- **`LoadSystemPrompt`** — Delegates directly to `GetDefaultSystemPrompt`; provides aliasing for external consumers.

---

## JSON Parsing Utilities (`json_parser.go`)

### Functions

- **`CleanJSONResponse`** — Extracts raw JSON content from a response string by either stripping markdown code fences (`` ``` ``) or locating the first opening brace/bracket to the last closing brace/bracket in the payload.
- **`UnmarshalJSONResponse`** — Chains `CleanJSONResponse` with `json.Unmarshal`, returning an error if parsing fails into the provided target interface value.

---

## Runner Orchestrator (`runner.go`)

### Types

**`Runner`** — Struct storing engine configuration and orchestrating repository processing workflows across BM25 ranking, LLM streaming, and documentation generation phases.

### Functions

- **`NewRunner`** — Constructs a new `Runner` instance by encapsulating the provided configuration pointer.
- **`Run`** — Executes the full pipeline lifecycle: acquires a lock to serialize concurrent invocations, instantiates an `LLMClient`, and drives documentation generation within a bounded context window.