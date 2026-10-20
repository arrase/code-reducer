> **Module:** `internal/search` | **Source Files:** `search.go`

# internal/search

## Module Responsibility

Provides full-text search over markdown documentation sections using BM25 ranking. Discovers `.md` files under a docs directory, parses ATX headings into sections while skipping fenced code blocks, scores sections by query relevance, and returns ranked results with extracted snippets.

## Data Structures

### SearchResult

```go
type SearchResult struct {
    Page     string
    Anchor   string
    Target   string
    Heading  string
    Snippet  string
    Sources  []string
    Score    float64
}
```

## Functions

### SearchWiki

Entry point for documentation search. Accepts `repoRoot`, `docsDir`, `query`, optional `pathHints`, and `limit`. Walks the docs directory tree, parses each `.md` file into sections, scores sections via BM25 (k1=1.2, b=0.75), applies path-hint score boosts for matching module paths, extracts query-matched prose snippets truncated to 160 runes, and returns the top `limit` results ranked by score.

### ReadWikiSection

Reads a single markdown section by target path. Validates the resolved path stays within `docsAbs` and rejects hidden files/directories. Returns the section content or an error.

## Internal Data Flow

```mermaid
flowchart TD
    A["SearchWiki(repoRoot, docsDir, query, pathHints, limit)"] --> B["security.SafeResolve(repoRoot, docsDir)"]
    B --> C["filepath.WalkDir(docsAbs)"]
    C --> D{filepath.ReadDir / os.DirEntry}
    D --> E{filepath.Rel / filepath.Join / filepath.FromSlash / filepath.ToSlash / filepath.Clean}
    E --> F{Extension == .md?}
    F -->|No| G[security.ErrPathTraversal]
    F -->|Yes| H{Segment starts with .?}
    H -->|Yes| G
    H -->|No| I{relToDocs escapes docsAbs?}
    I -->|Yes| G
    I -->|No| J["tools.ReadFileSafely(repoRoot, relToRoot)"]
    J --> K[tools.ParseFenceOpen / tools.IsFenceClose / tools.ParseATXHeading / tools.NextHeadingAnchor]
    K --> L["BM25 Score (k1=1.2, b=0.75)"]
    L --> M{pathHints match?}
    M -->|Yes| N[Boost Score]
    M -->|No| L
    N --> O["Extract Snippet (160 runes)"]
    O --> P[SearchResult{Page, Anchor, Target, Heading, Snippet, Sources, Score}]
    P --> Q["Return ([]SearchResult, error)"]
    G --> Q
```

## Error Handling

- **Path traversal**: `security.ErrPathTraversal` returned when resolved paths escape `docsAbs`, contain `..` prefix, or start with `/`.
- **Hidden files**: Returns `security.ErrPathTraversal` when any path segment starts with `.`.
- **Extension validation**: Returns error when file extension is not `.md` (case-insensitive).
- **File read failures**: Wrapped via `fmt.Errorf("failed to read %s: %w", pageRel, err)`.
- **Markdown parsing**: Errors propagate via `ok` return values from `tools.ParseFenceOpen`, `tools.IsFenceClose`, `tools.ParseATXHeading`, `tools.NextHeadingAnchor`.
- **No network I/O, subprocess execution, environment variable access, or external file writes.**