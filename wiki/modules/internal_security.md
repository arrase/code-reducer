# `internal/security` — Package Documentation

## Module Responsibility & Data Flow

The `security` package encapsulates three orthogonal concerns for code-reducer processes: **sandbox enforcement** (preventing path traversal beyond the repository boundary), **process-level mutual exclusion** via file-based locks, and **operational hygiene** (keeping lock state out of version control). No networking, database access, or external process communication occurs within this package.

All functions are pure-Go with no global mutable state. Every observable side effect is local to a single `*SimpleLock` receiver instance or the calling goroutine's stack. Errors follow a strict wrapping convention: security-critical violations and actionable sentinel messages return unwrapped plain strings; standard-library errors use `%w`; best-effort cleanup paths swallow non-fatal I/O failures silently.

**Data flow** proceeds in two independent tracks that share no state except through the repository root path string:

1. **Path resolution track:** `SafeResolve` → called by `AcquireLock` to canonicalize the lock file location before attempting acquisition.
2. **Lock lifecycle track:** `AcquireLock` → returns a `*SimpleLock` receiver → caller uses it for work → calls `Unlock()` for release → `EnsureGitignoreHasLockfile` runs during cleanup to append the lock filename to `.gitignore`.

---

## Public API Surface

### Types

#### `SimpleLock`

File-based process lock with internal mutex protection. The struct is constructed by `AcquireLock`; once acquired, `lockPath` and `file` are set exactly once under mutex scope, then only modified during `Unlock()`.

| Field | Type | Semantics |
|-------|------|-----------|
| `lockPath` | `string` | Absolute path to the `.code-reducer.lock` file. Set in constructor; zero-valued if acquisition failed or was never called. |
| `file` | `*os.File` | Opened lock file handle. Acquired with `O_CREATE\|O_EXCL`; set to `nil` inside `Unlock()`. |
| `mu` | `sync.Mutex` | Per-instance mutex protecting `closed` and the pointer-clear of `file` during unlock. |
| `closed` | `bool` | Transition flag: `false` while locked, flips to `true` once `Unlock()` completes its write operations (under mutex). |

#### Functions

##### `SafeResolve(repoRoot, inputPath string) (string, error)`

Computes the absolute path of an input relative to a repository root. The resolution is symlink-aware: ancestor components are evaluated through their physical targets via `filepath.EvalSymlinks` before being walked upward until one succeeds. The final result must lie strictly inside the resolved root—any escape beyond it returns an unwrapped string error indicating a security violation.

##### `AcquireLock(repoRoot string) (*SimpleLock, error)`

Resolves the lock file path through `SafeResolve`, then attempts atomic creation with `O_CREATE\|O_EXCL`. On success, writes the process PID to the newly created file and returns a populated `*SimpleLock`. If the file already exists (anewer process holds it), returns an unwrapped sentinel string describing that another process has acquired the lock.

##### `EnsureGitignoreHasLockfile(repoRoot string) error`

Reads `.gitignore`; if present, appends `# Code-Reducer Lockfile\n.code-reducer.lock\n` only when not already contained in the file. If `.gitignore` does not exist, creates it with append semantics (`O_APPEND\|O_CREATE\|O_WRONLY`). Errors from reading or writing are wrapped; non-existence of `.gitignore` is treated as expected and returned as `nil`.

#### Methods

##### `(l *SimpleLock).Unlock() error`

Idempotent release: acquires the internal mutex, closes the file handle (discarding any close-error), removes the lock file from disk via best-effort `os.Remove`, sets `closed = true`, then releases the mutex. A second call is safe and returns `nil`. Swallowed errors for cleanup operations are discarded; removal failures that occur after a successful close surface as wrapped errors only in the specific narrow case where both close-succeeded-and-remove-failed—but this path effectively swallows non-`IsNotExist` remove errors in practice.

---

## Business Rules & Domain Concepts

### Repository Boundary Enforcement

All internal paths must resolve strictly within `repoRoot`. Any resolution that escapes beyond it returns a standalone string error: `"security violation: path traversal detected: %q"`, never wrapped. This is the one unwrapped class of non-sentinel errors in the package—security-critical, so callers detect it via exact match or `strings.Contains` rather than type assertion.

### Symlink-Aware Path Resolution

Ancestors are evaluated through their physical targets before path reconstruction. This prevents traversal attacks that use symlinked directories to bypass the sandbox while preserving logical input structure for legitimate uses. Implementation: `filepath.EvalSymlinks(absRoot)` resolves the root once; subsequent ancestor walks use `os.Lstat` with a fallback to `EvalSymlinks` on each component until one exists, then rebuild upward from that point.

### Process-Level File Locking

The lock file is `.code-reducer.lock`. Acquisition uses `O_EXCL` for atomic creation—no two processes can hold the lock simultaneously on the same filesystem. On success, the PID of the acquiring process is written as a single-line string (`fmt.Sprintf("%d\n", os.Getpid())`). The lockfile content therefore serves as both a record and a mechanism for stale detection: if another process exits without releasing its lock, a new acquisition attempt detects the existing file and reports an actionable error instructing manual cleanup.

