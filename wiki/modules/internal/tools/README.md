# `internal/tools` — Repository-Aware File I/O and Git Abstraction

## Module Responsibility

This module provides two subsystems: a repository-aware file I/O and discovery layer, and a Git command abstraction. Both operate on a `repoRoot` boundary to prevent path traversal outside the codebase. No mutable state exists in either subsystem; all operations are stateless per-call.

---

## File Tools (`file_tools.go`)

### Public API

| Function | Signature |
|----------|-----------|
| ReadFileSafely | `func ReadFileSafely(repoRoot, virtualPath string) ([]byte, error)` |
| WriteFileSafely | `func WriteFileSafely(repoRoot, virtualPath string, content []byte) error` |
| LoadGitignore | `func LoadGitignore(repoRoot string) ([]string, error)` |
| ShouldIgnoreFile | `func ShouldIgnoreFile(relPath string, gitIgnore *ignore.GitIgnore) bool` |
| DiscoverCodeFiles | `func DiscoverCodeFiles(repoRoot string, ignores []string) ([]string, error)` |

### Data Flow

1. **Safe Path Resolution**: All file operations resolve user-supplied virtual paths against `repoRoot` via a security layer that prevents escaping the repository boundary. Errors from this step propagate directly to the caller without wrapping.
2. **TOCTOU-Safe Writes**: `WriteFileSafely` writes to an in-directory temporary location, then atomically renames into place. Directories are created as needed with default permissions before write begins. Deferred cleanup (`tmpFile.Close`, `os.Remove`) discards errors silently — no return value reflects cleanup failures.
3. **Gitignore Parsing**: Reads `.gitignore` from `repoRoot`. Non-empty, non-comment lines are collected as active patterns. A missing file returns `(nil, nil)` intentionally — not an I/O failure signal. Scanner errors are returned as-is via `scanner.Err()`.
4. **Ignore Decision Logic**: A path is ignored if any component starts with a dot (hidden files) or ends with `.egg-info`, OR if it matches user-supplied gitignore patterns.
5. **Recursive Code Discovery**: Walks the repository root recursively. Directories starting with a dot or ending with `.egg-info` are skipped entirely. Files passing ignore checks have their relative paths normalized to forward slash and collected as results.

### Error Handling

- **Preserved via `%w`**: All disk I/O in `ReadFileSafely`, `WriteFileSafely`, and `LoadGitignore` uses wrapping that preserves the original error chain for unwrapping.
- **Swallowed**: The deferred cleanup block in `WriteFileSafely` discards Close/Remove errors entirely. The walk callback in `DiscoverCodeFiles` logs to stderr and returns `nil`, losing original error context.
- **Direct propagation**: Errors from the security layer pass through unchanged — caller sees exactly what happened.

---

## Git Tools (`git_tools.go`)

### Public API

| Function | Signature |
|----------|-----------|
| RunGit | `func RunGit(repoRoot string, args ...string) (string, error)` |
| VerifyGitRepo | `func VerifyGitRepo(repoRoot string) error` |

### Data Flow

1. **Initialize Command**: Constructs a Git command invocation in the specified root directory, appending `--no-pager` to suppress pager output.
2. **Execute and Capture**: Runs the constructed process while capturing stdout into `bytes.Buffer` and stderr separately buffered.
3. **Handle Success or Failure**: On success, trims whitespace from captured stdout and returns it as a string result. On failure, trims stderr and returns a formatted error containing the failure details.
4. **Validate Repository Status**: Invokes `git rev-parse --is-inside-work-tree` specifically to check if the directory is inside a work tree. If this command fails, classifies the location as not being a Git repository and returns that specific classification.

### External Systems Interacted With

| System | Mechanism | Details |
|--------|-----------|---------|
| `git` binary | `exec.Command("git", ...)` via `os/exec` | Spawns a child subprocess in `repoRoot`. Stdout captured to `bytes.Buffer`, stderr separately buffered. `cmd.Run()` executes the command and returns the exit error. |

### Error Propagation

- **RunGit**: Wraps the error with:
  ```
  fmt.Errorf("git command failed: %v, stderr: %s", err, trimmedErr)
  ```
  The original `err` from `exec.Cmd` (e.g., `*exec.Error`, non-zero exit status) is embedded as `%v`; stderr text is appended. No panic occurs on failure.

- **VerifyGitRepo**: Calls `RunGit(repoRoot, "rev-parse", "--is-inside-work-tree")`. If the returned error is non-nil, wraps with:
  ```
  fmt.Errorf("not a git repository (or any of the parent directories): %w", err)
  ```
  Uses `%w` for wrapError-compatible wrapping.