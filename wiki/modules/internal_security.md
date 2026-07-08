# Security Module (`security.go`)

## Responsibility and Data Flow

The security module encapsulates filesystem integrity checks, concurrency control primitives, and version-control hygiene for repository-level lock management. Its primary responsibility is to enforce safe path resolution before any I/O operation occurs and to serialize access to shared resources via `flock`-based locking.

**Data Flow:**
1.  **Input Sanitization:** External paths are passed through `SafeResolve`, which resolves symlinks relative to the repository root and validates that the resolved target does not escape the intended scope, returning a canonical absolute path or an error state if traversal is detected.
2.  **Concurrency Control:** Concurrent processes request access via `AcquireLock`. The function accepts an exclusive or shared intent flag. If exclusive, it atomically acquires the lock and persists the current process identifier (PID) into `.code-reducer.lock` to enable traceability of lock ownership. Shared locks are acquired without PID persistence.
3.  **Persistence Hygiene:** To prevent stale lockfiles from appearing in diffs or causing noise in repositories, `EnsureGitignoreHasLockfile` ensures the `.gitignore` file exists and contains an entry for `.code-reducer.lock`. If the ignore file is missing, it is created; if it exists but lacks the entry, the entry is appended.

---

## Path Traversal Sanitization (`SafeResolve`)

### Function Signature
```go
func SafeResolve(inputPath string) (string, error)
```

### Behavior
`SafeResolve` performs a security-critical path resolution operation designed to mitigate path traversal attacks. It evaluates the input `inputPath` by resolving all symbolic links relative to the repository root directory (`rootDir`). The function compares the canonical absolute path of the resolved target against the parent directory boundary of the repository root.

### Failure Modes
*   **Traversal Detection:** If the resolved symlink points outside the repository root, the function returns an error and does not return a path.
*   **Symlink Evaluation:** The OS-level `syscall.Lstat`/`Readlink` cycle is utilized to dereference symlinks transparently before comparison.

---

## File Locking (`AcquireLock`)

### Function Signature
```go
func AcquireLock(lockfile string, exclusive bool) error
```

### Behavior
`AcquireLock` implements file-based locking using `syscall.Flock` (POSIX advisory lock). It manages the lifecycle of `.code-reducer.lock`.

*   **Exclusive Mode (`exclusive == true`):** The caller attempts to acquire an exclusive lock. If successful, it writes the current process PID into the lockfile, creating a record of which process currently holds the resource. This allows for manual inspection or debugging of lock contention.
*   **Shared Mode (`exclusive == false`):** The caller acquires a shared lock without persisting state to disk.

### Data Flow
1.  Open/Create `.code-reducer.lock`.
2.  Call `syscall.Flock(fd, LOCK_EX | LOCK_NB)` for exclusive or `LOCK_SH | LOCK_NB` for shared (non-blocking).
3.  On success (exclusive): Write PID integer to file descriptor using a safe writer to prevent partial writes.

---

## VCS Integration (`EnsureGitignoreHasLockfile`)

### Function Signature
```go
func EnsureGitignoreHasLockfile() error
```

### Behavior
`EnsureGitignoreHasLockfile` ensures that the lockfile is excluded from version control tracking. It operates on `.gitignore`.

*   **Missing Ignore File:** If `.gitignore` does not exist at the repository root, it is created (with appropriate permissions `0644`).
*   **Existing Ignore File without Entry:** The function parses or searches for an existing entry corresponding to `.code-reducer.lock`. If absent, it appends a new line containing the lockfile path.

### Data Flow
1.  Check existence of `.gitignore` via `os.Stat`.
2.  Read file content if present; write empty string (or default template) otherwise.
3.  Search for substring `.code-reducer.lock`.
4.  If not found, append `\n.code-reducer.lock\n` to the file and return success.