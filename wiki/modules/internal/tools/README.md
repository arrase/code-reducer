# Internal Tools Module

## Module Responsibility

This module provides safe filesystem operations and repository verification utilities for source code management workflows. It operates on a repository root (`repoRoot`) and resolves virtual paths through a security layer to prevent path traversal attacks. All operations are disk I/O only—no network, database, or external API calls.

**Data Flow:**

```
caller → virtualPath → security.SafeResolve → filesystem path → os.* operations
```

All public functions accept a `repoRoot` parameter and resolve virtual paths before accessing the filesystem. Errors are propagated as return values; no panics occur.

---

## File Tools (`file_tools.go`)

### Safe File Access

**`FileExistsSafely(repoRoot string, virtualPath string) bool`**

Checks whether a file exists at the resolved filesystem path. Swallows `os.Stat` errors and returns `false` on any failure.

**`ReadFileSafely(repoRoot string, virtualPath string) ([]byte, error)`**

Reads file contents from the resolved filesystem path. Returns the file data and an error. Swallows `os.Open` errors.

**`RemoveFileSafely(repoRoot string, virtualPath string) error`**

Removes a file at the resolved filesystem path. Ignores `os.IsNotExist` errors (file may already be absent). Propagates all other errors.

### Atomic File Writes

**`WriteFileAtomic(targetPath string, data []byte, perm os.FileMode) error`**

Writes data to `targetPath` atomically: writes to a temporary file first, then renames to the target path. Wraps all sub-errors with `fmt.Errorf("failed to %s: %w", ...)`.

**`WriteFileSafely(repoRoot string, virtualPath string, content []byte) error`**

Writes content to the resolved filesystem path. Uses atomic write semantics.

### Gitignore Integration

**`EnsureGitignoreHasLockfile(repoRoot string) error`**

Ensures `.code-reducer.lock` is included in `.gitignore` to prevent it from being committed.

**`LoadGitignore(repoRoot string) ([]string, error)`**

Loads `.gitignore` patterns from the repository root. Returns the parsed patterns and an error.

**`ShouldIgnoreFile(relPath string, gitIgnore *ignore.GitIgnore) bool`**

Determines whether a relative path matches any loaded gitignore patterns. Uses `ignore.CompileIgnoreLines` for pattern parsing (in-memory, no external I/O).

### Global State

| Variable | Type | Mutability |
|---|---|---|
| `skippedFileNames` | `map[string]bool` | Immutable after init |
| `skippedExtensions` | `map[string]bool` | Immutable after init |
| `skippedTestSuffixes` | `[]string` | Immutable after init |

These maps are initialized once and never modified. They define the multi-layer file filtering rules applied during file discovery.

---

## Git Tools (`git_tools.go`)

### Repository Verification

**`VerifyGitRepo(repoRoot string) error`**

Verifies whether `repoRoot` resides inside a git repository by executing `git rev-parse --is-inside-work-tree` in the specified directory.

**Process Execution Details:**

- Spawns `git` as a child process via `exec.Command("git", "--no-pager", "rev-parse", "--is-inside-work-tree")`
- Redirects child process stderr to a `bytes.Buffer` for capture
- Sets child process working directory to `repoRoot`

**Error Handling:**

- If `cmd.Run()` returns an error, the function wraps it with `fmt.Errorf` and returns the wrapped error (including captured stderr content)
- If `cmd.Run()` returns nil, returns nil (success)
- No panics or swallowed errors