# internal/security Module

## Responsibility and Data Flow

The `internal/security` package provides two isolated concerns for repository-level operations: (1) safe resolution of user-supplied filesystem paths to prevent traversal escapes, and (2) file-based mutual exclusion ensuring only one instance of code-reducer holds a lock at any time. A third utility integrates the lockfile into version control so stale state is ignored by `git`.

Data flow begins with callers invoking `SafeResolve` or `AcquireLock`, both of which return errors to downstream consumers for branching logic. Sentinel errors from `errors.go` are returned via standard Go `error` returns and must be detected using `errors.Is()` or `errors.As()`. No wrapping occurs; propagation is contract-level, not exception-style.

---

## Error Contract Markers (`errors.go`)

### Sentinel Definitions

| Variable | Condition | Mechanism |
|---|---|---|
| `ErrPathTraversal` — `"security violation: path traversal detected"` | Returned when a resolved path falls outside the repository root boundary | Standard Go error return value. Not wrapped; no panic. Detected via `errors.Is()` / `errors.As()`. |
| `ErrLockHeld` — `"lock is already held by another process"` | Returned when another code-reducer instance holds the file lock | Standard Go error return value. Not wrapped; no panic. Detected via `errors.Is()` / `errors.As()`. |

Both errors are raw `*errors.errorString` values constructed using the standard library's `errors.New`. They carry no algorithmic logic and serve solely as contract markers for callers to branch on. No exception-style wrapping occurs in this file; any processing logic resides elsewhere in the module.

---

## Path Sanitization (`security.go`)

### Public API: `SafeResolve`

**Signature:** `func SafeResolve(repoRoot string, inputPath string) (string, error)`

**Responsibility:** Cleans an input path and ensures it lies strictly inside the repository root. Resolves symlinks on existing ancestor parts to block symlink-based traversal.

**Algorithm Steps:**
1. Accept a `repoRoot` absolute reference and a relative or absolute `inputPath`.
2. Resolve symlinks on existing ancestor directory components so that symlink-based traversal is blocked.
3. Walk up from the target until hitting an existing physical ancestor (skipping non-existent intermediate components).
4. Reconstruct the full resolved path from that ancestor plus the skipped suffixes.
5. Verify the final resolved path lies strictly inside the repository root; reject if it does not.

**Error Propagation:** Errors from `filepath.Abs`, `EvalSymlinks(absRoot)`, and `EvalSymlinks(current)` are wrapped with context and returned directly. The `os.Lstat` loop terminates on first success or when reaching the filesystem root (`parent == current`). Non-not-exist errors during stat are wrapped and returned immediately. If `filepath.Rel` returns an error OR the relative path starts with `".."`, a wrapped `ErrPathTraversal` is returned.

---

## Process Locking (`security.go`)

### Public API: `AcquireLock`

**Signature:** `func AcquireLock(repoRoot string) (*SimpleLock, error)`

**Responsibility:** Acquires an exclusive lock file (`.code-reducer.lock`) using O_EXCL for atomicity.

**Algorithm Steps:**
1. Call `SafeResolve` to sanitize the path; errors propagate to caller unchanged.
2. Open a new file with O_EXCL; failure indicates another process holds the lock or a stale file exists.
3. On open failure: check `os.IsExist(err)`. If the lock file already exists, return a specific message instructing manual cleanup; otherwise wrap with path context and original error.
4. Write the current PID to the lock file for identification/verification.
5. On write failure after successful open: clean up by calling `f.Close()` then `os.Remove`, then return the write error.

**Return Type:** `*SimpleLock` — a struct holding per-instance state (`lockPath`, `file *os.File`, `mu sync.Mutex`, `closed bool`). No public methods other than `Unlock()`.

---

### Struct Method: `(*SimpleLock) Unlock`

**Signature:** `func (l *SimpleLock) Unlock() error`

**Responsibility:** Releases the lock by closing the file and removing it. Idempotent and thread-safe.

**Algorithm Steps:**
1. Capture close error in an `err` variable.
2. Attempt `os.Remove`. Only replace `err` if (a) the previous operation succeeded AND (b) remove fails with a non-not-exist error.
3. Result: if close succeeds but remove fails, that error is returned; if close fails but remove also fails, only the close error is surfaced.

**Swallowed Error:** The deferred block at the end of `EnsureGitignoreHasLockfile` discards both `tmpFile.Close()` and `os.Remove(tmpName)` return values. If the temp file cannot be closed or removed (e.g., permission denied, interrupted signal), that error information is lost silently. This is a minor leak — not critical for correctness since the rename has already succeeded at that point, but it's worth noting.

---

## Gitignore Integration (`security.go`)

### Public API: `EnsureGitignoreHasLockfile`

**Signature:** `func EnsureGitignoreHasLockfile(repoRoot string) error`

**Responsibility:** Ensures that `.code-reducer.lock` is present in `.gitignore`.

**External I/O Operations:**
- `filepath.Abs` / `EvalSymlinks`: Resolve absolute paths and evaluate symlinks on the repository root/ancestor (shared with `SafeResolve`)
- `os.Lstat` (loop): Walk up directory hierarchy to find closest physically-existing ancestor during path traversal validation
- `filepath.Rel`: Verify resolved target lies strictly inside resolved root
- `os.OpenFile(... O_EXCL)`: Atomic create of lock file; failure indicates another process holds the lock or a stale file exists
- `f.Write([]byte{...})`: Write current PID to lock file for identification/verification
- `os.ReadFile`: Read existing `.gitignore` contents before appending entry
- `os.CreateTemp` + `tmpFile.Write`: Atomic write pattern — build new content in temp file, then rename over original
- `tmpFile.Sync`: Force flush to disk before close/rename
- `os.Chmod`: Set permissions on temp file matching default (0644)
- `os.Rename`: Atomically replace `.gitignore` with updated version

**Error Propagation:** SafeResolve errors propagate directly. Non-not-exist read failures are wrapped with `"error reading .gitignore"` context. All temp file operations (`CreateTemp`, `Write`, `Sync`, `Close`, `Chmod`) return wrapped errors to caller on failure. Final `os.Rename` error is returned if it fails.

---

## Module-Level Observations

- **No network/database interactions:** Confirmed by full code inspection. All I/O targets are local paths under `repoRoot`.
- **Mutable state:** None at the package level. The file contains only `const` values and function-local variables; no package-level or class-level properties are modified.
- **Shared/instance state:** `SimpleLock` holds per-instance fields (`lockPath`, `file *os.File`, `mu sync.Mutex`, `closed bool`). `Unlock()` modifies `l.closed = true` under its own internal mutex. This is instance-scoped state, not shared global state.
- **Package-level constant:** `defaultFilePerm = 0644` — used by temp file permission setting in the gitignore integration path.