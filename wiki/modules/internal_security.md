# internal/security — Path Containment & Process Mutual Exclusion

## Module Responsibility

This module enforces two non-negotiable invariants for any code-reducer session: (1) the working directory must remain strictly within the repository root boundary, and (2) only one process may hold an exclusive lock on the shared resource at any given moment. It also maintains a `.gitignore` entry that excludes the lockfile from version control while ensuring it is automatically tracked if absent.

The module communicates with the outside world indirectly: `security.go` performs filesystem I/O and returns sentinel error values to callers; those sentinels are defined in `errors.go`. No panic recovery, no error wrapping beyond `%w`, no silent swallowing of non-trivial failures occurs within these files.

## Sentinel Error Contract (`errors.go`)

### Package-Level Variables

| Identifier | Type | Purpose |
|---|---|---|
| `ErrPathTraversal` | `error` | Returned when a resolved path escapes outside the repository root boundary |
| `ErrLockHeld` | `error` | Returned when another code-reducer process holds an exclusive file lock on the shared resource |

Both variables are initialized once via `errors.New` and remain read-only throughout their scope. No mutable state, no concurrency primitives, no panic handling exists in this file. Callers detect violations by matching against these sentinels using `errors.Is(err, ErrPathTraversal)` or equivalent patterns; because the errors are unwrapped as-is (no wrapping), identity is preserved directly through the error chain.

### Domain Concepts

- **Path Traversal Violation** (`ErrPathTraversal`) — signals when a resolved path escapes outside the repository root boundary. This is a classic path traversal / directory escape guardrail.
- **Lock Conflict** (`ErrLockHeld`) — signals when another process already holds an exclusive file lock, indicating contention on a shared resource.

## Lock Acquisition and Release (`security.go` — `SimpleLock`)

### Struct Definition

```go
type SimpleLock struct {
    lockPath string
    file     *os.File
    mu       sync.Mutex
    closed   bool
}
```

| Field | Type | Semantics |
|---|---|---|
| `lockPath` | `string` | Absolute path to the lockfile on disk |
| `file` | `*os.File` | Open file descriptor for the lockfile, or nil if not acquired |
| `mu` | `sync.Mutex` | Protects internal struct fields during `Unlock()` |
| `closed` | `bool` | Marks whether the lock has been released and the file descriptor closed |

### `*SimpleLock.Unlock()`

Closes the lock file descriptor and removes the lockfile in a thread-safe manner.

**Idempotency contract:** Calling `Unlock()` on an already-closed lock returns `nil`. This is intentional, not accidental.

**Error handling inside the method:**
- If `l.closed == true` → return `nil`
- Close the file descriptor (`os.File.Close`) → error captured in local variable; if close fails, set `file = nil` so a subsequent close becomes a no-op
- Remove the lockfile via `os.Remove`
- If *both* close and remove fail: remove error overwrites prior close error only when close returned without error. If close failed first, any remove error is ignored. This means the final returned error reflects whichever operation last produced an error, not necessarily the most severe one — an intentional design choice worth flagging for callers.
- If `os.IsNotExist(err)` on remove → silently accepted as normal idempotent cleanup

## Path Resolution (`security.go` — `SafeResolve`)

### Signature

```go
func SafeResolve(repoRoot, inputPath string) (string, error)
```

**Responsibility:** Resolve any input path strictly inside the repository root. The function performs a multi-step containment check to defeat symlink-based escape vectors and physical directory traversal.

### Algorithmic Steps

1. Call `filepath.Abs` on `inputPath`
2. Evaluate symlinks on the existing ancestor via `EvalSymlinks` — prevents symlink-based escape from parent directories that exist but are symbolic links
3. Walk up from the target until a physically-existing directory is found, then re-evaluate symlinks on that ancestor via `EvalSymlinks` again
4. Rejoin with the remaining suffix components after the physical ancestor boundary
5. Verify final result is under `resolvedRoot` via relative path check (`filepath.Rel`)

### Error Handling

| Condition | Handling |
|---|---|
| Any error from `Abs`, first `EvalSymlinks`, second `EvalSymlinks`, or `Lstat` (non-exist) | Wrapped with descriptive message using `fmt.Errorf(..., err)` — underlying cause recovered via `%w` |
| Unrecognized error from `Lstat` (`!os.IsNotExist`) | Returned immediately as wrapped error |
| Path escapes repo root (`..` prefix or non-nil rel) | Returns sentinel-style error wrapping `ErrPathTraversal` with the original input path |

## Lock Acquisition (`security.go` — `AcquireLock`)

### Signature

```go
func AcquireLock(repoRoot string) (*SimpleLock, error)
```

