# internal/tools — Repository Traversal, Safe File I/O, and Git Utilities

This module provides three independent capability groups: **repository code discovery**, **safe file read/write operations** with TOCTOU race mitigation, **gitignore-aware filtering**, and **Git subprocess execution**. All functions are standalone (no methods on types); no exported structs or interfaces exist. No goroutines, channels, or sync primitives are used anywhere in the package—every call is synchronous and single-threaded.

---

## Repository Code Discovery

`DiscoverCodeFiles` walks `repoRoot` recursively via `filepath.WalkDir`, skipping directories matching `.gitignore` patterns (compiled by `LoadGitignore`) or any dot-prefixed component (`.build`, `.svn`, etc.), then filters each leaf through `ShouldIgnoreFile`. Valid source paths are accumulated into a slice and returned.

Walk callback errors from `filepath.WalkDir` are swallowed—individual directory entries that fail to stat return `nil` to the caller, making it impossible to distinguish an intentional skip from a walk failure downstream.

---

## Safe File Read / Write Operations

### `ReadFileSafely(repoRoot, virtualPath)` → `(data []byte, err error)`

Resolves `virtualPath` through `security.SafeResolve` (path-traversal guard), then reads the resulting absolute path via `os.ReadFile`. Errors from both `SafeResolve` and `os.ReadFile` are wrapped with prefix `"failed to read file:"` using `%w`.

### `WriteFileSafely(repoRoot, virtualPath, content []byte)` → `error`

Performs TOCTOU-safe writes:

1. Resolve the safe absolute path via `security.SafeResolve`.
2. Call `os.MkdirAll(dir, 0755)` to create parent directories if missing; wraps with `"failed to create directory"`.
3. Open for write+create (`O_WRONLY|O_CREATE`, mode `0644`); wraps open failure with `"failed to open file for writing"`.
4. Call `os.Lstat(safePath)` (no symlink follow) and `f.Stat()`; compare via `os.SameFile(fiLstat, fiFstat)`. If they differ—indicating a symlink swap or race condition—return an error prefixed `"security violation"` or `"TOCTOU symlink race detected"`.
5. Truncate (`f.Truncate(0)`), then write content; wrap each with `"failed to truncate file:"` / `"failed to write file content:"`.

---

## Gitignore Handling and Binary Classification

### `LoadGitignore(repoRoot)` → `(patterns []string, err error)`

Reads `.gitignore` from `repoRoot`, scanner processes line-by-line. Empty file returns `nil, nil`; non-existence (`os.IsNotExist`) also returns `nil, nil`. All other errors are wrapped. Patterns returned are active non-comment entries.

### `ShouldIgnoreFile(repoRoot, relPath string, gitIgnore *ignore.GitIgnore, ignoredExtensions []string)` → `bool`

Applies layered ignore rules:

- Compiles user-supplied patterns via the external `github.com/sabhiram/go-gitignore` package (`*ignore.GitIgnore`).
- Rejects paths with dot-prefixed components or `.egg-info` suffixes (build artifacts).
- Checks file extension against a provided blacklist.
- For unknown extensions, reads the first 1024 bytes and scans for null bytes via `IsBinaryFile`. If null bytes are present, returns `true` (binary → ignore).

### `IsBinaryFile(path string)` → `bool`

Opens the path; if `os.Open` fails, returns `false` (treats unreadable as non-binary). Reads up to 1024 bytes. Only wraps `io.EOF` (expected EOF) without error—other read errors return `false`. No error wrapping for open or other read failures beyond EOF handling.

---

## Git Subprocess Execution

### `RunGit(repoRoot string, args ...string)` → `(stdout string, err error)`

Constructs an `exec.Command` for `git`, prepends `--no-pager` to user-supplied arguments, sets `cmd.Dir = repoRoot`. Captures stdout into a `bytes.Buffer`, reads it back, trims whitespace. On success returns trimmed output; on failure wraps the original error with stderr text: `"git command failed: %v, stderr: %s"`.

### `VerifyGitRepo(repoRoot string)` → `error`

Two-stage validation:

1. Calls `exec.LookPath("git")`; if unavailable, returns a fresh error without wrapping—no reference to any prior failure since none exists at that point.
2. Otherwise delegates to `RunGit(repoRoot, "rev-parse", "--is-inside-work-tree")`. If the inner call fails, discards its returned error entirely and returns a new message: `"not a git repository (or any of the parent directories)"`.

---

## Error Handling Summary

| Function | Failure Mode | Strategy |
|---|---|---|
| `ReadFileSafely` | Resolve or read failure | Wrap with `"failed to read file:"`, `%w` |
| `WriteFileSafely` | Directory open, file open, lstat/stat mismatch, truncate, write | Contextual prefix per step; symlink race → `"security violation"` / `"TOCTOU symlink race detected"` |
| `LoadGitignore` | Non-existence or read error | `nil, nil` on missing/empty; wrap everything else |
| `DiscoverCodeFiles` | Walk entry failure | Swallowed—caller sees no signal |
| `IsBinaryFile` | Open failure or non-EOF read error | Returns `false`; EOF is expected and ignored |
| `RunGit` | Non-zero exit / exec error | Wrap original + stderr text with `%v`, `%s` |
| `VerifyGitRepo` | Missing git binary | Fresh error, no wrapping |
| `VerifyGitRepo` | Git query failure | Discard inner error; return fresh message only |