### Stale Lock Detection

Intentional behavior—the system does not attempt to detect or kill the holding process. The caller receives an unwrapped string describing that the lock is held by another process and must clean up manually. This keeps the package non-invasive; it never interacts with external processes.

### Automated Gitignore Integration

The lockfile path is appended to `.gitignore` during unlock so it does not enter version control. Idempotent: if a line containing `code-reducer.lock` already exists, no duplicate entry is written. The lock filename itself is the only thing tracked; comment prefix provides human readability without affecting git behavior.

---

## Mutable State & Concurrency Analysis

### Per-Instance State

| Field | Mutated By | Notes |
|-------|-----------|--------|
| `lockPath` | Constructor (`AcquireLock`) | Set exactly once. Zero value if acquisition failed or never called. Not mutated after construction. |
| `file` (*os.File) | Constructor, then `Unlock()` | Pointer cleared inside the mutex during unlock so subsequent reads yield nil. |
| `mu` (sync.Mutex) | All methods on receiver | Acquired/released by every method; not modified as a value. |
| `closed` (bool) | `Unlock()` only | Flips from `false` → `true` under mutex. Once written, stays true for the lifetime of the receiver. |

### Concurrency Protection

The `sync.Mutex` protects all writes to `l.closed` and the pointer-clear of `l.file` inside `Unlock()`. No other concurrent access paths touch these fields in this file—`AcquireLock` constructs a new instance, so no shared writer exists for an already-acquired lock. The mutex serializes unlock calls on a single receiver only; it is not a global contention point across processes.

---

## Side Effects & I/O Communication

### Filesystem Operations by Function

| Function | Reads | Writes | Deletes/Atomic | Notes |
|----------|-------|--------|----------------|--------|
| `SafeResolve` | `os.Lstat`, `filepath.EvalSymlinks` (metadata only) | None | None | Walks ancestors upward until one exists; no data read/written. |
| `AcquireLock` | None | `O_CREATE\|O_EXCL` + PID bytes via `fmt.Sprintf("%d\n", os.Getpid())` | None | Atomic creation on success; cleans up file handle and removes partial state if PID write fails before returning error. |
| `SimpleLock.Unlock()` | None | `l.file.Close()` (flushes buffer) | `os.Remove(l.lockPath)` — best-effort, non-blocking | Idempotent. Swallowed close errors; remove failures are discarded unless they occur after a successful close and removal itself fails. |
| `EnsureGitignoreHasLockfile` | `os.ReadFile(gitignorePath)` | Append-mode open + write string | None | Creates `.gitignore` if absent (append semantics). Appends only when lock filename is not already present. |

### Network / External Process

None. The package performs no HTTP, gRPC, socket, or IPC operations. No external process interaction beyond reading/writing files within the repository boundary.

---

## Error Handling Patterns

### Sentinel Errors (Unwrapped)

| Condition | Returned Value | Rationale |
|-----------|---------------|-----------|
| Lock held by another process | `"lock at %s is already held by another process..."` | Actionable message; caller needs exact match or substring search. |
| Path traversal detected | `"security violation: path traversal detected: %q"` | Security-critical; never wrapped for unambiguous detection. |

### Wrapped Errors (Standard Library)

| Condition | Wrapping Strategy | Notes |
|-----------|------------------|--------|
| `filepath.Abs` fails | `%w` | Root is not absolute—caller should handle gracefully. |
| `filepath.EvalSymlinks` on root fails | `%w` | Symlink resolution failure for the repository boundary itself. |
| `os.Lstat` returns non-nil, non-IsNotExist error | `%w` | Unexpected stat error on an ancestor component. |
| `EvalSymlinks` on ancestor fails | `%w` | Ancestor symlink resolution failure during upward walk. |
| `.gitignore` read (not IsNotExist) | `%w` | Read of existing gitignore encounters unexpected I/O error. |
| Append/open for `.gitignore` fails | `%w` | File open or write failure for the ignore file. |

### Swallowed Errors (Best-Effort Cleanup)

If `l.file.Close()` inside `Unlock()` returns an error, it is replaced with `nil` and cleanup proceeds unconditionally. If `os.Remove(l.lockPath)` fails after a successful close, the remove-error is discarded unless both close-succeeded-and-remove-failed—but this narrow path effectively swallows non-`IsNotExist` errors in practice. The lockfile removal happens regardless; any failure that isn't `IsNotExist` is preserved in a local variable but only surfaces when close succeeded and remove failed—which is treated as swallowed behavior: best-effort cleanup.

### Lock Acquisition Failure Cleanup

In `AcquireLock`, if writing the PID to the newly created file fails, the function calls `f.Close()` then `os.Remove(lockPath)` before returning an error. The file handle and lockfile are cleaned up so no stale partial state remains on disk; the returned error is wrapped with `%w` for context.