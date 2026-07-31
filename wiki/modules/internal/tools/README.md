# Module: `internal/tools` — Repository Analysis Utilities

## Responsibility & Data Flow

The `internal/tools` package provides two complementary capabilities for analyzing a code repository's structure: (1) safe file I/O operations with TOCTOU protection and atomic writes, and (2) Git command abstraction for process execution. All functions operate against a local filesystem only; no network calls, database interactions, or external API invocations occur.

The module's entry point is the `repoRoot` string passed to every public function. Data flows from caller → `internal/tools` package functions → OS subsystems (POSIX file system for I/O operations, git binary via subprocess execution). No mutable state exists within any function scope; all operations are stateless and idempotent with respect to repository content.

---

## File Operations (`file_tools.go`)

### Atomic Write — `WriteFileAtomic`

```go
func WriteFileAtomic(targetPath string, data []byte, perm os.FileMode) error
```

Writes binary data to `targetPath` using a temp-file + rename pattern that prevents partial or corrupt state if interrupted. The directory is created with mode `0755`. Returns an error on any failure; returns `nil` only after the final rename succeeds.

**Sequence:**
1. Create temporary file in same directory as target (ensures atomic rename semantics)
2. Write data to temp file, sync before close for crash safety
3. Close and flush via defer block with cleanup gated by success flag
4. Apply `perm` mode via `os.Chmod`
5. Atomic rename from temp path into final location

Errors at any step are wrapped; deferred cleanup runs on failure paths to prevent partial temp file leakage. No panic/recover anywhere in this function.

### Safe Read — `ReadFileSafely`

```go
func ReadFileSafely(repoRoot string, virtualPath string) ([]byte, error)
```

Resolves the virtual path against `repoRoot`, then reads via `os.ReadFile`. Wraps any read failure with context ("failed to read file content"). The path resolution error from `security.SafeResolve` propagates unwrapped.

### Safe Write — `WriteFileSafely`

```go
func WriteFileSafely(repoRoot string, virtualPath string, content []byte) error
```

Delegates to both `ReadFileSafely` (for path resolution) and `WriteFileAtomic`. Unwrapped `security.SafeResolve` error propagates through. Otherwise defers to `WriteFileAtomic`'s return value.

### Gitignore Integration (`file_tools.go`)

#### Lockfile Recording — `EnsureGitignoreHasLockfile`

```go
func EnsureGitignoreHasLockfile(repoRoot string) error
```

Ensures `.gitignore` contains a line matching `security.LockFileName`. Reads the existing file, appends the lock entry if absent (adding a newline before it when needed), and writes back atomically. Returns an error on read/write failures. Read errors from `.gitignore` (except `os.IsNotExist`) are wrapped with "error reading .gitignore". A non-existent `.gitignore` falls through to the write path, which will fail if the directory does not exist; no special handling for that intermediate state.

#### Loading — `LoadGitignore`

```go
func LoadGitignore(repoRoot string) ([]string, error)
```

Reads `.gitignore` from `repoRoot`, strips comments (`#`) and blank lines, returns remaining patterns as `[]string`. Returns `nil, nil` when no `.gitignore` exists (treated as success). Any other read failure propagates unwrapped. Scanner errors appended via `scanner.Err()` at end — if a scan error occurs mid-stream but lines have already been populated, the partial result still gets returned to the caller.

#### Path Filtering — `ShouldIgnoreFile`

```go
func ShouldIgnoreFile(relPath string, gitIgnore *ignore.GitIgnore) bool
```

Returns `true` when the given relative path should be ignored: matches via the provided GitIgnore object (if non-nil), contains a dot-prefixed component, or ends with `.egg-info`. No I/O; returns only boolean values. No error path.

#### Discovery — `DiscoverCodeFiles`

```go
func DiscoverCodeFiles(repoRoot string, ignores []string) ([]string, error)
```

Recursively walks `repoRoot` to collect relative file paths that pass ignore filtering. Skips directories whose names start with `.`, end with `.egg-info`, or match the compiled GitIgnore rules. Returns a list of valid relative paths and any walk error. Walk errors are wrapped with context (path + "error walking"). Relative-path computation failures are also wrapped. Directory entries matching ignore rules return `filepath.SkipDir`. Non-matching files append to the slice.

**Walk sequence:**
1. Iterate via `filepath.WalkDir` from `repoRoot`
2. For each directory entry: skip if name starts with `.`, ends with `.egg-info`, or matches compiled GitIgnore patterns
3. For each file entry: run both ignore-checks (compiled rules + dot-prefixed / `.egg-info` suffix), collect those that pass

