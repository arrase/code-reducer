# Internal Tools Module Architecture

## Module Responsibility

The `internal/tools` package provides two complementary subsystems: **file system operations** with path-safe read/write primitives and **Git subprocess execution** utilities for repository validation and command dispatching. All functions operate synchronously with no shared mutable state, making the module safe for concurrent use across goroutines without external synchronization.

---

## File System Operations (`file_tools.go`)

### Public Surface Area

| Function | Input Types | Output Types |
|---|---|---|
| `ReadFileSafely(repoRoot string, virtualPath string)` | `repoRoot` `string`, `virtualPath` `string` | `[]byte`, `error` |
| `WriteFileSafely(repoRoot string, virtualPath string, content []byte)` | `repoRoot` `string`, `virtualPath` `string`, `content` `[]byte` | `error` |
| `LoadGitignore(repoRoot string)` | `repoRoot` `string` | `[]string`, `error` |
| `ShouldIgnoreFile(repoRoot string, relPath string, gitIgnore *ignore.GitIgnore, ignoredExtensions []string)` | `repoRoot` `string`, `relPath` `string`, `gitIgnore` `*ignore.GitIgnore`, `ignoredExtensions` `[]string` | `bool` |
| `DiscoverCodeFiles(repoRoot string, ignores []string, ignoredExtensions []string)` | `repoRoot` `string`, `ignores` `[]string`, `ignoredExtensions` `[]string` | `[]string`, `error` |
| `IsBinaryFile(path string)` | `path` `string` | `bool` |

### Data Flow: Path Resolution Boundary

All read/write functions resolve paths through a `security.SafeResolve` wrapper before any OS call, establishing a consistent safety boundary around path resolution. The wrapper is the single point of trust for preventing directory traversal attacks; downstream filesystem calls never receive unvalidated absolute paths.

---

## File System Functions

### ReadFileSafely(repoRoot string, virtualPath string) ([]byte, error)

Resolves `virtualPath` relative to `repoRoot`, delegates to `security.SafeResolve`, then reads the resulting path via OS read primitive. Returns wrapped error if resolution or read fails. No panic on failure.

```
ReadFileSafely(repoRoot, virtualPath)
    └─ security.SafeResolve(repoRoot, virtualPath) → safePath (or error)
    └─ ReadFile(safePath) → bytes or error
```

### WriteFileSafely(repoRoot string, virtualPath string, content []byte) (error)

Performs a two-stage verification to prevent TOCTOU race conditions on writes:

1. Resolves `virtualPath` through `security.SafeResolve`, creates parent directories with mode 0755
2. Opens file with `O_WRONLY|O_CREATE` flags and mode 0644
3. Re-stat's both the resolved path and opened descriptor to confirm same inode; if they differ, returns "symlink race detected" error
4. Truncates to zero bytes
5. Writes content

After truncation, checks `os.ModeSymlink` via Lstat on the safe path. If symlink flag is set, returns a security violation instead of overwriting the symlink target. No panic on failure; errors are wrapped where applicable across multi-step operations.

```
WriteFileSafely(repoRoot, virtualPath, content)
    ├─ security.SafeResolve → safePath (or error)
    ├─ MkdirAll(parents, 0755)
    ├─ OpenFile(safePath, O_WRONLY|O_CREATE, 0644) → file or error
    ├─ Lstat(safePath) + f.Stat(file) → same inode check
    │   └─ Different inodes → "symlink race detected" error
    ├─ Truncate(file, 0)
    ├─ Write(file, content)
    └─ Close(file)
```

### LoadGitignore(repoRoot string) ([]string, error)

Opens `.gitignore` from disk at `repoRoot`. Parses line-by-line: skips empty lines and comments (`#`). Returns nil when absent (not an error). All other errors are returned bare without wrapping.

```
LoadGitignore(repoRoot)
    └─ Open(gitignorePath) → file or error
        ├─ Not exist → return nil, nil (intentional absence)
        └─ Read all lines → filter empty/comments → return []string, err
```

### ShouldIgnoreFile(repoRoot string, relPath string, gitIgnore *ignore.GitIgnore, ignoredExtensions []string) (bool)

Applies layered file-ignoring logic:

1. **Gitignore pattern check** — compiled patterns from user-provided ignore list
2. **Path component inspection** — dot-prefixed directories or `.egg-info` suffixes
3. **Extension matching** — normalized to lowercase, with leading-dot handling
4. **Short-circuit skip for known text file extensions** — avoids binary scan entirely
5. **Binary classification via IsBinaryFile only if neither of the above matched**

Returns `true` on any internal failure (swallows errors from `security.SafeResolve` and `IsBinaryFile`). No error return at all.

### DiscoverCodeFiles(repoRoot string, ignores []string, ignoredExtensions []string) ([]string, error)

Performs full directory walk:

