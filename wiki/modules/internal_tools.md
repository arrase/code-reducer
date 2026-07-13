# Module: `internal/tools` — Repository-Aware File & Git Operations

## Responsibility and Data Flow

This module provides safe, repository-aware file operations (read/write/discover) that respect `.gitignore` rules, plus utilities for executing `git` commands against a repo root. All functions operate on an absolute filesystem rooted at the caller-supplied `repoRoot`. No mutable global state is held; every call resolves paths locally and returns results directly.

Data flow: caller supplies `repoRoot` → module validates or constructs absolute paths under it → I/O occurs within that boundary → results (bytes, strings, file lists) are returned to caller with errors propagated (or swallowed per documented convention). No network, database, or API calls exist in this module; all external interactions are local filesystem and OS process invocations.

---

## File Operations — `file_tools.go`

### Safe Path Resolution

All read/write/discover functions begin by resolving a virtual/relative path into an absolute filesystem path confined to `repoRoot`. The resolution step is delegated to `security.SafeResolve`, which performs the traversal-safe check before any I/O. Errors from this step are returned directly (or wrapped) and never panic/recovered within this module.

### Reading Files — `ReadFileSafely`

```go
func ReadFileSafely(repoRoot, virtualPath string) ([]byte, error)
```

**Parameters:**
- `repoRoot` (`string`) — repository root path.
- `virtualPath` (`string`) — logical file path inside the repo.

**Returns:**
- `([]byte)` — file content bytes.
- `error`

**Flow:** Resolves `repoRoot/virtualPath` via safe resolution → reads the absolute target with `os.ReadFile` → wraps any read error as `"failed to read file content: %w"`. No panic/recovery path exists; all errors propagate to caller.

### Writing Files — `WriteFileSafely`

```go
func WriteFileSafely(repoRoot, virtualPath string, content []byte) error
```

**Parameters:**
- `repoRoot` (`string`) — repository root path.
- `virtualPath` (`string`) — logical file path inside the repo.
- `content` (`[]byte`) — bytes to write.

**Returns:**
- `error`

**Flow:** Resolves target path safely → creates parent directories via `os.MkdirAll` (errors wrapped individually with descriptive prefix) → writes content into a temporary file in the same directory using `os.CreateTemp`, suffix `.tmp.*` → syncs and closes → atomically renames temp over final target → applies `os.Chmod`. Each step's error is wrapped individually before propagating to caller. No partial-write corruption path: atomic rename ensures either pre-rename state or post-rename state only.

### Loading Gitignore — `LoadGitignore`

```go
func LoadGitignore(repoRoot string) ([]string, error)
```

**Parameters:**
- `repoRoot` (`string`) — repository root path.

**Returns:**
- `([]string)` — active ignore patterns (empty slice if no `.gitignore` exists).
- `error`

**Flow:** Opens `<repoRoot>/.gitignore` via `os.Open` with deferred close (file is always closed regardless of error) → if the file does not exist (`os.IsNotExist`), returns `(nil, nil)` → any other open error propagates after deferred-close semantics. The returned slice contains non-comment, non-empty lines parsed from the file.

### Ignoring Files — `ShouldIgnoreFile`

```go
func ShouldIgnoreFile(relPath string, gitIgnore *github.com/sabhiram/go-gitignore.GitIgnore) bool
```

**Parameters:**
- `relPath` (`string`) — relative file path.
- `gitIgnore` (`*github.com/sabhiram/go-gitignore.GitIgnore`) — compiled Git ignore matcher (may be nil).

**Returns:**
- `bool`

**Flow:** Checks whether `relPath` matches any loaded gitignore pattern via the provided compiled matcher. Additionally filters dot-prefixed directory components and `.egg-info` suffixes regardless of gitignore content. Returns true if matched; false otherwise. A nil `gitIgnore` produces a default-matching behavior (not explicitly documented beyond the nil allowance).

### Discovering Code Files — `DiscoverCodeFiles`

```go
func DiscoverCodeFiles(repoRoot string, ignores []string) ([]string, error)
```

**Parameters:**
- `repoRoot` (`string`) — repository root path.
- `ignores` (`[]string`) — list of ignore strings to compile into a GitIgnore matcher.

**Returns:**
- `([]string)` — discovered file paths (relative, slash-separated).
- `error`

**Flow:** Recursively walks `repoRoot` via `filepath.WalkDir`. For each entry: computes relative path, skips ignored directories including hidden ones, and collects non-ignored file paths as discovered code files. Walk errors are printed to stderr (`fmt.Fprintf(os.Stderr, ...)`) and swallowed — the walk continues with `return nil`, effectively skipping errored entries. Relative-path computation errors follow the same pattern (printed, skipped). The final return value is `(files, err)` where `err` reflects whatever error `WalkDir` itself produced if none were skipped; otherwise it will be `nil`.

---

## Git Operations — `git_tools.go`

### Running Git Commands — `RunGit`

```go
func RunGit(repoRoot string, args ...string) (string, error)
```

**Parameters:**
- `repoRoot` (`string`) — path to the repository root directory.
- `args` (`...string`) — variable-length slice of git command arguments appended after `--no-pager`.

**Returns:**
- `string` — trimmed stdout from the git command, or an empty string on failure.
- `error` — non-nil if the git process exited with a non-zero status; wraps both exit error and stderr output.

**Flow:** Spawns `git` via `exec.Command("git", --no-pager, args...)` running in `repoRoot` (via `cmd.Dir`). Stdout and stderr are captured into memory buffers (`bytes.Buffer`). If the command fails, returns `(string{}, err)` where `err` is wrapped as:

```
fmt.Errorf("git command failed: %v, stderr: %s", err, trimmedErr)
```

The original error (`err`) is included verbatim via `%v`, and `stderr.String()` (trimmed) is appended. No wrapping with `%w`; callers cannot use `errors.Is()` to recover the underlying exec error from this wrap. On success, returns `(trimmedStdout, nil)`.

### Verifying Git Repository — `VerifyGitRepo`

```go
func VerifyGitRepo(repoRoot string) error
```

**Parameters:**
- `repoRoot` (`string`) — path to the directory to check.

**Returns:**
- `error` — non-nil if git is not available or the path is not inside a git work tree; wraps the underlying `RunGit` error with context about which parent directories were checked.

**Flow:** Invokes `git rev-parse --is-inside-work-tree` via `RunGit`. If it returns an error, wraps as:

```
fmt.Errorf("not a git repository (or any of the parent directories): %w", err)
```

Uses `%w` (Go 1.13+ wrapping), so callers can use `errors.Is()` to match on `"not a git repository..."`. No panic or crash paths observed. All external failures are surfaced as returned errors, not swallowed.