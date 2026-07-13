# Package: internal/tools — Architecture Document

## Module Responsibility

The `internal/tools` package provides repository-scanning primitives for a codebase-analysis tool. It exposes two functional domains: **safe local file I/O** (`file_tools.go`) and **git subprocess orchestration** (`git_tools.go`). Together they enable the caller to discover source-code files, classify their content type, read/write artifacts safely, and validate that the target directory is a git work-tree before proceeding.

Data flow follows a two-phase pattern:
1. `VerifyGitRepo` (or an equivalent pre-flight check) confirms the working directory corresponds to a valid git repository by invoking `git rev-parse --is-inside-work-tree`.
2. Once validated, `DiscoverCodeFiles` walks the entire tree under that root, applying layered ignore rules and returning only text-source files.

Both domains share these architectural constraints: **no panics**, **error wrapping** (stderr is preserved for diagnostics), and **no network/DB calls**. All I/O operates within a single local process or on the local filesystem.

---

## File Operations (`file_tools.go`)

### Safe Read / Write Primitive

`ReadFileSafely` and `WriteFileSafely` implement TOCTOU-safe file operations using the temp-file + rename pattern.

**Shared sequence:**
1. Resolve a virtual path to an absolute filesystem path via `SafeResolve`.
2. On write: create parent directories with `defaultDirPerm`; on read: no directory creation.
3. Open or create a temporary file in the target directory using `os.CreateTemp`.
4. Write content (for writes) or read from the resolved path (for reads).
5. Close and sync (writes only); close (reads only).
6. Rename temp into final destination for writes; return contents for reads.

**Symlink detection:** Both paths verify the target is not a symlink via `os.Lstat`/`f.Stat`. Pre-open checks prevent TOCTOU swap attacks where an attacker replaces a real file with a symlink between resolution and open. Post-open checks catch any state changes after the initial open.

**Error semantics:**
- `ReadFileSafely`: returns `nil, err` on failure; wraps each step with context strings (`failed to open file`, `failed to lstat`, etc.). Symlink detection yields an explicit "security violation" error rather than a generic I/O error.
- `WriteFileSafely`: all failure paths return wrapped errors (directory creation, temp write, sync, close, chmod, rename). No panic usage; no sentinel values.

**Deferred cleanup:** Both functions defer `f.Close()` for reads and use deferred `os.Remove` in `WriteFileSafely` to clean up temp files if the rename fails.

### Gitignore Handling

- **`LoadGitignore(repoRoot)`**: Opens `.gitignore` under `repoRoot`. If absent (`os.IsNotExist`), returns `nil, nil` — no error raised. Any other open/read failure is returned unwrapped.
- **`ShouldIgnoreFile(repoRoot, relPath, gitIgnore)`**: Applies layered rules:
  1. Check against compiled `GitIgnore` patterns (user-supplied + `.gitignore`).
  2. Reject any path component starting with `.` or ending with `.egg-info`, regardless of other rules.
  3. If extension matches a known text-source language list, accept as code.
  4. For unknown extensions: resolve absolute path and scan first 1024 bytes for null bytes; binaries return `true`.

**Side effect note:** `ShouldIgnoreFile` opens the file on every invocation to classify unknown extensions — this is a real I/O side effect per call, not cached.

### Code Discovery Pipeline (`DiscoverCodeFiles`)

**Input:** repository root + list of ignore patterns (strings).
**Output:** slice of relative source-code paths.

**Algorithmic flow:**
1. Compile user-supplied ignore patterns and `.gitignore` entries into a single `GitIgnore` object via `LoadGitignore`.
2. Walk the entire tree recursively using `filepath.WalkDir`. For each entry:
   - Directory: if name starts with `.` or matches any compiled pattern, skip subtree entirely.
   - File: delegate to `ShouldIgnoreFile`.
3. Collect passing files into result slice.

**Walk error handling:** If a walk callback returns an error for a single entry, the error is printed to `stderr` and that item is skipped (`return nil`). The overall `err` from `WalkDir` propagates at the end — terminal fatal errors are returned; non-fatal entries are swallowed silently.

### Binary Classification (`IsBinaryFile`)

Scans first 1024 bytes for null bytes to distinguish text source files from binary content. Returns `true` (ignored) on any I/O error other than EOF — this "fail-closed" assumption treats unreadable files as likely binary rather than propagating the actual error.

**Constants:** `binaryDetectionBufSize` is lowercase and package-private; not exported.

---

## Git Tools (`git_tools.go`)

### External Command Execution (`RunGit`)

Spawns `git --no-pager` via `exec.Command` with any additional arguments from the caller. The subprocess runs within `repoRoot` as its working directory. Stdout/stderr are captured into in-memory `bytes.Buffer`; no file, network, or DB I/O occurs.

**Failure handling:** On error, wraps the original with stderr content:
```
fmt.Errorf("git command failed: %v, stderr: %s", err, trimmedErr)
```
Returns `(string, error)` — partial stdout is still delivered alongside the error. No panics.

### Repository Validation (`VerifyGitRepo`)

Delegates to `RunGit` with subcommand `rev-parse --is-inside-work-tree`. If this invocation results in an error, returns a fixed message: `"not a git repository (or any of the parent directories)"`. The original stderr is discarded at this wrapper layer — normalized to a single sentinel-style message for downstream callers.

---

## Concurrency & State Analysis

**Mutable state:** None detected across both files. All constants are `const` (compile-time); all variables (`safePath`, `dir`, `tmpFile`, `tmpName`, `patterns`, `files`, `buf`, `cmd`, `stdout`, `stderr`, etc.) are function-local and never escape their defining scope. No package-level global state is modified.

**Concurrency mechanisms:** None detected. No `sync.Mutex`, channels, `atomic` types, or other synchronization primitives. Shared mutable state does not exist to protect.

---

## Error Handling Summary

| Pattern | Where |
|---|---|
| Wrap-and-return (preserve stderr) | `RunGit` |
| Normalize to sentinel message | `VerifyGitRepo` |
| Sentinel + wrap with context | `ReadFileSafely`, `WriteFileSafely`, `LoadGitignore` |
| Boolean return, resolve failures as "ignored" | `ShouldIgnoreFile` |
| Swallow per-entry walk errors, propagate terminal | `DiscoverCodeFiles` |
| Fail-closed (I/O error → binary assumption) | `IsBinaryFile` |

**No panics** observed across either file. All errors are returned to callers as Go values.