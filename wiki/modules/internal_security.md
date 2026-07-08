# Security Module (`security.go`)

## Responsibility

The `security` package provides repository-integrity primitives that enforce **path confinement**, **concurrent access control**, and **state isolation** for the repository root. These guards are invoked before any state-modifying or resource-accessing operation to prevent privilege escalation through path traversal, TOCTOU symlink hijacking, and git-tracking of shared resources.

---

## Path Confinement (`SafeResolve`)

`SafeResolve` validates that a given input path resolves strictly within the repository root directory. It returns an error if **path traversal** (e.g., `..` components) or **external symlinks** are detected at any stage of resolution. This function is invoked before any filesystem operation that targets resources under the repository root, ensuring no escape from the confinement boundary.

| Parameter | Type | Description |
|-----------|------|-------------|
| input path | `string` | Absolute or relative path to validate against the repository root |

---

## Lock Acquisition (`AcquireLock`)

`AcquireLock` acquires either an **exclusive** or **shared** file-lock on a specified lockfile path, with TOCTOU-safe checks to prevent symlink hijacking of the lock target. The lock mechanism uses `flock(2)` semantics for mutual exclusion across processes.

| Parameter | Type | Description |
|-----------|------|-------------|
| lockfile path | `string` | Path to the lock file on which to acquire the lock |
| lock mode | (exclusive/shared) | Determines whether other processes may concurrently hold a lock of the same type |

**Data flow:** The caller determines whether shared or exclusive access is required; `AcquireLock` then opens the specified path, performs TOCTOU-safe validation, and acquires the requested lock. If the operation fails, the function returns an error without leaving any partial state.

---

## State Isolation (`EnsureGitignoreHasLockfile`)

`EnsureGitignoreHasLockfile` ensures that the `.gitignore` file exists in the repository root and contains an entry for the lock file so it is not tracked by git. This prevents version-control systems from tracking shared or transient state, which would otherwise introduce non-deterministic behavior across clones and forks.

| Parameter | Type | Description |
|-----------|------|-------------|
| (implicit) repository root | `string` | Root directory of the repository; `.gitignore` is expected at this path |

---

## Module-Level Constant

### `LockFileName` (`string`)

Stores the canonical name of the lock file used by flock-based locking in this package. All internal callers reference this constant when constructing paths to shared or exclusive lock targets, ensuring consistent naming across the repository's lifecycle.