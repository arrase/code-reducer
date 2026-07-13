# internal/security — Repository-Safe Path Resolution & Locking

## Module Responsibility

This package provides two safety primitives for a repository-aware tool: preventing directory escape during path resolution, and coordinating concurrent access through an exclusive lockfile. All operations are scoped to local filesystem state within the repository tree; no network calls or external APIs are invoked.

## Data Flow

```
┌─────────────┐    ┌──────────┐    ┌──────────────┐    ┌──────────────┐
│  Caller     │───▶│ SafeResolve│───▶│ AcquireLock   │───▶│ SimpleLock  │
│             │    │           │    │               │    │ (instance)  │
└─────────────┘    └──────────┘    └──────────────┘    └──────────────┘
                                                              │
                                                              ▼
                                                       Unlock() → nil error
```

`SafeResolve` is invoked first by `AcquireLock` to guarantee the lockfile path lies within the symlink-resolved repo root. Both sentinel errors (`ErrPathTraversal`, `ErrLockHeld`) are defined in a separate file and returned only from downstream handlers; this package does not propagate them internally.

---

## Sentinel Errors — `errors.go`

### ErrPathTraversal

Returned by external path validation logic when a resolved target lies outside the repository root boundary. Defined as an `errors.New()` sentinel with payload prefixed `"security violation:"`. No I/O or state mutation occurs in this file; the error exists solely for consumer handlers to return from their respective call sites.

### ErrLockHeld

Returned by external lock acquisition logic when another process already holds the shared resource. Also defined as an `errors.New()` sentinel with payload prefixed `"security violation:"`. The package-level description distinguishes inter-process contention ("another process") from intra-process re-entry, which this error does not signal.

---

## Path Sanitization — `SafeResolve`

**Signature:** `func SafeResolve(repoRoot string, inputPath string) (string, error)`

### Algorithm Steps

1. Convert `repoRoot` to an absolute path via `filepath.Abs`.
2. Resolve symlinks on the existing ancestor parts of the joined (`root + input`) path by walking upward until a physically existing directory is found.
3. Rebuild the full path from the resolved ancestor and any non-existent suffix components.
4. Verify the resulting path lies strictly within the symlink-resolved repo root using `filepath.Rel`; reject if the relative path starts with `".."`.

### Error Handling

- A specific `os.IsExist(err)` branch is checked after opening the lockfile with `O_EXCL` to distinguish "lock held by another process" from generic I/O errors. The existence case returns a user-facing message; other OS errors are wrapped and propagated upward.
- Write failures during PID capture trigger cleanup: the file is closed and removed before returning an error, preventing stale partial lockfiles.

---

## Exclusive Locking — `AcquireLock` & `SimpleLock.Unlock()`

### AcquireLock

**Signature:** `func AcquireLock(repoRoot string) (*SimpleLock, error)`

1. Resolve the lockfile path via `SafeResolve` so it is guaranteed inside the repo root.
2. Open the file with `O_EXCL` (exclusive creation) to atomically detect whether another process holds the lock.
3. If acquisition fails due to existence, return an error suggesting manual cleanup of a stale lockfile.
4. Write the current PID into the newly created lockfile.

### SimpleLock

**Fields:** All fields are lowercase/private (`closed`, `mu`, etc.). The struct embeds `sync.Mutex` for serialization of unlock calls on the same instance only; concurrent acquisition across different instances (different processes) is by design—each process gets its own lock.

**Instance-Level Mutable State:**
| Field | Type | Modified By | Synchronization |
|-------|------|-------------|-----------------|
| `closed` | `bool` | `Unlock()` sets to `true` | Internal `sync.Mutex` (`mu`) |

### Unlock

**Signature:** `func (l *SimpleLock) Unlock() error`

1. Close the underlying file and remove the lockfile from disk; treat missing-file errors as benign so repeated calls do not fail.
2. If both close and remove fail: close error is returned only if no other error occurred; otherwise the remove error (non-`IsNotExist`) takes precedence.
3. Idempotent: checks `l.closed` first; if already released, returns nil without further I/O.

---

## Gitignore Maintenance — `EnsureGitignoreHasLockfile`

**Signature:** `func EnsureGitignoreHasLockfile(repoRoot string) error`

1. Read `.gitignore` (or tolerate its absence).
2. Append a commented entry for the lockfile if it is not already present.
3. Write the updated content to a temporary file, then atomically rename it over the original `.gitignore`.

### Atomicity Guarantees

- `os.CreateTemp(dir, ".gitignore.tmp.*")` creates a temp file in the same directory as `.gitignore`, ensuring rename is atomic on the filesystem.
- `tmpFile.Sync()` forces OS-to-disk flush before close.
- `os.Rename(tmpName, gitignorePath)` atomically replaces `.gitignore` with the new content via rename; temp file cleanup runs via a deferred function always executed regardless of success or failure path.

---

## Concurrency Model

### Package-Level State
No mutable state at package level. All constants (`LockFileName`) are compile-time immutable values.

### Instance-Level State
Only `SimpleLock` carries instance-local state protected by an internal mutex for the unlock path. No global counters, shared caches, or concurrently-accessed variables exist across instances.