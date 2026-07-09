# `internal/security` Module Architecture

## Responsibility

This package provides path-traversal prevention, cross-process coordination via file-based locking, and `.gitignore` hygiene for the lock artifact. It operates on a single repository root; no global or package-level mutable state exists. All external I/O is filesystem-bound.

---

## Constants & Types

### `LockFileName`
```go
const LockFileName = ".code-reducer.lock"
```

Exported string constant identifying the lockfile artifact. Referenced by both `AcquireLock` and `EnsureGitignoreHasLockfile`.

### `SimpleLock`
```go
type SimpleLock struct {
    lockPath string
    file     *os.File
}
```

Per-instance value used only as the return from `AcquireLock`. Fields are private. Allocation is local to each call; no sharing across goroutines or process invocations.

---

## Data Flow: Lock Lifecycle

`AcquireLock` → `SimpleLock.Unlock()` forms a linear lifecycle. `Unlock` closes the underlying handle and removes the file via `os.Remove`. The order is close-then-remove, which aligns with POSIX semantics (close invalidates the handle). No coordination exists for concurrent callers holding locks; if another process expects to observe a successful write before removal, there is no synchronization primitive governing that path.

---

## Path Resolution: `SafeResolve`

Canonicalizes `repoRoot` and `inputPath` into absolute form via `filepath.Abs`, then computes the relative offset from root. If the relative prefix begins with `..`, returns `"security violation: path traversal detected: %q"`. Otherwise returns the cleaned absolute path. Wraps any `filepath.Abs` error with context `"failed to get absolute root path: %w"`.

---

## Lock Acquisition & Release: `AcquireLock` / `SimpleLock.Unlock()`

Opens `.code-reducer.lock` in repoRoot using O_EXCL semantics; on success, writes the current process PID via `fmt.Sprintf("%d\n", os.Getpid())`. Atomic creation via O_EXCL ensures that if an existing lock is detected at acquire time, surfaces a descriptive error suggesting manual cleanup of a stale lockfile rather than silently failing.

**Unlock**: Closes the open handle (if non-nil) and deletes the file from disk. Returns last non-nil error between close and remove — priority given to whichever operation fails; if close already failed, remove errors are not re-surfaced unless close succeeded first.

---

## Git Hygiene: `EnsureGitignoreHasLockfile`

Reads `.gitignore` via `os.ReadFile`, tolerating absence. Scans every line for one matching the lock filename. If found, returns success without modification. Otherwise appends a dedicated comment line and the lock filename in append-create mode (`O_APPEND|O_CREATE|O_WRONLY`), preserving pre-existing contents.

---

## Concurrency Model

No Go concurrency primitives (mutexes, channels) exist within this package. Cross-process coordination relies exclusively on OS file semantics via O_EXCL creation. No in-memory mutable state protected by any mutex or channel exists. Each `AcquireLock` call allocates a new `SimpleLock`; fields are not shared across goroutines or process invocations.

---

## Side Effects Summary

| Function | I/O Target | Action |
|----------|-----------|--------|
| **SafeResolve** | Local filesystem (repo root) | Reads `filepath.Abs` to resolve absolute path. No writes. |
| **AcquireLock** | `.code-reducer.lock` in repoRoot | Creates file with O_EXCL, writes PID (`fmt.Sprintf("%d\n", os.Getpid())`). Atomic creation via O_EXCL. |
| **SimpleLock.Unlock()** | `.code-reducer.lock` in repoRoot | Calls `os.Remove(l.lockPath)` — deletes the lockfile on disk. Does NOT close `f` (caller's responsibility). |
| **EnsureGitignoreHasLockfile** | `.gitignore` in repoRoot | Reads via `os.ReadFile`. If file missing (`IsNotExist`), skips read path. Appends `# Code-Reducer Lockfile\n.code-reducer.lock\n` if lockfile not already listed. Uses O_APPEND\|O_CREATE\|O_WRONLY. Writes with `f.WriteString`. |

---

## Error Handling Patterns

| Function | Strategy |
|----------|----------|
| **SafeResolve** | Wraps `filepath.Abs` error with context `"failed to get absolute root path: %w"`. Separately detects traversal via `strings.HasPrefix(rel, "..")` and returns a sentinel-style message (`"security violation: path traversal detected: %q"`). |
| **AcquireLock** | On O_EXCL failure, checks `os.IsExist(err)` first. If true → descriptive error telling user the lock is held by another process, suggesting manual deletion of stale locks. Otherwise wraps raw OS error with `%w`. |
| **SimpleLock.Unlock()** | Returns last non-nil error (close or remove). Priority: if close fails AND remove succeeds, returns close error. If close succeeds AND remove fails, returns remove error. Uses `&& err == nil` guard to avoid masking remove errors when close already failed — though this logic is inverted in practice (it returns the first non-nil, then overrides only if previous was nil). |
| **EnsureGitignoreHasLockfile** | On read: treats `IsNotExist` as success (silently skips). Wraps other read errors with context. On write: returns wrapped error if append fails. No panic paths — all OS errors are returned to caller. |

---

## Observations

- All external I/O is filesystem-only. No network, no DB, no stdin/stdout writes beyond `fmt.Sprintf`.
- Errors are never panicked; all surfaced as `(result, error)` tuples except `EnsureGitignoreHasLockfile` which returns only an error.
- `SimpleLock.Unlock()` does NOT close the file before removing — this may be intentional (close truncates), but if another caller holds the lock and expects to see a successful write completion before removal, there is no coordination.