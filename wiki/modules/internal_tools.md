# internal/tools — Repository Tooling Module

## Module Responsibility & Data Flow

The `internal/tools` package provides two capability clusters: **safe filesystem operations** within a bounded repository root (`file_tools`) and **git process orchestration** (`git_tools`). Both clusters share the same safety contract—no mutable global state, no concurrency primitives, sequential execution only. All functions return `(result, error)`; panics are absent.

Data flow is unidirectional: callers pass in a `repoRoot` boundary and receive either structured output or a wrapped error. No shared state exists between functions, so call order is undefined from the module's perspective alone.

---

## File Operations (`file_tools`)

### Safe Read / Write

- **ReadFileSafely** — resolves the input path through `SafeResolve` (external dependency) and reads via `os.ReadFile`. Errors are wrapped with contextual prefix; no buffering beyond OS page cache is applied.
- **WriteFileSafely** — resolves path, creates parent directories (`MkdirAll`), opens target with `O_WRONLY|O_CREATE`, then applies a TOCTOU mitigation: an `Lstat` followed by `Fstat` compares inodes to detect symlink substitution between open and write. If the inode differs, a wrapped error is returned. Partial writes that fail after truncation are not rolled back; this is a documented limitation rather than an unhandled path.

### Ignore Decision Tree (`ShouldIgnoreFile`)

The decision tree evaluates four layers in order:

1. **Compiled gitignore patterns** — passed as `*ignore.GitIgnore` (compiled externally via `sabhiram/go-gitignore`, not within this file).
2. **Path-component heuristics** — dot-prefixed directories and `.egg-info` suffixes are auto-ignored without external configuration.
3. **Known text-file extensions** — a constant map of recognized source extensions short-circuits further checks.
4. **Binary detection fallback** — if no prior layer matched, the file is treated as ignored (binary).

The function returns `bool`. If `SafeResolve` fails (path outside repo root), it returns `true`; anything beyond the repository boundary is assumed safe to ignore by design.

### Code Discovery (`DiscoverCodeFiles`)

Walks the repository tree via recursive directory traversal. Each directory node checks its name against dot-prefix and `.egg-info` patterns, plus compiled gitignore matches — a match yields `SkipDir`. File nodes run through `ShouldIgnoreFile`; survivors are appended as slash-normalized relative paths. Walk errors encountered during iteration are dropped; only the accumulated walk error is surfaced at function exit.

### Binary Detection (`IsBinaryFile`)

Reads up to 1024 bytes and scans for null bytes. Returns `true` on detection, `false` otherwise. If the file cannot be opened, it returns `false`; this open-failure path is treated as a false negative rather than surfacing an error.

---

## Git Operations (`git_tools`)

### Process Execution (`RunGit`)

Constructs a command prefixed with `--no-pager`, executes in the supplied `repoRoot`, and captures both stdout/stderr into memory buffers. Whitespace is trimmed from captured output before return. If execution fails, stderr content is folded into a single wrapped error message alongside any captured stdout.

### Repository Validation (`VerifyGitRepo`)

Two-step check: first confirms the `git` binary exists in system PATH via `LookPath`; second invokes `rev-parse --is-inside-work-tree`. Either failure returns a distinct error — "not found" for missing binary, or explicit message indicating the directory is not inside any git work tree (or no parent qualifies).

---

## Module-Level Observations

- **No standard library package names are referenced** in this documentation; all external dependencies are called by fully qualified path.
- **Error wrapping is consistent**: `fmt.Errorf` with `%w` or `%v`/`%s` prefixes throughout. Errors support standard unwrapping semantics (`errors.Is`, `errors.Unwrap`). No sentinel errors defined.
- **No network calls, no database access** observed in either file.
- **Side effects are limited to disk I/O and process execution**. Both files spawn child processes (git binary) or perform direct filesystem reads/writes.