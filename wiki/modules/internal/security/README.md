# Module: `internal/security` — Path Traversal Prevention and Process-Level Locking

## Module Responsibility

This module provides two security primitives scoped to a repository root:

1. **Path traversal prevention** via `SafeResolve`, which validates that user-supplied paths resolve within the repository boundary, resolving symlinks on existing ancestor parts before comparison.
2. **Cross-process lock management** via `SimpleLock`/`AcquireLock`/`Unlock`, which ensures only one process holds exclusive access to a protected resource at any given time through an atomic file-based lock mechanism.

Both primitives operate against the local filesystem; no network, database, or remote API interactions occur within production code. All errors are returned to callers and propagated via Go's `error` interface; panics are not raised by this module.

## Data Flow Overview

1. Callers invoke `SafeResolve(repoRoot, inputPath)` with a candidate path string and an anchor directory; the function returns either a resolved absolute path confined within `repoRoot` or an error (typically `ErrPathTraversal`).
2. Callers invoke `AcquireLock(repoRoot)` to obtain a lock handle; if contention is detected—either via OS-level atomic failure of `O_EXCL` or presence of a stale lockfile—the function returns an error wrapping the sentinel `ErrLockHeld`.
3. When release is needed, callers call `Unlock()` on the returned `*SimpleLock`; this method is idempotent and thread-safe with respect to itself, closing the file descriptor and removing the lockfile from disk under a mutex guard.

## Error Definitions — `errors.go`

The module declares two sentinel error variables used as context markers during error propagation:

| Sentinel | Declaration Scope | Usage Context |
|---|---|---|
| `ErrPathTraversal` | package-level in `internal/security/errors.go` | Returned by `SafeResolve` when the resolved path escapes outside `repoRoot`. The original traversal signal is swallowed and replaced with a formatted message carrying `inputPath` as context. |
| `ErrLockHeld` | package-level in `internal/security/errors.go` | Returned by `AcquireLock` when another process holds the lock or a stale lockfile persists; returned via `%w` wrapping of the underlying OS error with descriptive context. |

No functions, imports beyond `errors`, or runtime operations exist in this file. Error propagation from these sentinels (e.g., further wrapping via `fmt.Errorf`) occurs elsewhere, not here.

## Path Resolution — `SafeResolve`

### Signature

```go
func SafeResolve(repoRoot, inputPath string) (string, error)
```

### Responsibility

Resolves a candidate path against an anchor root while preventing escape through symlinks and directory traversal. The function returns the cleaned absolute path if it remains strictly inside the resolved repository boundary; otherwise it returns `ErrPathTraversal`.

### Algorithm Steps

1. Compute the absolute root directory from `repoRoot` via `filepath.Abs`, wrapping any resulting error with `%w`.
2. Resolve symlinks on the absolute root using `filepath.EvalSymlinks(absRoot)`, again wrapping errors with `%w`.
3. Walk up from the joined path (`absRoot + inputPath`) until a physically existing ancestor is found via repeated `os.Lstat(current)` calls; each Lstat failure that is not "not exist" is wrapped and returned immediately.
4. Resolve symlinks on the first physically existing ancestor via `filepath.EvalSymlinks(current)`, wrapping errors with `%w`.
5. Reconstruct the full target path from the resolved ancestor plus all previously-skipped components.
6. Verify that the reconstructed path remains inside the resolved root; reject if it escapes by returning a wrapped error using `ErrPathTraversal` with `inputPath` as context.

### External I/O Operations

| Operation | Function | Purpose |
|---|---|---|
| Absolute path resolution from relative input | `filepath.Abs(repoRoot)` | Establishes the canonical root baseline for comparison |
| Symlink resolution on root | `filepath.EvalSymlinks(absRoot)` | Prevents symlink-based escape at the boundary |
| Ancestor existence check | `os.Lstat(current)` loop | Walks up the path tree stopping at first physically existing parent |
| Symlink resolution on ancestor | `filepath.EvalSymlinks(current)` | Prevents symlink-based escape mid-path |

## Lock Acquisition and Release — `SimpleLock`

### Types

#### `SimpleLock` Struct

| Field | Type | Mutability | Notes |
|---|---|---|---|
| `lockPath` | `string` | Modified once during acquisition, then read-only thereafter. Not mutated by any other method. | Absolute path of the lockfile within the repository root. |
| `file` | `*os.File` | Modified once during acquisition. Closed during unlock and removed from disk afterward. | File descriptor holding the exclusive lock; opened with `O_WRONLY\|O_CREATE\|O_EXCL`. |
| `mu` | `sync.Mutex` | Read via lock/unlock primitives only. Field itself is never reassigned after struct initialization. | Protects the close-and-remove sequence in `Unlock()`, making unlock atomic with respect to itself. |
| `closed` | `bool` | Set to `true` during unlock; read inside unlock for idempotency check (`if l.closed { return nil }`). | Tracks release state so subsequent calls return immediately without further I/O. |

