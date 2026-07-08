# `security.go` — Path Isolation & Process Locking

## Module Responsibility & Data Flow

This module enforces filesystem isolation and concurrent access control for the code reduction process. Its primary responsibility is twofold: bounding all user-supplied or derived paths strictly within the repository root to prevent path traversal, and managing exclusive process locks via a persistent file-based mechanism to serialize stateful operations (e.g., parallel reductions) and ensure lock state hygiene relative to version control tracking.

**Data Flow:**
1.  **Input Sanitization:** User input is passed through `SafeResolve`, which canonicalizes the path and verifies it resolves strictly within the repository root (`repo.Root`). Any deviation triggers an immediate error return, preventing directory traversal.
2.  **Lock Lifecycle:** Concurrent processes attempt to acquire a lock via `AcquireLock`. This writes the current process PID into `.code-reducer.lock` atomically. If acquisition fails (file exists or I/O error), the caller is returned with an error. Subsequent operations require holding this lock (`*SimpleLock`). Upon completion, `Unlock()` closes the underlying file handle and removes the lockfile from disk to prevent stale state accumulation.
3.  **Gitignore Enforcement:** To ensure the lockfile does not enter version control history or clutter diffs, `EnsureGitignoreHasLockfile` verifies `.gitignore` exists, appends the lockfile path if missing, and ensures directory structure integrity for the ignore file itself.

---

## Path Sanitization: `SafeResolve`

**Responsibility:** Cleans an input string representing a filesystem path and validates it resolves strictly inside the given repository root (`repo.Root`). Returns an error immediately upon detecting any traversal attempt (e.g., `../../`, symlink escapes, or absolute paths outside the root).

```go
// SafeResolve cleans input and verifies resolution within repo root.
func SafeResolve(input string) (string, error) { ... }
```

**Behavior:**
*   Resolves the canonical absolute path of `input`.
*   Compares against the canonical absolute path of `repo.Root`.
*   If `resolved != rootPrefix`, returns a traversal error.
*   Returns the cleaned absolute path on success.

---

## Process Locking: `SimpleLock`, `AcquireLock`, `Unlock`

**Responsibility:** Provides a file-based process lock mechanism using `.code-reducer.lock`. Stores the PID of the holding process in the lockfile to identify the owner for potential manual cleanup or debugging. Ensures mutual exclusion during parallel code reduction runs.

### Lock Acquisition (`AcquireLock`)
*   Attempts to create an exclusive lockfile containing the current process PID.
*   Returns an error if another process already holds the lock (non-zero file size on creation) or if file system operations fail.

```go
// AcquireLock attempts to create an exclusive lockfile with current PID.
func AcquireLock() (*SimpleLock, error) { ... }
```

### Lock Representation (`*SimpleLock`)
The `SimpleLock` struct wraps the underlying file handle and associated state for a held process lock.

```go
type SimpleLock struct { ... }
```

### Lock Release (`Unlock`)
*   Releases the lock by closing its underlying file handle to flush buffered I/O.
*   Removes the `.code-reducer.lock` file from disk via `os.Remove`.
*   Returns an error if removal fails (e.g., permission denied or OS-level constraint).

```go
// Unlock releases the lock by closing the file handle and removing the lockfile.
func (l *SimpleLock) Unlock() error { ... }
```

---

## Configuration Hygiene: `EnsureGitignoreHasLockfile`

**Responsibility:** Ensures `.code-reducer.lock` is listed in `.gitignore`, preventing accidental version control tracking of the runtime lock state. Creates the ignore file or appends the entry as needed, handling missing parent directories for the gitignore path itself.

```go
// EnsureGitignoreHasLockfile ensures .code-reducer.lock is listed in .gitignore.
func EnsureGitignoreHasLockfile() error { ... }
```

**Behavior:**
1.  Checks if `.gitignore` exists at the repository root.
2.  If missing, creates an empty `.gitignore`.
3.  Reads existing content and appends `*.code-reducer.lock` (or exact path) only if not present to avoid duplicate entries.
4.  Returns error on I/O failure during creation or append operations.