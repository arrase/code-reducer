# `internal/tools` — Repository Metadata & File System Utilities

## Responsibility and Data Flow

This package provides low-level primitives for resolving repository state: filesystem reads/writes with security checks, recursive source-file discovery, git command execution, and deterministic path hashing. All functions operate on a virtualized (logical) root that maps to an absolute disk path at resolution time. The design is intentionally TOCTOU-safe where write paths are involved—`WriteFileSafely` verifies the resolved target is not a symlink before truncating.

Data flow: `HashRepoRoot` hashes a logical path string; `ReadFileSafely` / `WriteFileSafely` resolve virtual paths to absolute filesystem locations, then read/write raw bytes with error propagation on failure; `DiscoverCodeFiles` walks the root recursively and filters by exclusion rules; git operations execute in-process via `RunGit`, which returns combined stdout/stderr or an error message.

---

## File System Utilities (`file_tools.go`)

### Path Resolution and Safe Read/Write

- **ReadFileSafely** — Resolves a virtual path through the repository root (absolute filesystem resolution), reads raw file content, and returns `[]byte` + error on resolution/read failure.
- **WriteFileSafely** — Writes bytes to a virtual path with TOCTOU-safe checks: verifies the resolved target is not a symlink before truncating and writing.

### Source Discovery and Filtering

- **DiscoverCodeFiles** — Recursively walks the repository root, collecting relative paths of source files while excluding build artifacts, binary files, lock files, and custom ignore patterns.
- **ShouldIgnorePath** — Evaluates whether a given relative file path matches any pattern in the provided `ignores` slice using four matching strategies: exact match, prefix matching, component-level matching, and glob-style wildcard matching (`*`, `?`, `[...]`).
- **IsBinaryFile** — Opens a file and scans its first 1024 bytes for null byte (`0x00`) characters; returns true if any detected.
- **NameSuffixIgnored** — Returns true when the given filename ends with common lock/configuration suffixes: `-lock.json`, `.lock.yaml`, `pnpm-lock.yaml`.

---

## Git Operations (`git_tools.go`)

### Command Execution and Repository Validation

- **RunGit** — Executes a git command in the specified repository directory with the `--no-pager` flag; returns combined output or an error message.
- **GetGitHead** — Retrieves the current HEAD commit hash by delegating to `RunGit`.
- **VerifyGitRepo** — Validates that the system has a git executable available and confirms the target directory is inside a valid git working tree.

---

## Path Hashing (`registry.go`)

### Deterministic Identifier Computation

- **HashRepoRoot** — Computes and returns a hex-encoded SHA-256 hash of the provided path string after attempting to resolve it to an absolute filesystem path.