**Responsibility:** Establish an exclusive process-level lock on a shared resource. Uses `O_EXCL` flag for atomic creation of the lockfile — if the lockfile already exists, acquisition fails immediately without silent success.

### Lockfile Lifecycle

| Phase | Mechanism |
|---|---|
| Resolve path | Calls `SafeResolve(repoRoot)` to obtain absolute lock path |
| Create atomically | Opens file with `O_WRONLY \| O_CREATE \| O_EXCL` — atomic exclusive creation prevents race between existence check and open |
| Record identity | Writes PID of the acquiring process to the newly created lockfile |
| Failure cleanup | On failure: closes file, removes lockfile |

### Error Handling

| Condition | Handling |
|---|---|
| `SafeResolve` fails | Propagated as-is to caller |
| File open fails AND error is `os.IsExist` (lock already held) | Returns wrapped sentinel error wrapping `ErrLockHeld`, including the lock path — caller can detect stale-lock condition specifically via `errors.Is(err, ErrLockHeld)` |
| File open fails with any other error | Wrapped: `"failed to acquire lock at %s: <cause>"` |
| Write PID fails | File closed, lockfile removed (cleanup), wrapped error returned |

## Gitignore Maintenance (`security.go` — `EnsureGitignoreHasLockfile`)

### Signature

```go
func EnsureGitignoreHasLockfile(repoRoot string) (error)
```

**Responsibility:** Maintain the convention that the lockfile is excluded from version control but automatically tracked if missing. Appends the lockfile entry to `.gitignore` only if not already present; uses atomic rename via temp file + sync before close to prevent partial writes.

### Algorithmic Steps

1. Resolve `.gitignore` path via `SafeResolve(repoRoot)`
2. Read existing `.gitignore` content (`os.ReadFile`)
3. Parse lines, check if lockfile entry already exists
4. If not present: construct new content with the lockfile entry appended (with trailing-newline normalization)
5. Create a temp file in same directory as `.gitignore`
6. Write to temp file → call `Sync()` on it → close it → `Chmod` it
7. Rename temp file over original `.gitignore`

### Error Handling

| Condition | Handling |
|---|---|
| `SafeResolve` fails | Propagated as-is |
| Read fails AND not `os.IsNotExist` (file exists but read error) | Wrapped: `"error reading .gitignore: <cause>"` — caller must handle missing-file vs I/O-error distinction separately |
| CreateTemp fails | Wrapped with descriptive message including `%w` |
| Write to temp file fails | Wrapped; temp file is NOT cleaned up (defer handles close + remove, but write error returns before cleanup path executes) |
| Sync fails | Wrapped — note that `os.Chmod` and subsequent rename may have already run on a partially-written temp file if this error occurs late in the sequence |
| Close fails | Wrapped |
| Chmod fails | Wrapped |
| Rename (atomic swap) fails | Wrapped with descriptive message |

The deferred function (`tmpFile.Close()` + `os.Remove(tmpName)`) runs regardless of return path. Because the function returns before the defer has a chance to execute in some failure paths, cleanup may not happen for intermediate states — but since we're using temp file with unique name and same-directory rename, the OS-level temp file is cleaned by `os.Remove` in the defer on any error return path. If *write* fails mid-stream before close/rename, the deferred Remove will clean up the partially-written temp file.

## Data Flow Summary

```
[Initialize] → [Ensure path safety for all repo-root operations (SafeResolve)] → 
[If no process holds the lock] → [Create lockfile with PID via O_EXCL (AcquireLock)] → 
[Run business logic within locked session] → 
[Append entry to .gitignore if needed (EnsureGitignoreHasLockfile)]
```

The two non-obvious constraints are: (1) path resolution walks symlinked ancestors before rejoining suffix components, and (2) gitignore updates use atomic rename with explicit sync before close.

## Key Patterns Observed Across the Module

1. **Error wrapping throughout** — All functions use `fmt.Errorf(..., err)` with `%w`, making root-cause recovery possible via `errors.Is()`.
2. **Custom sentinel errors** — At least two custom error variables referenced: `ErrPathTraversal` and `ErrLockHeld`. These enable typed error detection by callers without requiring unwrapping logic on this file's side.
3. **No panic anywhere** — All failure paths return to caller with an error value.
4. **Idempotent operations deliberately silent** — `Unlock()` on already-closed lock returns nil; missing `.gitignore` in EnsureGitignoreHasLockfile returns nil. These are design choices, not accidentals.
5. **Atomic rename pattern for .gitignore** — Write to temp → Sync → Close → Chmod → Rename is the standard atomic-update pattern. The `Sync()` call before close is explicit and unusual (normally deferred), but here it's called eagerly before close in this implementation.
6. **No global package-level mutable state** exists in either file — all state is either local to a function or encapsulated within the `SimpleLock` receiver, which is accessed only through its methods.