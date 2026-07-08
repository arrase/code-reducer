# internal/tools — Repository Introspection Utilities

## Purpose

This package supplies hardened low-level primitives for reading, writing, discovering, and hashing files within a Git repository. The data flow is linear: callers resolve virtual paths through the filesystem layer (`file_tools.go`), capture version-control state via `git_commands` (`git_tools.go`), or compute a stable root hash (`registry.go`). All exported functions operate on absolute paths after sanitization to prevent directory-traversal attacks and TOCTOU race conditions.

---

## File I/O Operations

### ReadFileSafely

Resolves a virtual path inside the repository, performs security validation (path prefix checks against known safe directories), then reads and returns its contents. Returns an error if the file does not exist or is outside the allowed root boundary.

```go
func ReadFileSafely(virtualPath string) ([]byte, error)
```

**Data flow:** `virtualPath` → path sanitization → root-boundary check → `os.ReadFile` → return contents.

---

### WriteFileSafely

Resolves a virtual path, creates parent directories (`os.MkdirAll`) if absent, writes content to the file descriptor after opening, then verifies no TOCTOU symlink was created between open and write completion by re-checking the resolved target. Returns an error on any failure mode including race-condition detection.

```go
func WriteFileSafely(virtualPath string, data []byte) error
```

**Data flow:** `virtualPath` + `data` → parent directory creation → `os.OpenFile(O_CREATE|O_WRONLY)` → post-write symlink check → return success/error.

---

### DiscoverCodeFiles

Recursively walks the codebase to collect high-signal source files while skipping build artifacts, dependency trees (`vendor/`, `node_modules/`), caches (`.git/`, `.hg/`, `__pycache__/`), and paths matching user-supplied ignore patterns. Returns a sorted slice of absolute paths.

```go
func DiscoverCodeFiles(ignorePatterns []string) ([]string, error)
```

**Data flow:** root directory → recursive walk (`filepath.Walk`) → filter against skip-set + pattern matcher → deduplicate → return sorted path list.

---

### IsBinaryFile

Determines whether a file is binary by scanning its first 1024 bytes for null byte (`\x00`) characters. Returns `true` if any null byte is encountered within the scan window; otherwise returns `false`.

```go
func IsBinaryFile(path string) (bool, error)
```

**Data flow:** `path` → open file → read 1024 bytes → scan for `\x00` → return boolean.

---

## Git Operations

### RunGit

Executes a git command in the specified repository directory and returns its combined stdout/stderr output (or an error). Captures both streams and merges them before returning to allow full diagnostic information retrieval from a single call site.

```go
func RunGit(args ...string) ([]byte, error)
```

**Data flow:** `args` → `git exec -C repoDir args...` → capture stdout + stderr concurrently → merge → return combined bytes or error.

---

### GetGitHead

Returns the current HEAD commit hash by delegating to `git rev-parse HEAD`. Returns an empty string and non-nil error if the repository is not in a detached state or no commits exist.

```go
func GetGitHead() (string, error)
```

**Data flow:** → `RunGit("rev-parse", "HEAD")` → trim whitespace → return hash string.

---

### CreateGitSummary

Builds a structured multi-section string of git status, head hash, log history (last N commits), and diff evidence for use as LLM prompt context. Sections are concatenated in deterministic order to produce reproducible output across identical repository states.

```go
func CreateGitSummary() (string, error)
```

**Data flow:** → `GetGitHead()` + `RunGit("status")` + `RunGit("log", "-n10")` + `RunGit("diff")` → format each section with delimiters → return single string.

---

## Hashing / Registry

### HashRepoRoot

Computes and returns a hexadecimal-encoded SHA-256 hash of the resolved absolute file system path provided by the caller. The input is canonicalized (trailing slashes removed, symlinks followed via `filepath.EvalSymlinks`) to ensure deterministic output regardless of how the path was constructed.

```go
func HashRepoRoot(path string) (string, error)
```

**Data flow:** `path` → `EvalSymlinks` + canonicalization → `io.ReadAll` on path bytes → SHA-256 digest → hex encoding → return 64-character hex string.