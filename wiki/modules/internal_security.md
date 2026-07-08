# Security & Isolation Module (`security.go`)

## Responsibility

The `security` package enforces sandbox isolation and concurrency safety for repository operations. It validates external input against the repository boundary, serializes resource access via POSIX file locking, and ensures lockfile metadata remains excluded from version control history. All functions operate relative to a canonical `repoRoot`, establishing a consistent security perimeter.

## Path Traversal Protection (`SafeResolve`)

**Signature:** `func SafeResolve(repoRoot, inputPath string) (string, error)`

`SafeResolve` validates that an external `inputPath` resolves strictly within the repository boundary defined by `repoRoot`. It rejects absolute paths, relative traversals via `..`, and symlink targets pointing outside the repo. It returns a normalized absolute path on success or a sentinel error on failure. This function acts as the mandatory entry point for all filesystem I/O operations to prevent escape vectors.

## Concurrency Control (`AcquireLock`)

**Signature:** `func AcquireLock(repoRoot string, exclusive bool) (*flock.Flock, error)`

`AcquireLock` acquires a POSIX advisory file lock using `syscall.Flock`. It accepts an `exclusive bool` flag to select between exclusive (write-serialize) and shared (read-multiple-writer) modes. The function detects and rejects TOCTOU symlink races on the lockfile itself before acquiring, ensuring the target inode has not been swapped post-resolution. It returns a handle of type `*flock.Flock`, which must be released by the caller to prevent resource leaks.

## Version Control Hygiene (`EnsureGitignoreHasLockfile`) & Constants

**Signature:** `func EnsureGitignoreHasLockfile(repoRoot string) error`
**Constant:** `const LockFileName = ".code-reducer.lock"`

`LockFileName` defines the canonical path for the process-level lockfile, used consistently across all functions in this module.

`EnsureGitignoreHasLockfile` ensures `.gitignore` contains an entry for `LockFileName`. If `.gitignore` is missing, it creates one with the lockfile entry; if existing, it appends the entry to prevent duplicate tracking or corruption of git state. This prevents accidental modification of the lockfile via `git` operations while preserving repository integrity.