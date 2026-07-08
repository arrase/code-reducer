# Module Overview

Enforces filesystem isolation and process synchronization primitives for repository-level operations. Centralizes path validation against an absolute root boundary, manages concurrent access via atomic file-based locking (PID-pinned), and maintains version control hygiene by integrating lock artifacts into `.gitignore`. All state transitions revolve around the `repoRoot` absolute path as a constant security boundary.

## Path Validation and Traversal Protection

### SafeResolve

**Signature:** `SafeResolve(repoRoot, inputPath string) (string, error)`

Resolves an arbitrary user-supplied `inputPath` against the canonical absolute `repoRoot`. Performs cleanup of the input string to neutralize relative references (`..`, single-dot), then resolves the resulting path against the repository root. Returns a fully qualified absolute path if the resolved target remains within the `repoRoot` boundary; otherwise, returns an error to signal a traversal attempt or invalidation.

### Data Flow

1.  **Input:** `inputPath` (untrusted string) + `repoRoot` (absolute, trusted constant).
2.  **Sanitization:** `inputPath` is normalized to eliminate relative components and redundant separators.
3.  **Resolution:** OS-level path resolution joins the sanitized input with `repoRoot`.
4.  **Boundary Check:** The resolved string is compared against the prefix of `repoRoot`.
5.  **Output:** Validated absolute path or error indicating traversal/invalidation failure.

## File-Based Process Locking

### SimpleLock Struct

**Signature:** `type SimpleLock struct { ... }`

Encapsulates the state required to manage a single process-level lock:

*   **LockFilePath:** Absolute filesystem location of the lockfile (typically `<repoRoot>/.code-reducer.lock`).
*   **FileHandle:** OS file descriptor representing an acquired exclusive hold on the lockfile.

### AcquireLock

**Signature:** `AcquireLock(repoRoot string) (*SimpleLock, error)`

Establishes a new process-level lock using atomic file semantics:

1.  Opens `<repoRoot>/.code-reducer.lock` with `O_EXCL` (exclusive creation).
2.  Writes the current process PID (`os.Getpid()`) to the newly created file handle.
3.  Returns a pointer to the resulting `SimpleLock`, holding the open file handle for subsequent lifecycle management.

### Unlock

**Signature:** `Unlock() error`

Releases the acquired lock:

1.  Flushes and closes the underlying OS file handle associated with the `SimpleLock`.
2.  Removes the lockfile from disk via filesystem delete operation.
3.  Returns an error if removal fails (e.g., permission denied, path not found).

## Repository Configuration Maintenance

### EnsureGitignoreHasLockfile

**Signature:** `EnsureGitignoreHasLockfile(repoRoot string) error`

Ensures the repository's `.gitignore` file tracks the lock artifact to prevent accidental version control inclusion:

1.  Locates `<repoRoot>/.gitignore`.
2.  Checks for existing content containing the entry `.code-reducer.lock`.
3.  If absent, appends the entry to the file (creating it if nonexistent).