# `internal/security` — Technical Documentation

## Module Responsibility

The `internal/security` package provides **process-level mutual exclusion** and **path sandboxing** for the code-reducer toolchain. It enforces that all file operations occur within a single repository root using an atomic lockfile, prevents directory-traversal attacks against user-supplied paths, and ensures the lock sentinel is excluded from version control tracking via `.gitignore` injection.

All functions operate exclusively against the local filesystem. No network I/O or database interactions exist in this package. Every operation returns errors to callers; no panics occur anywhere in the public surface area.

---

## Constants

| Identifier | Type | Value |
|-----------|------|-------|
| `LockFileName` | `string` | `.code-reducer.lock` |

The sentinel filename is a package-level constant, immutable after compilation. It is referenced directly by `AcquireLock()` and consumed by `EnsureGitignoreHasLockfile()` during housekeeping operations.

---

## Types

### `SimpleLock`

```go
type SimpleLock struct {
    lockPath string // set once in AcquireLock; never reassigned
    file     *os.File // OS handle opened atomically via O_EXCL
}
```

**Invariants:**
- `lockPath` is assigned during construction and remains unchanged for the lifetime of the instance. No concurrent mutation of this field exists.
- `file` holds an open file descriptor. It is closed exclusively by `(l *SimpleLock).Unlock()`. There are no other writers to this field.

---

## Public API Surface

### Path Traversal Prevention — `SafeResolve`

```go
func SafeResolve(repoRoot, inputPath string) (string, error)
```

**Contract:** Resolves an arbitrary user-supplied path against the repository root and rejects any result that escapes outside it.

**Flow:**
1. Calls `filepath.Abs(repoRoot)` to obtain the absolute anchor.
2. Calls `filepath.Abs(inputPath)` to obtain the absolute candidate.
3. Computes the relative position of `inputPath` with respect to `repoRoot` via `filepath.Rel`.
4. If the resulting string begins with `".."`, the function returns an error wrapping the original failure. The wrapped error is produced by concatenating a descriptive prefix with `%w` applied to the underlying `filepath.Rel` or `filepath.Abs` error.

**Semantics:** A path that resolves to a sibling directory, parent directory, or any ancestor of `repoRoot` is rejected. This enforces sandboxing regardless of how the caller passes `inputPath`.

---

### Lock Acquisition — `AcquireLock`

```go
func AcquireLock(repoRoot string) (*SimpleLock, error)
```

**Contract:** Atomically opens `.code-reducer.lock` in the repository root. If another process already holds the file descriptor open for writing, this call fails immediately and returns an error.

**Flow:**
1. Calls `SafeResolve(repoRoot, LockFileName)` to obtain the absolute lockfile path.
2. Opens the file with flags `os.O_WRONLY | os.O_CREATE | os.O_EXCL` and mode `0644`. The `O_EXCL` flag guarantees atomic mutual exclusion at the OS/kernel level: if a process holds an open writeable descriptor, this open returns an error without creating or partially writing the file.
3. If the open fails, checks `os.IsExist(err)` on the returned error object. When true, emits a specific message indicating a stale lockfile is present; otherwise wraps the original error with `%w`.
4. Returns the constructed `*SimpleLock` and any error encountered during construction or file opening.

**Semantics:** The PID of the locking process is written to the lockfile on disk for diagnostic inspection after successful acquisition. The lock remains held until the associated `SimpleLock` instance calls `(l *SimpleLock).Unlock()`.

---

### Lock Release — `(l *SimpleLock).Unlock()`

```go
func (l *SimpleLock) Unlock() error
```

**Contract:** Closes and removes the sentinel file, releasing the lock.

**Flow:**
1. Calls `l.file.Close()` to release the OS file descriptor.
2. Removes the underlying path via `os.Remove`.
3. If both operations fail, returns the non-nil error from `Close`; if only one fails, returns that single error. This precedence rule is explicit: a close failure masks a removal failure.

**Semantics:** Any I/O errors encountered during this method are propagated to the caller; no panics occur. The lockfile is fully cleaned up on successful completion.

---

### Gitignore Enforcement — `EnsureGitignoreHasLockfile`

```go
func EnsureGitignoreHasLockfile(repoRoot string) error
```

**Contract:** Ensures `.gitignore` contains an entry for the lockfile so it does not pollute version control tracking.

**Flow:**
1. Derives the path to `.gitignore` within `repoRoot`.
2. If the file does not exist on disk (`os.IsNotExist(err)` is true), treats this as success and skips the read step entirely — no error returned for missing files, only for genuine I/O failures.
3. Otherwise reads the existing content via `os.ReadFile`, then opens the file with flags `os.O_APPEND | os.O_CREATE | os.O_WRONLY` and mode `0644`.
4. Appends a lockfile entry to the file handle.

**Semantics:** The operation is idempotent on repeated calls — duplicate entries are not detected or cleaned up, but they do not cause functional issues downstream because `.gitignore` treats duplicates as no-ops.

---

## Concurrency Model

The package relies exclusively on **OS-level atomicity**. No in-process synchronization primitives (`sync.Mutex`, `sync.RWMutex`, channels) appear anywhere in this file. The mutual exclusion guarantee is provided by the kernel's handling of `O_EXCL` during `os.OpenFile`. If a process holds an open writeable descriptor to `.code-reducer.lock`, any other process attempting acquisition will fail atomically, with no window for race conditions between check and act.

There are **no package-level global variables**. Mutable state is contained within the `SimpleLock` struct fields: both `lockPath` and `file` are set once during construction and never modified after that point. The only constant referenced externally is `LockFileName`, which is immutable post-compilation.

---

## Error Handling & Side Effects Summary

| Category | Result |
|----------|--------|
| Mutable state | None — fields are write-once, no shared mutable state to protect |
| Concurrency protection | OS-level atomicity via `O_EXCL`; no shared mutable state exists in this package |
| Network I/O | None |
| Database operations | None |
| Panics / sentinels | None |

All functions return typed errors via their return values. Errors are wrapped with descriptive messages using `%w` for wrapping original `filepath.Abs`, `filepath.Rel`, and OS-level errors. The `Unlock()` method propagates close or remove failures; when both fail, the close error is returned in precedence over removal.