# internal/tools

## Module Responsibility

The `internal/tools` package provides low-level utility primitives for repository-aware operations: safe file I/O, git command execution, source-file discovery, and cryptographic hashing of resolved paths. All functions operate against an absolute repository root that is resolved once per operation to prevent path-traversal vulnerabilities. Data flows consistently from a caller-supplied string through `filepath.Abs` resolution into the security layer before any filesystem or git interaction occurs.

---

## Filesystem Operations (`file_tools.go`)

### Safe Read / Write Interface

| Function | Direction | Responsibility |
|---|---|---|
| **ReadFileSafely** | Inbound | Resolves an absolute path via the security layer, then reads the repository-relative file and returns its byte content. |
| **WriteFileSafely** | Outbound | Accepts bytes + a repository-relative path, creates parent directories as needed, verifies the target is not a symlink before writing, and persists the data. |

```
ReadFileSafely(path string) ([]byte, error)

WriteFileSafely(relPath string, content []byte) error
```

Both functions delegate to the security layer for absolute-path resolution. `WriteFileSafely` additionally enforces a symlink check on the target before any write operation to prevent privilege escalation through symlink injection.

### Source-File Discovery (`DiscoverCodeFiles`)

`DiscoverCodeFiles` recursively walks the repository root, collecting relative paths of source files while excluding:
- Build artifacts (compiled binaries, object files)
- Binary files detected via null-byte scanning
- Dependency directories (vendor, node_modules, etc.)
- User-configured ignore patterns

```
DiscoverCodeFiles(root string) ([]string, error)
```

The walk is bounded to the repository root. Each discovered candidate passes through `IsBinaryFile` before inclusion. The returned slice contains paths relative to `root`.

### Binary Detection (`IsBinaryFile`)

**IsBinaryFile** reads a file and inspects its first 1024 bytes for null byte presence. If any null byte is found, the function returns `true`, classifying the file as binary rather than text source material. This heuristic provides O(1) disk-read cost regardless of file size.

```
IsBinaryFile(path string) (bool, error)
```

---

## Git Operations (`git_tools.go`)

### Generic Git Execution (`RunGit`)

**RunGit** executes an arbitrary git subcommand within a specified repository root directory and returns the combined stdout/stderr output. It handles both success and failure cases without panicking on non-zero exit codes. The caller is responsible for interpreting return values.

```
RunGit(root string, args ...string) (string, error)
```

**GetGitHead** delegates to `RunGit` with the subcommand `rev-parse HEAD`, returning the current HEAD commit hash:

```
GetGitHead(root string) (string, error)
```

Both functions operate against a resolved absolute path. The git process inherits the repository root as its working directory; stdout/stderr are captured and returned verbatim.

---

## Path Hashing (`registry.go`)

### SHA-256 of Resolved Root (`HashRepoRoot`)

**HashRepoRoot** returns a SHA-256 hex-encoded hash of the resolved absolute path passed as argument. If `filepath.Abs` fails during resolution, it falls back to returning the raw input string unchanged:

```
HashRepoRoot(path string) (string, error)
```

This function is used for deterministic identification of repository states across operations that require a content-addressable key derived from the root location.