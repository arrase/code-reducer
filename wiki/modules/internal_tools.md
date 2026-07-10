# `internal/tools` — File System & Git Operations Module

## Module Responsibility

This module provides two complementary interfaces for repository-aware operations: safe file I/O with TOCTOU race mitigation, and Git process abstraction for executing commands within the working tree. Both modules operate on a single root path (`repoRoot`) without shared mutable state, and neither uses concurrency primitives — all functions are goroutine-safe by construction.

---

## `file_tools.go` — Safe File I/O & Discovery

### Core Functions

| Function | Input | Output |
|---|---|---|
| [`ReadFileSafely`](file:///path/to/file_tools.go#L15-L64) | `repoRoot`, `virtualPath` `string` | `[]byte`, `error` |
| [`WriteFileSafely`](file:///path/to/file_tools.go#L70-L132) | `repoRoot`, `virtualPath` `string`; `content` `[]byte` | `error` |
| [`LoadGitignore`](file:///path/to/file_tools.go#L138-L164) | `repoRoot` `string` | `[]string`, `error` |
| [`ShouldIgnoreFile`](file:///path/to/file_tools.go#L170-L236) | `repoRoot`, `relPath` `string`; `gitIgnore` `*ignore.GitIgnore`; `ignoredExtensions` `[]string` | `bool` |
| [`DiscoverCodeFiles`](file:///path/to/file_tools.go#L242-L291) | `repoRoot` `string`; `ignores`, `ignoredExtensions` `[]string` | `[]string`, `error` |
| [`IsBinaryFile`](file:///path/to/file_tools.go#L297-L315) | `path` `string` | `bool` |

### TOCTOU Race Mitigation Pattern

Both `ReadFileSafely` and `WriteFileSafely` implement a Time-Of-Check-Time-Of-Use safety pattern:

1. Resolve the virtual path to an absolute safe path via `SafeResolve`
2. Open the file descriptor first
3. Perform `lstat` on the resolved path (no symlink following)
4. Perform `fstat` on the open file descriptor (follows symlinks)
5. Verify that `lstat` and `fstat` reference the same inode — mismatch indicates a symlink race between opening and checking

Both operations reject any symlink detection as a security violation. On success, `ReadFileSafely` reads all content via buffered I/O; on success, `WriteFileSafely` truncates to zero first (safe only because symlinks were ruled out), then writes the provided content.

### Error Handling Contract

| Function | Wrapped Errors (`%w`) | Swallowed | Sentinel Unwrapped |
|---|---|---|---|
| `ReadFileSafely` | Yes, for every I/O error | No | Symlink race check |
| `WriteFileSafely` | Yes, for every I/O error | No | Symlink race check |
| `LoadGitignore` | No | Missing file → `(nil, nil)` | None |
| `ShouldIgnoreFile` | No | `SafeResolve` err → returns `true` | None |
| `DiscoverCodeFiles` | No | Extensive — walk callback errors swallowed at each node | None |
| `IsBinaryFile` | No | All open/read errors → `false` | None |

### File Discovery Algorithm (Multi-Layer Ignore Rules)

A file is considered "ignored" if it matches **any** of these conditions:

- Matches a compiled gitignore pattern list
- Has a path component starting with `.` or ending in `.egg-info`
- Has an extension matching the user-provided ignored extensions list (case-insensitive, supports both `.` prefix and suffix forms)
- Is detected as binary by reading up to 1024 bytes and scanning for null byte (`\x00`)

If any single check returns true, the file is excluded. This is a **union** of ignore conditions.

The primary business rule is to recursively walk the repository root and discover high-signal source code files while filtering out noise: build artifacts, dependency directories, output files, dot-prefixed hidden paths, `.egg-info` markers, binary files, and any user-defined ignore patterns (via gitignore compilation).

### Binary Detection Rule

A file is considered binary if its first 1024 bytes contain a null byte (`\x00`). Files that cannot be opened are treated as non-binary (returns false), avoiding the loading of entire files into memory for classification.

---

## `git_tools.go` — Git Process Abstraction

### Core Functions

| Function | Input | Output |
|---|---|---|
| [`RunGit`](file:///path/to/git_tools.go#L15-L64) | `repoRoot string`, variadic `args ...string` | `(string, error)` |
| [`VerifyGitRepo`](file:///path/to/git_tools.go#L70-L89) | `repoRoot string` | `error` |

### Execution Model

Both functions spawn the `git` binary via `exec.Command("git", ...)` with `--no-pager`, restricting execution to the provided root directory. Both capture stdout and stderr into buffers synchronously. No standard library packages beyond `bytes`, `fmt`, `os/exec`, `strings` are imported.

### Error Handling Contract

| Function | Failure Path | Behavior |
|---|---|---|
| `RunGit` | Exit code ≠ 0 | Wraps error: `git command failed: %v, stderr: %s`. Returns `(string, error)` with stdout and wrapped error. No panic. |
| `RunGit` | Exit code 0 | Returns `(trimmedOut, nil)`. Stdout is whitespace-trimmed; stderr is discarded on success. |
| `VerifyGitRepo` | `RunGit(...)` returns error | Wraps it: `not a git repository (or any of the parent directories)`. The original stderr content is lost in this wrapper. Returns only the wrapped sentinel-like error. No panic. |

### Repository Integrity Check

A directory is considered a valid Git working tree only if `git rev-parse --is-inside-work-tree` succeeds; any failure indicates the path is outside a Git working tree or not a git repository at all. The verification phase delegates to the execution phase with specific arguments, interpreting success as confirmation of repository validity and failure as an assertion that the directory is not a Git repository.

---

## Data Flow Summary

```
DiscoverCodeFiles(repoRoot, ignores, ignoredExtensions)
├── Compile gitignore from ignore lines → gitIgnore object (in-memory, no I/O)
└── filepath.WalkDir(repoRoot):
    ├── Skip repo root itself (first entry)
    ├── If directory:
    │   ├── Check name against dot-prefix / .egg-info / gitIgnore
    │   └── If any match → Skip entire subtree
    └── If file:
        └── ShouldIgnoreFile check:
            ├── gitignore matches? → ignore
            ├── path component starts with "." or ends ".egg-info"? → ignore
            ├── extension in ignoredExtensions list? → ignore
            ├── binary? → ignore
            └── otherwise → add to result list

Result: flat list of relative paths representing source code files that pass all safety and filtering rules.
```