---

## Git Integration (`git_tools.go`)

### Command Execution — `RunGit`

```go
func RunGit(repoRoot string, args ...string) (string, error)
```

Executes a git command in the specified repo directory with `--no-pager`. Returns trimmed stdout and any error.

**Sequence:**
1. Construct invocation: `git --no-pager <args>` in `repoRoot`
2. Spawn child process via `exec.Command("git", ...)` — subprocess runs in `repoRoot`
3. Capture stdout into `bytes.Buffer`, stderr separately buffered
4. If `cmd.Run()` succeeds: trim whitespace from captured stdout, return as string result
5. If `cmd.Run()` fails: trim whitespace from captured stderr, wrap with `fmt.Errorf("git command failed: %v, stderr: %s", err, trimmedErr)`

The original error from `exec.Cmd` (e.g., `*exec.Error`, non-zero exit status) is embedded as `%v`; stderr text appended. No panic occurs on failure.

### Repository Validation — `VerifyGitRepo`

```go
func VerifyGitRepo(repoRoot string) error
```

Checks that `git` is available and that `repoRoot` is inside a git working tree. Invokes `git rev-parse --is-inside-work-tree`. If the returned error is non-nil, wraps with: `fmt.Errorf("not a git repository (or any of the parent directories): %w", err)`. Uses `%w` for wrapError-compatible wrapping (recoverable by unwrappers).

---

## Error Propagation Patterns

### Disk I/O — File Operations

All functions interact exclusively with the local filesystem via standard OS calls (`os.ReadFile`, `os.CreateTemp`, `os.MkdirAll`, `os.Rename`, `os.Chmod`, `os.Open`). No network, database, or external API interactions exist.

| Function | I/O Type | External System |
|---|---|---|
| `ReadFileSafely` | Disk read (`os.ReadFile`) | Local filesystem — target path resolved through `security.SafeResolve` before reading |
| `WriteFileAtomic` | Disk write (create temp → sync → chmod → rename) | Local filesystem — writes to a `.tmp.*` intermediate file then renames into place |
| `WriteFileSafely` | Disk read + disk write | Delegates path resolution and atomic write; no additional I/O beyond what the two sub-functions perform |
| `EnsureGitignoreHasLockfile` | Disk read (`os.ReadFile`) + disk write (`WriteFileAtomic`) | Reads `.gitignore`, appends lockfile pattern, writes back atomically |
| `LoadGitignore` | Disk read (`os.Open` + `bufio.Scanner`) | Reads `.gitignore`; returns `nil, nil` when file does not exist (no error) |
| `DiscoverCodeFiles` | Disk walk (`filepath.WalkDir`) | Recursively walks the repository root; skips directories matching ignore rules or dot-prefixed names |

### Process I/O — Git Operations

The only external interaction is subprocess execution against the installed `git` binary. Stdout captured to `bytes.Buffer`, stderr separately buffered. `cmd.Run()` executes the command and returns the exit error.

| Function | System | Mechanism |
|---|---|---|
| `RunGit` | External process (git) | `exec.Command("git", ...)` via `os/exec` — spawns child git subprocess in `repoRoot`. Stdout captured to `bytes.Buffer`, stderr separately buffered. `cmd.Run()` executes the command and returns the exit error. |
| `VerifyGitRepo` | External process (git) | `RunGit(repoRoot, "rev-parse", "--is-inside-work-tree")` — delegates to `RunGit` for execution; wraps non-nil return value with repository classification error |

---

## Concurrency & State

No mutable state exists within any function scope. All operations are stateless and idempotent with respect to repository content. No goroutine spawning or concurrent access patterns observed in this module.

---

## Notable Observations

- **TOCTOU mitigation**: `WriteFileAtomic` uses a temp file + rename pattern, which avoids race conditions on the target path during writes
- **Sync before close**: `tmpFile.Sync()` is called before `Close()`, ensuring data reaches stable storage before the handle is released — relevant for crash safety but adds latency per write
- **Deferred cleanup in WriteFileAtomic**: On any error, `tmpFile.Close()` and `os.Remove(tmpName)` run via defer. This prevents partial temp file leakage on failure paths. The flag `success` gates whether to trigger cleanup
- **No panic/recover anywhere** — all errors are returned as typed values. No `panic`, no bare recovery blocks observed in this module or its tests