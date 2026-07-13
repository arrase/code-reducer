# Module: internal/security

## Responsibility

Enforces security boundaries and inter-process exclusivity during Code-Reducer operations. The module prevents directory-traversal attacks by constraining resolved paths within the repository root, ensures shared resources are not accessed concurrently via file locking, and integrates with version control to keep lock state out of tracked content. No mutable package-level state exists; all concurrency control is instance-scoped.

## Data Flow

The two sentinel errors defined in `errors.go` (`ErrPathTraversal`, `ErrLockHeld`) serve as the terminal failure signals for path-validation and lock-contention scenarios. Callers receive these values through standard Go error-return patterns and propagate them via `if err != nil` checks; no panic, crash, or custom wrapping occurs within this package. The actual I/O operations that trigger these errors reside in other files not shown here.

## Public API: `SimpleLock`

### Type Definition

```go
type SimpleLock struct {
    lockPath string
    file     *os.File
    mu       sync.Mutex
    closed   bool
}
```

**State characteristics:**

- **`lockPath string`** — read-only after construction; never reassigned within this package. Assigned exclusively by the constructor.
- **`file *os.File`** — modified inside `Unlock()` (`l.file = nil`) and assigned once in `AcquireLock()`.
- **`mu sync.Mutex`** — value-type mutex embedded on each instance. Acquired and released only within `Unlock()` via defer.
- **`closed bool`** — modified inside `Unlock()` (`l.closed = true`).

### Methods

#### `Unlock() error`

Closes the underlying file descriptor, removes the lockfile atomically (ignoring errors if the file no longer exists), and sets internal state to closed. Safe to call multiple times or from different goroutines without deadlocking. Returns a non-nil error only when both close and remove fail; otherwise returns nil. No panic path observed.

#### `SafeResolve(repoRoot string, inputPath string) (string, error)`

Resolves an arbitrary input path against the repository root boundary. Walks upward via `os.Lstat` until hitting the first physically-existing directory or reaching the root (`parent == current`). Symlinks are evaluated on existing ancestor components to defeat symlink-based traversal attacks. Non-existent suffix components are appended after the walk completes. If the relative path starts with `..`, it is rejected as a security violation and returns `ErrPathTraversal`. Any stat returning something other than ENOENT during the upward walk is returned immediately without masking — this could surface unexpected errors (e.g., EACCES) as fatal rather than being handled.

#### `AcquireLock() (*SimpleLock, error)`

Resolves a lockfile path within the repository root using `SafeResolve`; atomically creates the file via O_WRONLY|O_CREATE|O_EXCL to prevent concurrent acquisition from another process; writes the current PID into the newly-created file. If lock path cannot be resolved → returns nil plus the original error from SafeResolve. If O_EXCL open fails with EEXIST → wraps `ErrLockHeld` and includes lockPath in message. If write to lockfile fails → closes file, removes lockfile, then returns wrapped error (prevents stale locks). No panic or crash path observed.

#### `EnsureGitignoreHasLockfile(lockPath string) error`

Reads `.gitignore`; checks whether it already contains an entry for the lockfile; if not, appends a commented entry under a temp file written atomically via rename. Uses `Sync()` before rename for durability. Failure modes: unreadable `.gitignore` → propagates upstream error; read failure (non-ENOENT) → wrapped; temp-file creation/write/sync/chmod/rename failures → each wrapped with descriptive messages. Partial writes are not persisted if sync fails (atomic-ish via same-directory pattern).

## Error Propagation Summary

| Function | Failure Mode | Caller Impact |
|---|---|---|
| `SafeResolve` | Absolute path error, symlink resolution failure, stat-on-nonexistent-component error (only if not ENOENT), or path-traversal detection via `ErrPathTraversal`. | Any caller of `AcquireLock` or `EnsureGitignoreHasLockfile` receives the wrapped error directly. |
| `AcquireLock` | Lock path cannot be resolved → returns nil + original error from SafeResolve. O_EXCL open fails with EEXIST → wraps `ErrLockHeld`. Write failure → closes file, removes lockfile, then returns wrapped error. | Caller receives non-nil error; no lock is held. No panic or crash path observed. |
| `SimpleLock.Unlock()` | Close of underlying file fails → captured as err. Removal failure (other than ENOENT) only considered if close succeeded and removal also failed — otherwise swallowed (`if err == nil { err = removeErr }`). | Returns non-nil error only on double-failure; otherwise returns nil. No panic path. |
| `EnsureGitignoreHasLockfile` | `.gitignore` cannot be resolved → propagates upstream. Read fails (non-ENOENT) → wraps error message. Temp file creation fails → wrapped. Write to temp file fails → wrapped. Sync fails → wrapped and returns early; subsequent close/rename skipped. Chmod fails → wrapped. Rename fails → wrapped. | Caller receives a descriptive error for each failure point. Sync-before-rename pattern means partial writes are not persisted if sync fails (atomic-ish). |

## Notable Patterns / Risks

- **`SafeResolve` loop**: The `for` loop calling `os.Lstat` walks upward until it finds an existing ancestor or reaches the root (`parent == current`). If a non-existent-component stat returns something other than ENOENT, it is returned immediately — this could mask unexpected errors (e.g., EACCES on a directory) as fatal rather than being handled.
- **`.gitignore` rename**: The temp-file + rename pattern ensures atomicity but requires write access to the parent directory of `.gitignore`. Failure modes are well-covered, though no retry logic exists for transient I/O errors (e.g., NFS timeout during `Sync`).
- **Lockfile cleanup on write failure**: In `AcquireLock`, if PID write fails, the lockfile is explicitly removed before returning error — this prevents stale locks from being left behind.