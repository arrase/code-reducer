# Module Documentation: Internal Engine

## Responsibility & Data Flow

The internal engine orchestrates a hierarchical Map-Reduce pipeline that synthesizes code repositories into structured documentation. The `Runner` coordinates all operations—lock acquisition, LLM client instantiation, and mode-based execution (init/update). On invocation, `Run(ctx, repoRoot, mode)` acquires a repository lock, instantiates an `LLMClient`, then dispatches to either the init or update pipeline based on the `mode` parameter.

The pipeline discovers code files, constructs a directory tree via `DirNode` nodes, and synthesizes modules hierarchically through `synthesizeNode`. Each node recursively processes children and files: file metadata is cached in `MetadataCache`, per-directory markdown documentation is written to disk at `.metadata.json`, and the synthesized output propagates upward. The root-level synthesis generates global architecture docs and quickstart documentation.

## Tree Construction & File Change Tracking

### DirNode

Represents a hierarchical directory node containing a path identifier, an array of associated files, and a map of child nodes for tree construction.

### FileChange

Stores a file change entry consisting of a relative path string and a status field indicating whether the file was added, modified, or deleted.

## LLM Client Abstraction

### Message

Struct representing a chat message with `Role` and `Content` fields, used for LLM request/response payloads.

### LLMClient

Configurable client struct holding model ID, base URL, context size, and HTTP transport for Ollama API interactions.

#### NewLLMClient

Factory function that initializes an `LLMClient` with the provided configuration and a 10-minute timeout on the default HTTP client.

#### CallLLM

Synchronous method that sends a non-streaming chat request to the configured Ollama endpoint and returns the model's response text or an error.

#### StreamLLM

Asynchronous method that streams real-time chunks from the LLM via the streaming API, invoking a user-supplied callback for each content chunk until completion.

### GetDefaultSystemPrompt

Returns a system prompt string tailored to one of three commands (`extract_file`, `module_synthesis`, `architecture`) or a default fallback, with Code-Reducer role definition embedded in all variants.

### LoadSystemPrompt

Delegates directly to `GetDefaultSystemPrompt` and returns the resulting prompt paired with a nil error for use by callers that require a typed result.

## JSON Response Parsing & Cleaning

### CleanJSONResponse

Extracts valid JSON content from a raw response string by stripping markdown code fences and locating the first opening brace or bracket through the last closing brace or bracket.

### UnmarshalJSONResponse

Cleans the raw JSON response string using `CleanJSONResponse` and unmarshals it into the provided target interface value, returning an error if parsing fails.

## Metadata Caching Layer

### FileCacheEntry

Stores per-file cache data consisting of the file's SHA256 hash and an associated facts string.

### MetadataCache

Aggregates repository-wide metadata including the last documented commit, a map of per-file entries, and module-to-path mappings.

#### loadMetadataCache

Deserializes the on-disk `.metadata.json` into memory; if the file is absent or malformed it returns a freshly initialized empty `MetadataCache`.

#### saveMetadataCache

Marshals an in-memory `MetadataCache` to formatted JSON and writes it safely to disk at `.metadata.json`.

### computeSHA256

Reads the contents of the given virtual path and returns its SHA256 hash as a hex-encoded string.

## Core Synthesis Engine

### Event

Struct holding a `Type` string and `Message` string to represent status or progress events emitted during processing.

#### reduceInChunks(ctx, c, nodePath, items, logEvent) error

Recursively chunks code file paths into batches that fit the LLM context window, then synthesizes them by calling the LLM to produce a combined description.

#### synthesizeNode(ctx, c, node, repoRoot, docsDir, cache, affectedDirs, logEvent) error

Performs hierarchical tree synthesis for a directory by recursively processing children and files, caching file metadata, and writing per-directory markdown documentation.

## Runner & Pipeline Orchestration

### Runner

Coordinates repository operations including lock acquisition, gitignore management, LLM client instantiation, and mode-based execution of init or update pipelines.

#### NewRunner(cfg *config.Config) *Runner

Factory function that initializes a new `Runner` instance with the provided configuration.

#### Run(ctx context.Context, repoRoot string, mode string, onEvent func(Event)) error

Main entry point that ensures lockfile is gitignored, acquires a repository lock, instantiates an `LLMClient`, and executes either the init or update pipeline based on the `mode` parameter.

### RunInit(ctx, repoRoot, cfg, onEvent) error

Orchestrates the full Map-Reduce pipeline: discovers code files, builds a directory tree, synthesizes all modules hierarchically, generates global architecture docs, and writes quickstart documentation.