# Module: `security` (`security.go`)

## Responsibility & Data Flow

The `security` package implements two orthogonal safety guarantees for the Code-Reducer execution environment: **path containment enforcement** and **process serialization via file-based locking**. All public functions operate against an absolute repository root derived at initialization, ensuring that no downstream operation can escape the intended working directory. The data flow follows a lifecycle pattern:

1.  **Initialization:** `EnsureGitignoreHasLockfile` registers the lock artifact in `.gitignore` to prevent version control pollution.
2.  **Serialization Entry:** `AcquireLock` atomically claims an exclusive process slot by writing the current PID into `LockFileName` (`.code-reducer.lock`) using `O_EXCL`, returning a `SimpleLock` handle.
3.  **Execution Guardrail:** Any I/O operation must pass through `SafeResolve`, which canonicalizes relative paths and rejects any traversal attempt that would resolve outside the repository root.
4.  **Serialization Exit:** `Unlock` closes the underlying file descriptor and unlinks the lockfile, releasing the process slot for the next iteration or concurrent worker.

## Path Safety: `SafeResolve`

**Function:** `SafeResolve(path string) (string, error)`

Canonicalizes an input path into its absolute representation relative to the repository root. Performs a strict containment check; returns an error if the resolved target lies outside the boundary or if any traversal component (`..`) is detected pre-resolution. This prevents directory escape vectors and symlink-based bypasses during file system operations.

## Process Locking Protocol

### Initialization & Cleanup
**Function:** `EnsureGitignoreHasLockfile()`

Idempotently appends `LockFileName` to the repository's `.gitignore`. If the entry does not exist, it creates a new line; if present, it is left untouched. Ensures the lock mechanism remains ephemeral and local to the working tree.

**Function:** `Unlock(lock SimpleLock)`

Releases resources associated with an acquired `SimpleLock`. Closes the underlying file handle (`os.File`) and calls `os.Remove` on the lockfile path stored within the struct. This guarantees no stale PID files persist after process termination or intentional release, preventing deadlock conditions for subsequent acquire attempts.

### Lock Acquisition
**Function:** `AcquireLock()`

Establishes an exclusive process lock by opening `LockFileName` (`.code-reducer.lock`) with `O_WRONLY | O_CREATE | O_EXCL`. The `O_EXCL` flag enforces atomicity: if a previous instance holds the file, the open fails immediately. On success, the current PID is written to the file descriptor and flushed, then the handle is returned as a `SimpleLock`. This state represents the "locked" condition for the duration of the reduction task.

**Type:** `SimpleLock`

Struct wrapping the canonical lockfile path (`string`) and an associated `*os.File` handle. Represents the active ownership of the process slot. Functions `Unlock` and internal acquire logic consume this struct to manage lifecycle transitions between unlocked, locked, and closing states.

**Constant:** `LockFileName`

String constant defining the artifact name `.code-reducer.lock`. Serves as the sole input to path resolution during lock acquisition and cleanup.