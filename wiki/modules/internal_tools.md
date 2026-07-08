# Internal Tools Module — Architecture Reference

## 1. Repository Verification & Git Operations

**`git_tools.go`** provides the foundational layer for interacting with git repositories. These functions establish a verified working tree before any subsequent file operations occur, ensuring that all downstream utilities operate within a valid repository context.

### `VerifyGitRepo(repoPath string) error`
Validates that:
- The `git` binary is present on the system PATH.
- The specified directory is a recognized git working tree by executing `rev-parse --is-inside-work-tree`.

Returns an error if either precondition fails. This function serves as a gatekeeper; callers should invoke it before any repository-aware operation.

### `RunGit(repoPath string, args ...string) (string, error)`
Executes arbitrary git commands scoped to the verified working tree. The implementation:
- Captures stdout and trims trailing whitespace.
- Propagates non-zero exit codes as errors.

Used for lightweight queries such as resolving relative paths, checking branch state, or retrieving repository metadata without requiring manual shell invocation by callers.

---

## 2. Safe File I/O Operations

**`file_tools.go`** handles read/write primitives with security-focused semantics: path resolution, TOCTOU mitigation, and symlink awareness.

### `ReadFileSafely(repoPath string) ([]byte, error)`
Resolves a virtual repository-relative path to its absolute filesystem location and returns raw byte content. The implementation performs an error-checked open/read/close cycle, returning the complete file payload or an error if the operation cannot be completed within expected bounds.

### `WriteFileSafely(repoPath string, content []byte) error`
Writes arbitrary content to a resolved repository path with three safety measures:
- **Directory creation**: Parent directories are created if absent (with appropriate mode preservation).
- **Symlink detection**: Existing symlinks at the target path are detected and rejected to prevent silent redirect attacks.
- **TOCTOU-safe truncation**: The file is opened in exclusive-truncate mode (`O_RDWR | O_CREATE | O_TRUNC`) to eliminate race conditions between read and write phases.

### `IsBinaryFile(path string) bool`
Scans the first 1024 bytes of a file for null byte (`\x00`) presence. Returns `true` if binary content is detected within that boundary. Used as a quick heuristic filter during code discovery to exclude non-source artifacts (images, compiled binaries, etc.) without loading entire files into memory.

---

## 3. Code Discovery & Filtering Pipeline

**`file_tools.go`** also contains higher-level utilities for walking the repository tree and applying exclusion rules derived from `.gitignore` semantics. These functions compose to produce a final list of source code files suitable for downstream processing (linting, analysis, migration).

### `LoadGitignore(repoRoot string) ([]string, error)`
Reads `.gitignore` from the given repository root, strips comment lines (`#`) and blank lines, and returns the active pattern set. Returns `nil` if the file does not exist or cannot be parsed. Patterns are returned as-is for downstream evaluation; callers should match them against relative paths using standard gitignore matching semantics.

### `ShouldIgnoreFile(relPath string) bool`
Determines whether a given relative path matches any exclusion rule by evaluating:
- Active `.gitignore` patterns from `LoadGitignore`.
- Dot-prefixed directory components (e.g., `.build`, `.cache`).
- Known binary file extensions (detected via `IsBinaryFile` or extension whitelist).
- Null-byte presence in the path string.

Returns `true` if any exclusion rule matches, indicating the file should be skipped during discovery.

### `DiscoverCodeFiles(repoRoot string) ([]string, error)`
Recursively walks the codebase rooted at `repoRoot`, collecting source files while skipping:
- Build directories (e.g., `node_modules`, `.git`, `target`).
- Paths matching any active `.gitignore` pattern from `LoadGitignore`.
- Files flagged as binary by `IsBinaryFile`.

Returns a sorted slice of relative path strings representing the discovered codebase. The walk order is deterministic; results are suitable for direct consumption by analysis or transformation pipelines.