# security.go — Repository Security Module

## Module Responsibility

This module provides two non-negotiable security primitives for a repository-level tool:

1. **Path traversal prevention** — `SafeResolve` ensures any input path resolves strictly inside the repository root, even when symlinks are involved.
2. **Process-level locking** — `AcquireLock` / `SimpleLock` / `Unlock` prevents concurrent execution by multiple processes of the same tool.

**Data flow:** `SafeResolve(repoRoot, inputPath)` → `(string, error)`. `AcquireLock(repoRoot)` → `(*SimpleLock, error)`. `(*SimpleLock).Unlock()` → `error`. No global mutable state exists; all constants and sentinel errors are immutable.

---

## Path Traversal Prevention

### `SafeResolve(repoRoot, inputPath string) (string, error)`

Cleans `inputPath` and verifies the resolved result lies strictly inside the repository root.

**Algorithm:**

1. `filepath.Abs(repoRoot)` — canonicalize the repository root to an absolute path.
2. `filepath.EvalSymlinks(repoRoot)` — resolve symlinks on the repository root itself.
3. Walk up the input path from the root, stopping at the first physically existing ancestor (checked via `os.Lstat`).
4. `filepath.EvalSymlinks` on that existing ancestor.
5. Rebuild the full path by appending the non-existent suffix components.
6. `filepath.Rel` computes the relative path from the resolved root to the rebuilt path. If the result is not `""`, return `ErrPathTraversal`.

**Error propagation:** Returns `(string, error)`. Errors are wrapped with `fmt.Errorf` using `%w` for chainability. `ErrPathTraversal` is returned directly as a sentinel.

---

## Process-Level Locking

### `AcquireLock(repoRoot string) (*SimpleLock, error)`

Acquires a simple file lock in `repoRoot` using `O_EXCL` for atomicity.

**Algorithm:**

1. Resolve the lock file path using `SafeResolve`.
2. `os.OpenFile` with `O_WRONLY|O_CREATE|O_EXCL`. If the file already exists, return `ErrLockHeld`.
3. Write the current process PID (`os.Getpid`) to the file.
4. Return a `*SimpleLock` handle.

**Error propagation:** If `OpenFile` fails with `os.IsExist`, wraps `ErrLockHeld`. If `Write` fails, the file is closed and removed before returning a wrapped error.

### `(*SimpleLock) Unlock() error`

Releases the lock by closing the file and removing it. Idempotent and thread-safe.

**Algorithm:**

1. Acquire the internal `sync.Mutex` (`l.mu`).
2. If `l.closed` is true, return `nil`.
3. Close the file descriptor.
4. Remove the lock file. If both close and remove fail, return the error from the non-nil one.

**Error propagation:** If close succeeds but remove fails (and is not `os.IsNotExist`), the remove error is assigned to `err`. If close fails, the remove error is ignored.

---

## Types

### `SimpleLock`

```go
type SimpleLock struct {
    lockPath string
    file     *os.File
    mu       sync.Mutex
    closed   bool
}
```

- `lockPath` — absolute path to the lock file.
- `file` — open file descriptor for the lock.
- `mu` — protects `closed` field.
- `closed` — set to `true` in `Unlock()`; guards idempotency.

---

## Constants and Errors

### Constants

- `LockFileName` = `".code-reducer.lock"`
- `defaultFilePerm` = `0644`

### Error Variables

- `ErrPathTraversal` = `errors.New("security violation: path traversal detected")`
- `ErrLockHeld` = `errors.New("lock is already held by another process")`

---

## External I/O Interactions

All operations target the local filesystem. No network, database, or API interactions.

| Operation | Purpose |
|-----------|---------|
| `filepath.Abs` | Resolves absolute path for `repoRoot` |
| `filepath.EvalSymlinks` | Resolves symlinks on `repoRoot` and intermediate ancestors |
| `os.Lstat` | Checks existence of path components during traversal resolution |
| `os.OpenFile` | Opens the lock file with `O_WRONLY|O_CREATE|O_EXCL` |
| `os.Remove` | Removes the lock file during `Unlock()` and on write failure in `AcquireLock()` |
| `os.Getpid` | Reads process ID (no external I/O) |
| `filepath.Rel` | Computes relative path to verify containment |

---

## Synchronization

- `SimpleLock.Unlock()` uses `sync.Mutex` (`l.mu`) to protect the `closed` field.
- `AcquireLock()` is a package-level function that creates a new `SimpleLock` instance; it does not use any synchronization mechanism.

---

## Error Handling Summary

- Errors are not logged or printed; they propagate to the caller.
- No `panic`/`recover` is used.
- Sentinel errors (`ErrPathTraversal`, `ErrLockHeld`) are returned directly.
- All other errors are wrapped with `fmt.Errorf` using `%w` for chainability.