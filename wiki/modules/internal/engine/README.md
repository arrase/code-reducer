# internal/engine — Codebase Documentation Synthesis Engine

## Module Responsibility

The `internal/engine` package implements an LLM-driven pipeline that automatically generates and maintains codebase documentation (architecture overview, quickstart guide, module details, and agent guidelines). The engine operates in two modes:

- **Init** — Performs a full synthesis of the entire codebase from scratch.
- **Update** — Detects file changes since the last run and regenerates only the affected documentation.

Data flows from the repository root through a lock acquisition, gitignore parsing, code discovery, SHA-256 hashing, and tree construction. The orchestrator coordinates cache loading, change detection, hierarchical synthesis, and final persistence.

---

## Core Infrastructure

### LLM Client (`client.go`)

The `llmCaller` interface abstracts LLM invocation. The unexported `llmClient` type implements it by serializing requests to the Ollama `/api/chat` endpoint and returning text responses.

```go
type llmCaller interface {
    CallLLM(ctx context.Context, systemPrompt, userPrompt string) (string, error)
    NumCtx() int
}
```

`CallLLM` sends an HTTP POST with a 10-minute timeout. On a 200 OK, the full response body is read; on non-200, up to 1024 bytes of the error body are captured. Marshaling, request creation, and response parsing errors are wrapped with `fmt.Errorf`. The `onStatus` callback fires when `done_reason == "length"`.

### Metadata Cache (`cache.go`)

`MetadataCache` persists file state and configuration across engine runs. It stores a version, a `stepsHash` derived from model ID and all prompt templates, per-file SHA-256 hashes with associated facts, and module-to-path mappings.

```go
type FileCacheEntry struct {
    SHA256 string
    Facts  string
}

type MetadataCache struct {
    Version   int
    StepsHash string
    Files     map[string]FileCacheEntry
    Modules   map[string]string
}
```

`loadMetadataCache` reads `.metadata.json` from `docsDir`. If absent, it returns an empty cache; if the version is incompatible, it resets. `saveMetadataCache` writes indented JSON. `IsInitialized` checks file existence. `computeSHA256` hashes arbitrary file paths.

---

## Tree Construction & Change Detection

### Directory Tree (`tree.go`)

`DirNode` represents a directory containing files and child directories. `buildTree` parses a file slice into a hierarchical `*DirNode` structure.

```go
type DirNode struct {
    Path    string
    Files   []string
    Children map[string]*DirNode
}
```

`FileChange` tracks a file's status (`StatusAdded`, `StatusModified`, `StatusDeleted`) and path. `propagateAffected` walks the tree upward, marking ancestors affected when a descendant is affected. `isNodeAffected` checks whether a node's files appear in the changed set or its module is missing from cache. `determineAffected` combines changed files with cache state to produce a final affected-dirs map.

### Orchestrator (`orchestrator.go`)

The `orchestrator` struct holds the `llmCaller` and coordinates the full pipeline. `pipelineState` is created fresh per invocation and contains `repoRoot`, `cfg`, `cache`, `affectedDirs`, `precalculatedHashes`, and `onStatus`.

```go
type orchestrator struct {
    client llmCaller
}

type pipelineState struct {
    client              llmCaller
    repoRoot            string
    cfg                 *config.Config
    cache               *MetadataCache
    affectedDirs        map[string]bool
    precalculatedHashes map[string]string
    onStatus            StatusFunc
}
```

`Run` is the entry point. It acquires a lock via `security.AcquireLock`, loads `.gitignore`, loads the metadata cache, and dispatches to `RunInit` or `RunUpdate`. `RunInit` builds the tree, hashes all code files, synthesizes the root summary, generates `architecture.md` and `quickstart.md`, updates `AGENTS.md`, and persists the cache. `RunUpdate` detects file changes, prunes stale cache entries, and regenerates only affected modules.

`setupPipeline` initializes the cache and affected-dirs map. `teardownPipeline` cleans up stale module files and empty directories. `hashCodeFiles` computes SHA-256 hashes for a file list. `collectTreeDirs` returns the set of directories present in the tree. `updateAgentGuidelines` appends documentation references to `AGENTS.md`.

---

## Hierarchical Synthesis

### Synthesis Pipeline (`synthesize.go`)

`extractFileFacts` invokes the LLM for each extraction step per chunk of a file. It strips outer markdown fences, filters "none" facts, and caches results keyed by file SHA-256. Read failures are swallowed via `onStatus`; LLM and chunking failures propagate.

`synthesizeChildren` processes child directories recursively, synthesizing subsystem summaries before individual files. `collectComponents` combines subsystem summaries and file facts into a unified component list. `synthesizeNode` reduces all components for a directory into a final summary, writes it to `modules/<path>.md`, and persists the cache. If no components are produced, the cached summary is deleted and the markdown file is removed.

```go
func extractFileFacts(ctx context.Context, p *pipelineState, f string, siblingFiles []string) (string, error)
func synthesizeChildren(ctx context.Context, p *pipelineState, node *DirNode) ([]string, error)
func collectComponents(ctx context.Context, p *pipelineState, node *DirNode, subsystems []string) ([]string, error)
func synthesizeNode(ctx context.Context, p *pipelineState, node *DirNode) (string, error)
```

`buildExtractionPrompt` constructs the user prompt for a single extraction step. `maxExtractionOverhead` computes the maximum characters available for extraction given system prompt and file context. `toSafeMarkdownFilename` sanitizes a module path for use as a filename.

### Text Reduction & Chunking (`chunking.go`)

`reduceWithLLM` calls the LLM to reduce a batch of items. `reduceInChunks` splits oversized items with an 800-character overlap, batches remaining items, and reduces each batch. If total output reaches 95% of input, reduction stops and results are concatenated. Otherwise, the intermediate results are recursively reduced.

`chunkTextWithOverlap` splits text with overlap validation. `stripOuterMarkdownFence` removes surrounding code fences. `calculateFileLimit` computes the maximum processing characters from context window minus system prompt overhead. `isNoneFact` filters empty, "none", and "n/a" facts. `expandOversizedItems` splits items exceeding the limit. `batchItems` groups remaining items. `countRunes` counts Unicode code points. `reduceItems` orchestrates the reduction with a custom function.

---

## Error Handling Summary

| Operation | Failure Mode | Handling |
|-----------|-------------|----------|
| Lock acquisition | `security.AcquireLock` fails | Fatal error returned |
| Gitignore load | `tools.LoadGitignore` fails | Fatal error returned |
| Code discovery | `tools.DiscoverCodeFiles` fails | Fatal error returned |
| File read (extraction) | `tools.ReadFileSafely` fails | Swallowed, logged via `onStatus` |
| LLM call | `CallLLM` fails | Propagated as wrapped error |
| Chunking | Invalid `maxRunes` or `overlapRunes` | Returned as error |
| Disk write | `tools.WriteFileSafely` fails | Fatal error returned |
| Stale cleanup | `os.Remove` fails | Silently ignored |
| Cache save | Marshaling or write fails | Fatal error returned |
| HTTP non-200 | Response body read | Wrapped with status code |
| Context cancellation | Any operation | Propagated as error |