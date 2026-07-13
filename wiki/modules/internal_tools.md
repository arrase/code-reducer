# internal/tools — Repository File Operations & Git Integration

## Module Responsibility

The `internal/tools` package provides safe filesystem I/O primitives and Git command execution utilities for operating on a repository root directory. It is split into two files: file handling (`file_tools.go`) and Git interaction (`git_tools.go`). The module has no mutable state and performs only local disk operations.

## Data Flow

```
caller → [ReadFileSafely | WriteFileSafely] → security.SafeResolve (path validation) → OS open/write/rename
```

```
caller → [DiscoverCodeFiles | ShouldIgnoreFile] → gitignore parsing / extension whitelist / binary detection → []string or bool
```

```
caller → RunGit(repoRoot, args...) → exec.Command("git", ...) → bytes.Buffer capture → stdout trimmed string
```

## File I/O: `file_tools.go`

### Safe Read and Write

**ReadFileSafely** resolves the virtual path against `repoRoot`, performs a TOCTOU-safe open-and-stat sequence to prevent symlink races, reads the file contents via `io.ReadAll`, and returns `(content []byte, error)`. All OS errors are wrapped with a descriptive prefix.

**WriteFileSafely** ensures parent directories exist via `os.MkdirAll` (`defaultDirPerm = 0755`), creates a temporary file in the same directory as the destination using `os.CreateTemp`, writes content, syncs and closes the temp file, applies `Chmod` with `defaultFilePerm = 0644`, then atomically renames into place. Each step wraps its error; on failure, the deferred cleanup block unconditionally calls `tmpFile.Close()` and `os.Remove(tmpName)`. If `os.Rename` fails after `Chmod`, the destination path may be left in an inconsistent state with no rollback.

### Gitignore Loading and File Classification

**LoadGitignore** reads `.gitignore` from `repoRoot` using `bufio.Scanner`. Non-comment lines are parsed via `ignore.CompileIgnoreLines`; existence errors return `(nil, nil)` (treated as success). Scanner errors at end-of-loop propagate directly.

**ShouldIgnoreFile** evaluates whether a file at relative path is ignored by applying four checks in order:
1. User-defined patterns from the loaded `GitIgnore` instance via `ignore.GitIgnore`.
2. Dot-prefixed components or `.egg-info` suffixes in the path.
3. Known text extension whitelist (go, js, ts, py, md, json, yaml, etc.).
4. Binary detection: opens the file and scans 1024 bytes for null bytes; returns `true` if any are found.

If `SafeResolve` fails during classification, returns `true`.

### Source Discovery and Binary Detection

**DiscoverCodeFiles** recursively walks the repo root via `filepath.WalkDir`, skipping directories matching gitignore patterns or dot-prefixed names. For each file, it runs the ignore classification; surviving relative paths are collected into a result list. Walk callback errors (directory traversal failures) are logged to `os.Stderr` and skipped (`return nil`). The final `(files, err)` from `filepath.WalkDir` is propagated as-is — callers receive whatever error the walker produced at completion.

**IsBinaryFile** opens a file and reads 1024 bytes (constant `binaryDetectionBufSize = 1024`). Any `os.Open` error returns `true` (assumes binary). Reads other than `io.EOF` return `true`. Clean read with no null bytes returns `false`.

### Constants (`file_tools.go`)

| Constant | Value | Description |
|---|---|---|
| `binaryDetectionBufSize` | 1024 | Buffer size for binary file detection |
| `defaultDirPerm` | 0755 | Default directory permission mode |
| `defaultFilePerm` | 0644 | Default file permission mode |

### Error Propagation (`file_tools.go`)

- **Defensive wrapping**: `ReadFileSafely` and `WriteFileSafely` wrap every OS error with a descriptive prefix via `fmt.Errorf("...: %w", err)`.
- **Swallowed/logged only**: WalkDir errors in `DiscoverCodeFiles` are written to stderr and not returned. LoadGitignore treats non-existence as success. `ShouldIgnoreFile` returns `true` on resolution failure rather than propagating the error.
- **Implicit assumptions**: `IsBinaryFile` assumes binary state when open or read errors occur.

## Git Operations: `git_tools.go`

### Public API Surface

**RunGit(repoRoot string, args ...string)** executes an arbitrary git command within `repoRoot`. Prepends `--no-pager` to disable pager wrapping. Captures stdout and stderr into memory buffers (`bytes.Buffer`). On success returns `(trimmed string, nil)`. On failure returns `(trimmed string, error)` where the error message includes stderr content.

**VerifyGitRepo(repoRoot string)** validates that `repoRoot` is inside a git working tree by invoking `git rev-parse --is-inside-work-tree`. If `RunGit` fails, wraps as `"not a git repository (or any of the parent directories)"`; underlying stderr from git is discarded at this layer.

### Subprocess Interaction Model

The only external system interaction in this file is spawning `git` as a child process via `exec.Command("git", ...)` and waiting for completion with `cmd.Run()`. All other I/O (reading `.git` objects, filesystem traversal) occurs inside the git binary itself.

**Error Propagation (`git_tools.go`)**:
- `cmd.Run()` error + non-zero exit → wrapped with both stderr text and exit code in a single `fmt.Errorf`.
- Success with output → `(trimmed string, nil)`.
- No panic/recover patterns — errors propagate as Go `error` interface values.