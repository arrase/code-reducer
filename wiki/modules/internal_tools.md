# internal/tools

## Module Overview

The `internal/tools` package encapsulates low-level primitives for filesystem I/O, repository traversal, Git interaction, and deterministic content hashing. It sits between the application layer and OS/subprocess abstractions, shielding consumers from path resolution failures, symlink race conditions, build artifact noise, and non-deterministic subprocess behavior. Data flows originate at either a virtual file system path or a repository root string; both converge on byte-level operations (read/write/hash) before being consumed by higher-order tooling modules.

---

## File I/O & Classification

### ReadFileSafely

Resolves a virtual path through the embedded filesystem abstraction and reads the underlying content into memory, returning raw bytes (`[]byte`) or an error upon resolution failure (missing parent directory, permission denial) or read failure (I/O error, truncated stream).

```go
func ReadFileSafely(path string) ([]byte, error) { /* ... */ }
```

**Contract:**
*   **Input:** Virtual path string.
*   **Output:** Raw file content bytes; nil on success is not permitted—zero-length files return an empty slice with `nil` error.

### WriteFileSafely

Writes content to a virtual path using a TOCTOU-safe pattern designed to detect and reject symlink targets before truncation. The implementation performs a stat call equivalent to the target inode immediately prior to opening for write; if the target is not a regular file (i.e., a symlink or directory), the operation aborts with an error, preventing `os.SYMLINK` race conditions where a dangling symlink could cause data loss on truncation.

```go
func WriteFileSafely(path string, content []byte) error { /* ... */ }
```

**Contract:**
*   **Input:** Target virtual path and payload bytes.
*   **Output:** Error if the target is not a regular file or permission denied; otherwise writes `content` atomically where possible (or in a single truncation+write pass).

### IsBinaryFile

Determines binary classification by scanning the first 1024 bytes of an opened file descriptor for null byte (`\x00`) characters. A presence of any null byte within the scanned window classifies the content as binary; absence implies text/ASCII. This heuristic avoids full-file parsing overhead while maintaining high signal-to-noise ratio for common binary formats (images, executables, archives).

```go
func IsBinaryFile(path string) bool { /* ... */ }
```

**Contract:**
*   **Input:** File path to inspect.
*   **Output:** Boolean; `true` if null byte detected within the first 1024 bytes.

---

## Repository Discovery

### DiscoverCodeFiles

Recursively walks the repository root, collecting high-signal source file paths while filtering out build artifacts (e.g., `.o`, `.class`, `node_modules/`), third-party dependencies, and custom ignore patterns defined in a configurable exclusion list. The traversal uses standard library path-walkers with explicit pruning logic to skip directories matching known artifact signatures.

```go
func DiscoverCodeFiles(root string) ([]string, error) { /* ... */ }
```

**Contract:**
*   **Input:** Repository root absolute path.
*   **Output:** Sorted slice of high-signal source file paths; error if the walk encounters unreadable directories or permission errors that cannot be recovered from.

---

## Git Operations

### RunGit

Executes a git command within the specified repository directory, capturing both stdout and stderr streams. It returns the combined output string along with an exit code status, handling both successful execution (exit 0) and failure cases (non-zero exit codes). The subprocess is spawned via `os/exec.Command` or equivalent, inheriting parent environment variables unless explicitly overridden.

```go
func RunGit(repoDir string, args ...string) (string, error) { /* ... */ }
```

**Contract:**
*   **Input:** Repository directory and git subcommand arguments.
*   **Output:** Git command stdout; error if the subprocess exits non-zero or fails to spawn.

### GetGitHead

Returns the current HEAD commit hash for the given repository root by delegating to `RunGit` with arguments equivalent to `git rev-parse --short=80 HEAD`. The returned string is trimmed of whitespace and validated against a hex character set; if validation fails, an error is propagated indicating an invalid or detached state.

```go
func GetGitHead(repoDir string) (string, error) { /* ... */ }
```

**Contract:**
*   **Input:** Repository directory path.
*   **Output:** 80-character hex SHA hash of the current HEAD; error if HEAD is not reachable or git execution fails.

---

## Hashing & Registry

### HashRepoRoot

Computes and returns the hexadecimal SHA-256 hash of the resolved absolute path provided as input. The implementation resolves symlinks to obtain a canonical absolute path before hashing, ensuring deterministic output regardless of filesystem aliasing. This utility is typically used for indexing repository states or verifying integrity in registry operations.

```go
func HashRepoRoot(path string) (string, error) { /* ... */ }
```

**Contract:**
*   **Input:** Any file system path (relative or absolute).
*   **Output:** 64-character hex SHA-256 digest; error if the path cannot be resolved to a canonical form.