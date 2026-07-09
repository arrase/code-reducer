# internal/security

## Module Responsibility

This module implements three infrastructure contracts for the Code-Reducer agent: path-boundary enforcement against directory-traversal attacks, process mutual exclusion via file locking, and lockfile version-control exclusion through `.gitignore` management. No synchronization primitives (`sync.Mutex`, channels, atomics) are used; concurrency safety is delegated to the OS-level `O_EXCL` semantics on lock acquisition.

## Domain Concepts and Business Rules

1. **Path-boundary enforcement** — `SafeResolve` requires any user-supplied path to resolve strictly inside the repository root. If a resolved absolute path escapes the root (detected via the `..` prefix check), the operation fails with an explicit violation error. This is a defense-in-depth rule against directory-traversal attacks.

2. **Process mutual exclusion** — The lock mechanism ensures only one Code-Reducer instance operates on a given repository at a time. Acquisition requires creating the lockfile atomically (via `O_EXCL`); if it already exists, acquisition fails with a "lock held" error. Release closes the file and removes the lockfile.

3. **Lockfile version-control exclusion** — The rule that `.code-reducer.lock` must appear in `.gitignore`. If the ignore file doesn't exist or lacks this entry, one is created/updated so the lockfile is not tracked by git.

## Data Structures

### `SimpleLock`

A value type representing an acquired lock on a specific repository root.

| Field | Type | Description |
|-------|------|-------------|
| `lockPath` | `string` | Absolute path to the lock file. Set in `AcquireLock`; read in `Unlock`. No reset after close. |
| `file` | `*os.File` | File handle opened during acquisition. After `Unlock()` the underlying file is closed and removed from disk but the field is never nilled out; subsequent calls to `Unlock()` will close an already-closed file (error swallowed). |

### Methods on `SimpleLock`

| Signature | Description |
|-----------|-------------|
| `(l *SimpleLock) Unlock() error` | Closes the lock file and removes it from disk. Returns an error if either step fails; individual errors are captured into local vars (`err1`, `err2`) and only one is ever returned (the second operation's error may be silently discarded in favor of the first). |

## Public Functions

### `SafeResolve(repoRoot, inputPath string) (string, error)`

Resolves and validates an input path against a repository root. Returns the cleaned absolute path or an error on traversal detection. Uses pure path manipulation: `filepath.Abs`, `filepath.Clean`, `filepath.Rel`. No actual disk read/write occurs during this call. The security check is performed via prefix inspection of the relative path after resolution.

### `AcquireLock(repoRoot string) (*SimpleLock, error)`

Acquires a simple file lock in the repo root. Requires creating the lockfile atomically (via `O_CREATE\|O_EXCL`); if it already exists, acquisition fails with a "lock held" sentinel message. On success writes PID (newline-terminated). If PID write subsequently fails, `f.Close()` and `os.Remove(lockPath)` are called explicitly — this cleans up the partially-created lockfile so another process can retry.

### `EnsureGitignoreHasLockfile(repoRoot string) error`

Ensures `.code-reducer.lock` is present in `.gitignore`. If the ignore file doesn't exist, one is created with a single line. Otherwise: scans for existing entry; if not found, appends a commented block plus the lockfile name. Opens `.gitignore` for appending with `O_APPEND\|O_WRONLY`; if append fails, returns a wrapped error. No partial state change occurs beyond the open handle (deferred close); no explicit cleanup of any partial content is performed on write failure after opening.

## Error Handling Patterns

1. **Wrap pattern (`fmt.Errorf %w`)** — Used in every failure path:
   - `SafeResolve`: wraps `filepath.Abs` error as `"failed to get absolute root path"`; wraps traversal detection as a hardcoded sentinel message.
   - `AcquireLock`: wraps the raw `os.OpenFile` error when it's not `IsExist`.
   - `EnsureGitignoreHasLockfile`: wraps stat/read/write errors individually.

2. **Sentinel / Hardcoded messages** — Not wrapped with `%w`, returned as-is:
   - `"security violation: path traversal detected"` — in `SafeResolve` when relative path starts with `..`.
   - `"lock at %s is already held by another process..."` — when `os.IsExist(err)` on OpenFile. Domain-specific sentinel for lock contention.

3. **Error swallowing** — `Unlock()` captures each step's error into local vars (`err1`, `err2`) and returns whichever is non-nil (or nil if both succeed). The second operation's error may be silently discarded in favor of the first, or vice versa — only one error is ever returned.

4. **No panic recovery** — None of these functions recover from panics. Any unexpected panic propagates to the caller with an empty stack trace in `runtime` (Go's default).