# security.go

## Module Overview

The `security` module enforces repository isolation, process-level concurrency control, and VCS hygiene for the code-reducer runtime environment. It manages three critical subsystems: path resolution integrity, file-based locking for concurrent analyzer invocations, and lockfile exclusion from Git tracking. Data flow proceeds as follows: user-supplied paths are sanitized via **path safety** primitives before any filesystem operation; concurrent access is governed by a `flock.Flock` handle acquired with PID registration to prevent overlapping reduction runs; finally, repository state is preserved by ensuring the lockfile remains untracked in `.gitignore`.

## Constants

### LockFileName

```go
const LockFileName = ".code-reducer.lock"
```

Defines the absolute path for the process coordination file stored at the repository root. Serves as the anchor point for both flock acquisition and git exclusion logic.

## Path Safety

### SafeResolve

**Signature:** `SafeResolve(repoRoot, inputPath string) (string, error)`

Validates and sanitizes user-supplied paths to ensure they resolve strictly within the repository boundary. Prevents symlink attacks and path traversal attempts by resolving relative components against `repoRoot` and rejecting any target that escapes the root directory or references dangling symlinks. Returns the canonical absolute path within the repo on success, or an error if resolution fails.

## Concurrency Control

### AcquireLock

**Signature:** `AcquireLock(repoRoot string, exclusive bool) (*flock.Flock, error)`

Acquires a file-based flock on `.code-reducer.lock` to serialize access across concurrent analyzer processes. When `exclusive` is true, obtains an exclusive (write) lock; otherwise acquires a shared (read) lock. Records the current process PID within the lockfile metadata to identify active owners for diagnostic purposes. Returns a pointer to the acquired `*flock.Flock` handle for subsequent release operations and errors if locking fails or the file is unreadable.

## Repository Integrity

### EnsureGitignoreHasLockfile

**Signature:** `EnsureGitignoreHasLockfile(repoRoot string) error`

Guarantees that the repository's `.gitignore` file exists at `repoRoot` and contains an entry for the lockfile path defined by `LockFileName`. If the file is absent, it is initialized; if present but missing the specific exclusion pattern, the entry is appended. Ensures the coordination file remains outside Git tracking history to prevent accidental inclusion in repositories or merge conflicts during VCS operations.