- Skips the repo root itself
- Prunes entire subtrees when directory names match dot-prefix, `.egg-info`, or gitignore patterns (via `filepath.SkipDir`)
- Delegates individual file checks to `ShouldIgnoreFile`
- Collects results as forward-slash relative paths

Walk-dir callback returns `nil` for individual entry errors (skip-on-error). Final return wraps the walk error if present.

```
DiscoverCodeFiles(repoRoot, ignores, ignoredExtensions)
    ├─ Compile gitignore patterns from ignores list
    └─ WalkDir(repoRoot):
         ├─ Skip repo root
         ├─ If directory:
         │   ├─ Check dot-prefix / .egg-info → SkipDir if match
         │   └─ Check compiled gitignore patterns → SkipDir if match
         └─ If file:
              └─ ShouldIgnoreFile(repoRoot, relPath, gitIgnore, ignoredExtensions):
                   ├─ Gitignore pattern check → true
                   ├─ Path component dot-prefix / .egg-info → true
                   ├─ Extension matching (normalized) → true
                   ├─ Known text extensions map → false (skip binary scan)
                   └─ Else: IsBinaryFile(absPath) → true if binary
              └─ Append to result list
```

### IsBinaryFile(path string) (bool)

Reads the first 1024 bytes and returns `true` if any null byte is found. Used only after all other ignore heuristics fail, minimizing its invocation frequency. Returns `false` on any open/read failure — treats all failures as "not binary". No error return.

---

## Git Subprocess Execution (`git_tools.go`)

### Public Surface Area

| Name | Input Types | Output Types |
|------|-------------|--------------|
| **RunGit** | `repoRoot string`, `args ...string` | `(string, error)` |
| **VerifyGitRepo** | `repoRoot string` | `error` |

### Data Flow: Execute-and-Collect Pattern

Both functions follow an execute-and-collect pattern: spawn process → redirect I/O → wait for completion → return results + error status. No retries, no fallbacks, no state transitions. All external work happens through subprocess invocation; no file handles, database connections, or network clients are opened directly by this code.

---

## Git Functions

### RunGit(repoRoot string, args ...string) (string, error)

Executes a git command in a given directory, captures stdout and stderr into separate buffers, returns cleaned output and an error if the process exits non-zero. Stderr is attached even when exit code is 0 so warnings/notifications are not silently lost.

On failure: trims stderr content, wraps the underlying error with a new message combining both the original error (`%v`) and the trimmed stderr string. Returns `(string, wrappedError)`. No panic, no silent swallow.

```
RunGit(repoRoot, args...)
    ├─ exec.Command("git", args...)
    ├─ Redirect stdout → buffer
    ├─ Redirect stderr → buffer (attached even on success)
    └─ cmd.Run() → combined output + error status
        ├─ Error: trim stderr, wrap with original + stderr message
        └─ Success: return stdout string, nil
```

### VerifyGitRepo(repoRoot string) (error)

Two-step pre-flight check:

1. Confirms the `git` executable exists on the system path via `exec.LookPath("git")`. If fails: returns a sentinel-style `fmt.Errorf` with fixed message ("git executable not found in system path"). Does NOT unwrap — replaces entirely.
2. Runs `git rev-parse --is-inside-work-tree` to confirm target directory is inside a git working tree. If fails: returns another sentinel-style `fmt.Errorf` with fixed message ("not a git repository (or any of the parent directories)"). Again, does NOT propagate or wrap underlying error.

```
VerifyGitRepo(repoRoot)
    ├─ exec.LookPath("git") → path or error
        └─ Fail → return fmt.Errorf("git executable not found in system path")
    └─ RunGit(repoRoot, "rev-parse", "--is-inside-work-tree") → output or error
        └─ Fail → return fmt.Errorf("not a git repository (or any of the parent directories)")
```

---

## Error Handling Summary

| Function | Failure Pattern | Notes |
|---|---|---|
| `ReadFileSafely` | Wrapped with context prefix | Caller sees "failed to read file" + underlying error |
| `WriteFileSafely` | Mix of wrapped and unwrapped | Multi-step write: each failure wrapped; final success returns nil |
| `LoadGitignore` | Unwrapped intentional absence | Only unwrapped path in the file for non-not-exist errors |
| `ShouldIgnoreFile` | Returns true on internal failure | Swallows all errors from dependencies |
| `DiscoverCodeFiles` | Swallowed walk-dir per-entry errors + wrapped final err | Walk callback returns nil for individual failures; final return wraps walk error if present |
| `IsBinaryFile` | Returns false on open/read failure | Treats all I/O failures as "not binary" |
| `RunGit` | Wrapped with combined message | Original error + trimmed stderr appended |
| `VerifyGitRepo` | Sentinel-style fixed messages | Replaces underlying error entirely, never wraps |

**No panics.** All errors are returned to callers. The module uses error wrapping exclusively except for intentional unwrapped absence detection in `LoadGitignore`. No DB or network communication — all I/O is local filesystem only.