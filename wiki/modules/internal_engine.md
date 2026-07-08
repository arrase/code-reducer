# Code Reduction Engine Architecture

## Module Responsibility

The `engine` package implements a deterministic code reduction pipeline that ranks repository files via BM25 relevance scoring, caches file fingerprints to avoid redundant reprocessing on subsequent runs, detects affected directories from parsed git diffs, and orchestrates LLM-powered code generation through a configurable chat-completion client. The runner entry point branches execution into initialization or update modes based on the supplied mode parameter.

---

## Data Model & BM25 Ranking (`context.go`)

### File Content Representation — `Document`

```go
type Document struct {
    // Content holds the raw file text.
    Content string
    // TermFrequencies maps token → occurrence count for BM25 scoring.
    TermFrequencies map[string]int
    // TokenCount is the total number of tokens in the document.
    TokenCount int
}
```

### Text Tokenization — `Tokenize`

Splits input text into lowercase alphanumeric tokens using a compiled regex tokenizer. Non-alphanumeric characters are discarded; whitespace and punctuation yield no tokens.

### Relevance Ranking — `FilterFilesBM25`

Ranks repository files by relevance to an arbitrary query string using BM25 (Okapi). Returns the top-K file paths after computing term frequencies over each document's tokenized content. The ranking formula incorporates normalized IDF weighting against the total corpus size and a length normalization factor proportional to `avgdl`.

### Prompt Injection Mitigation — `WrapInXmlDelimiter`

Wraps file content in strict XML-like tags with the file path attribute: `<file path="...">content</file>`. This prevents prompt injection attacks by constraining LLM input to syntactically delimited regions.

---

## Caching Layer (`engine.go`)

### Cached File Entry — `FileCacheEntry`

Stores a cached file's SHA256 hash and associated facts for retrieval without re-processing:

```go
type FileCacheEntry struct {
    Hash   string      // SHA256 hex digest of the original content.
    Facts  interface{} // Arbitrary metadata keyed by filename (e.g., code reduction results).
}
```

### Metadata Cache — `MetadataCache`

Aggregates per-file cache entries, the last documented commit hash, and module mappings into a single JSON-deserializable structure:

```go
type MetadataCache struct {
    LastDocumentedCommit string               // Commit hash of most recent successful documentation pass.
    FileCache             map[string]FileCacheEntry `json:"file_cache"`
    ModuleMappings        map[string]string         `json:"module_mappings"`
}
```

### Cache I/O — `loadMetadataCache` / `saveMetadataCache`

- **`loadMetadataCache(repoRoot, docsDir) (*MetadataCache, error)`** reads the metadata cache from disk at `<repoRoot>/<docsDir>/metadata_cache.json`. Returns an empty default cache if the file is missing or malformed.
- **`saveMetadataCache(repoRoot, docsDir, cache *MetadataCache) error`** serializes the metadata cache to JSON and writes it safely to disk atomically (write-to-temp + rename).

### File Fingerprinting — `computeSHA256`

Reads a file at the given path and returns its SHA256 hex digest. Used for change detection between runs.

---

## Change Detection & Affected Directory Propagation (`engine.go`)

### Git Diff Parsing — `parseGitDiff`

Parses raw git diff output into structured `[]FileChange` entries, categorizing adds, deletes, renames (via rename detection logic), copies, modifications, and type changes. Each entry carries the relative file path and change classification.

### File Filtering — `isAllowedFile`

Filters files by:
- Ignore lists (explicit path patterns)
- Directory/component name whitelists
- File extension allowlists (excludes binaries like `.exe`, `.dll`)
- Safe path resolution to prevent traversal attacks

Returns `true` if the file passes all filters.

### Affected Region Detection — `determineAffected` / `propagateAffected`

- **`determineAffected(node *DirNode, repoRoot, docsDir string, cache *MetadataCache, filteredChanges []FileChange, affectedDirs map[string]bool)`** identifies which directories need updating by checking for changed files and verifying module existence against the metadata cache.
- **`propagateAffected(node *DirNode, affectedDirs map[string]bool) bool`** recursively marks a directory tree as affected when any descendant is marked. Returns `true` if propagation occurred.

---

## LLM Client (`engine.go`)

### Message — `Message`

Represents an LLM role-content pair used in chat completions with JSON serialization support:

```go
type Message struct {
    Role   string // "system", "user", "assistant"
    Content string
}
```

### Client Configuration — `LLMClient` / `NewLLMClient`

- **`LLMClient`** holds configuration for an LLM provider including model ID, base URL, context size limit, and HTTP client instance.
- **`NewLLMClient(modelID, baseURL string, numCtx int) *LLMClient`** initializes a new `LLMClient` with the given parameters and a 10-minute default timeout for HTTP requests.

---

## Response Parsing (`json_parser.go`)

### JSON Extraction — `CleanJSONResponse`

Extracts JSON content from surrounding markdown code fences (`` ```json `` or `` ``` ``) or locates the first opening brace/bracket and last closing brace/bracket in raw text to return a trimmed JSON string. Handles malformed responses gracefully by returning an empty string if no valid JSON is found.

### Deserialization — `UnmarshalJSONResponse`

Cleans the raw response using `CleanJSONResponse`, then unmarshals the resulting JSON into the provided target interface value (e.g., `*struct`). Returns an error if deserialization fails or the cleaned text is empty.

---

## Execution Orchestration (`runner.go`)

### Runner — `Runner` / `NewRunner` / `Run`

- **`Runner`**: A struct holding a reference to the engine configuration, serving as the primary entry point for executing code reduction workflows.
- **`NewRunner(cfg *Config)`** initializes and returns a new `Runner` instance bound to a specific configuration pointer.
- **`Run(mode string) error`** executes the core workflow by:
  1. Acquiring repository locks (mutex or file-based, depending on environment).
  2. Creating an LLM client via `NewLLMClient`.
  3. Branching into initialization mode (`mode == "init"`) or update mode based on the provided mode parameter.
  4. Loading the metadata cache from disk (or creating a fresh one for init).
  5. Running git diff, filtering changes, and determining affected directories.
  6. Re-processing only affected files through BM25 ranking followed by LLM code generation.
  7. Updating the metadata cache with new SHA256 hashes and facts after completion.