#### Methods

##### `AcquireLock(repoRoot string) (*SimpleLock, error)`

**Responsibility**: Acquires an exclusive file-based lock within the provided repository root. Uses O_EXCL to ensure atomicity; failure implies another process holds the lock or a stale lockfile exists.

**Algorithm Steps**:
1. Calls `SafeResolve(repoRoot, LockFileName)` to obtain the canonical lock file path inside the repo root. If this fails, the error propagates directly without wrapping.
2. Opens the lockfile with `os.OpenFile(lockPath, O_WRONLY\|O_CREATE\|O_EXCL, 0644)`. The OS-level atomicity of O_EXCL means failure indicates another writer holds the file or a stale lock persists; this is treated as an error condition requiring manual cleanup by the caller.
3. Writes the current process PID into the lockfile via `f.Write([]byte(fmt.Sprintf("%d\n", os.Getpid())))` for identification/inspection purposes. If this write fails, the method closes the file and removes it before returning a wrapped error.

**Error Propagation**:
- Direct propagation when `SafeResolve` fails (no wrapping).
- Wraps with `%w` using `ErrLockHeld` plus context string describing stale lockfile when the OS reports `os.IsExist`.
- On write failure after successful OpenFile: closes file and removes it, then returns a wrapped error that swallows close/remove errors into a single formatted message—only the original write error is surfaced.

##### `Unlock() error` (method on `*SimpleLock`)

**Responsibility**: Releases the lock by closing the file descriptor and removing the lockfile from disk. Idempotent and thread-safe with respect to itself.

**Algorithm Steps**:
1. Acquires `l.mu.Lock()` at entry; releases via `defer mu.Unlock()`.
2. Checks `if l.closed { return nil }`; if already closed, returns immediately without further I/O.
3. Closes the file descriptor (`l.file.Close()`). If this fails, the error is swallowed when reporting removal errors—only surfaces the remove error if it subsequently fails.
4. Attempts to remove the lockfile from disk via `os.Remove(l.lockPath)`.

**Thread Safety**: The struct owns its own mutex; concurrent calls to `Unlock` on the same instance serialize through `mu.Lock()/defer mu.Unlock()`, making the operation atomic with respect to itself. Concurrent acquisition of different instances each creates independent `SimpleLock` objects with separate mutexes—no contention between instances is modeled or guaranteed by this code.

## Test Coverage — `security_test.go`

### Functions Under Test

| Function | Parameters | Return Values |
|---|---|---|
| `SafeResolve(repoRoot, inputPath)` | repository root path; input file/directory path to resolve | resolved absolute path (string); error |
| `AcquireLock(repoRoot)` | repository root path | lock object (`*SimpleLock`); error |

### Test Fixture Operations

All operations below target the local filesystem via Go's standard library:

| Operation | Function | Purpose | Error Handling |
|---|---|---|---|
| Directory creation for test fixtures | `os.MkdirAll()` | Sets up `src/sub/` directories | Propagated to test framework via `t.Fatal(err)` — terminates immediately, no recovery. |
| File writing for traversal tests | `os.WriteFile()` | Creates `file.txt`, `..config` | Same as above; propagated to `t.Fatal(err)`. |
| Symlink creation (conditional) | `os.Symlink()` | Creates a symlink pointing outside the repo root | Swallowed and replaced with `t.Skip("Symlinks not supported on this OS/filesystem")`; test continues if operation fails. |

### Test Assertions

- `security.SafeResolve(repoRoot, inputPath)` returns an error to its caller; tests verify both success and failure paths via a `wantErr` flag comparison (`t.Errorf(...)`).
- `security.AcquireLock(repoRoot)` returns `(lock, err)`. A second concurrent call failing with `AcquireLock() should have failed when lock is already held` confirms the production code signals contention via error return.

## Concurrency and Synchronization Summary

- No package-level variables are declared anywhere in this module; all mutable state is encapsulated within individual `SimpleLock` instances.
- `Unlock()` self-synchronizes via `mu.Lock()/defer mu.Unlock()`, making it safe for concurrent calls on the same instance.
- Concurrent calls to `AcquireLock` on different instances each create their own independent `SimpleLock` with separate mutexes—no contention between instances is modeled or guaranteed by this code.
- The file-level lock (the `.code-reducer.lock` file itself) provides OS-level mutual exclusion, which complements the in-memory `mu`.
- No locks/mutexes are used within the test file itself; each `Test*` function runs independently in a single goroutine.