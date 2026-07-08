# internal/tools — Repository Infrastructure Package

## Module Responsibility and Data Flow

This package provides low-level repository operations required by higher-level analysis modules: safe file I/O, git state queries, path hashing, and recursive source-file discovery. The data flow follows a consistent pattern—**verify the environment → resolve paths safely → read/write with TOCTOU guards → filter against ignore rules**. All functions operate on an absolute or virtual repository root that is resolved once per session; subsequent operations reference this canonical location internally.

---

## Repository Verification and Git Operations

### `git_tools.go`

#### `VerifyGitRepo(repoPath string) error`

Confirms two preconditions before any git or filesystem operation proceeds:

1. The host system has a `git` executable accessible via `$PATH`.
2. `repoPath` is inside a valid git working tree (i.e., `.git/` exists and the path resolves to it).

Returns an error if either check fails, preventing silent misbehavior downstream when callers assume a git-backed repository.

#### `GetGitHead(repoPath string) (string, error)`

Delegates to `RunGit("rev-parse", "HEAD")`, trims whitespace from stdout, and returns the current commit hash. Used by discovery and hashing modules to anchor state snapshots.

#### `RunGit(repoPath string, args ...string) (string, error)`

Executes a git command inside `repoPath`. Returns the trimmed stdout string plus any error; stderr is discarded in this wrapper because higher-level callers typically only need success output or a failure signal.

---

## Safe File I/O Operations

### `file_tools.go` — Read Pathway

#### `ReadFileSafely(virtualPath string) ([]byte, error)`

Resolves a virtual (relative-to-repo-root) path to its absolute on-disk location and reads the content into memory. The resolver normalizes separators and canonicalizes the result so that `./foo`, `../bar/baz`, and `baz` all produce identical byte slices for the same file.

### `file_tools.go` — Write Pathway

#### `WriteFileSafely(absPath string, data []byte) error`

Writes file content using a **TOCTOU-safe pattern**: before truncating the target, it verifies that `absPath` is not a symlink (via `os.Lstat()`). If the path is a symlink, the function returns an error rather than silently following the link and overwriting its destination. This prevents privilege escalation vectors where an attacker replaces a file with a symlink pointing to `/etc/passwd`.

---

## Path Resolution and Hashing

### `registry.go` — Root Fingerprint

#### `HashRepoRoot(path string) string`

Computes the SHA-256 hex digest of the resolved absolute path of the repository root. The input `path` may be relative or absolute; internal resolution normalizes it before hashing. This produces a deterministic fingerprint for the current repo location, useful for caching, diff detection, and state anchoring across runs.

---

## File Discovery and Ignore Logic

### `file_tools.go` — Ignore Rules Engine

#### `LoadGitignore(repoRoot string) ([]string, error)`

Reads `.gitignore` from `repoRoot`, parses line-by-line, skips comments (`#`) and blank lines, and returns the list of active ignore patterns. Patterns are returned in their original form (no glob expansion); callers apply them against relative paths using standard path-matching semantics.

#### `ShouldIgnoreFile(relPath string) bool`

Determines whether a relative file path should be excluded from processing by evaluating:

- Custom patterns loaded via `LoadGitignore`.
- Dot-prefixed components (`.` prefix at any level).
- Known binary/extension suffixes (e.g., `.exe`, `.so`).
- Suffix-based rules for common non-source extensions.

Returns `true` if the file matches any ignore criterion; otherwise `false`.

### `file_tools.go` — Source Discovery

#### `DiscoverCodeFiles(repoRoot string) ([]string, error)`

Recursively walks the repository tree to collect high-signal source files (`.go`, `.py`, `.rs`, etc.). Skips:

- Build directories (`node_modules/`, `vendor/`, `__pycache__/`, etc.).
- Dependency paths identified by common prefixes.
- Any entry matching the ignore list produced by `ShouldIgnoreFile`.

Returns a flat slice of relative file paths, sorted for deterministic output. Used as the primary input to analysis and transformation pipelines.