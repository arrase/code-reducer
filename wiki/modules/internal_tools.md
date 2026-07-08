# `internal/tools` — Module Architecture

## Responsibility Overview

The `internal/tools` package provides low-level infrastructure primitives for repository-aware code operations. It handles safe filesystem I/O, recursive source discovery with exclusion predicates, Git state queries, and deterministic path hashing. Functions are grouped by operational domain: file system access (read/write/discover/filter), Git interaction, and content integrity verification.

---

## File System Operations & Discovery

### Safe Read / Write Primitives

**`ReadFileSafely(path)`** resolves a virtual repository-relative path against the repository root without exposing the underlying filesystem directly. It returns raw file bytes (`[]byte`) or an error if path resolution fails (e.g., missing symlink target, permission denied) or reading encounters I/O failure.

**`WriteFileSafely(path, content []byte)`** writes `content` to a virtual repository-relative path with a TOCTOU-safe guard: before truncation, the resolved filesystem target is verified not to be a symlink. This prevents writing into attacker-controlled symlink targets (symlink injection / race condition).

### Source Discovery Pipeline

**`DiscoverCodeFiles(root string)`** recursively walks `root` collecting relative paths of source files. It applies exclusion predicates in sequence:
1. **Build artifacts** — detected via extension or known directories (`node_modules`, `.git`, `dist`, etc.)
2. **Binary files** — detected by `IsBinaryFile(path)` (first 1024 bytes scanned for `0x00` null bytes)
3. **Lock/config suffixes** — filtered via `NameSuffixIgnored(name)` matching `-lock.json`, `.lock.yaml`, `pnpm-lock.yaml`, etc.

### Path Filtering Predicates

Exclusion decisions are driven by two independent predicates:

- **`ShouldIgnorePath(rel string, ignores []string)`** evaluates whether a relative path matches any pattern in the provided ignore slice. It supports four matching modes:
  - Exact match (`path == pattern`)
  - Prefix match (`strings.HasPrefix(path, pattern)`)
  - Component-level match (any path component equals `pattern`)
  - Glob-style wildcard (`filepath.Match`)

- **`NameSuffixIgnored(name string)`** returns true if the filename ends with common lock/configuration suffixes. This is a fast-path shortcut for the glob matching layer; it does not inspect directory components—only the final filename.

### Data Flow Diagram

```
DiscoverCodeFiles(root) ──► recursive walk ──► relPath slice
    │                                                      │
    ▼                                                      ▼
ShouldIgnorePath(relPath, ignores)      IsBinaryFile(relPath)  NameSuffixIgnored(filename)
    │                                       │                        │
    ├── exclude (lock/config suffixes)──► binary detection ──► exclusion decision
    └── include (source file)          └── include (text file)
```

---

## Git Operations & Repository Validation

### `RunGit(repoDir string, args []string)`

Executes a git command with the `--no-pager` flag appended to suppress pager invocation in non-terminal contexts. Returns combined stdout+stderr as a single string or an error message if execution fails (nonzero exit code).

**Usage chain:**
- **`GetGitHead(repoDir string) (string, error)`** delegates to `RunGit(repoDir, []string{"rev-parse", "HEAD"})` and strips the leading newline/whitespace from output. Returns the 40-character commit SHA or an error if HEAD is detached/unavailable.

### Repository Validation

**`VerifyGitRepo(repoDir string) bool`** performs two checks:
1. Probes for a `git` executable on PATH (returns false if unavailable).
2. Runs `git rev-parse --is-inside-work-tree` and returns true only when the target directory is inside a valid git working tree.

---

## Content Integrity Hashing

### `HashRepoRoot(path string) (string, error)`

Computes a hex-encoded SHA-256 hash of `path`. It first attempts to resolve `path` to an absolute filesystem path via `filepath.Abs`; if resolution fails it returns the error directly. The resulting absolute path is then hashed in UTF-8 encoding and returned as lowercase hexadecimal. This function exists independently but integrates with file tools: a `DiscoverCodeFiles` pipeline can hash each retained source path for deterministic change detection or integrity verification.

---

## Module Integration Summary

| Function | Domain | Input | Output | Failure Mode |
|---|---|---|---|---|
| `ReadFileSafely` | FS I/O | virtual path | `[]byte`, error | resolution/IO failure |
| `WriteFileSafely` | FS I/O + TOCTOU | virtual path, content | error | symlink injection / IO failure |
| `DiscoverCodeFiles` | Discovery | root dir | `[]string` (rel paths) | walks entire tree; no early abort |
| `ShouldIgnorePath` | Filtering | rel path, patterns | `bool` | exact/prefix/component/glob match |
| `IsBinaryFile` | Content detection | file path | `bool` | reads ≤1024 bytes; error if unreadable |
| `NameSuffixIgnored` | Fast-path filter | filename | `bool` | no filesystem access |
| `RunGit` | Git shell | repo dir, args | output string / error | non-zero exit → error message |
| `GetGitHead` | Git state | repo dir | commit SHA / error | delegates RunGit; strips whitespace |
| `VerifyGitRepo` | Validation | repo dir | `bool` | false if git missing or invalid tree |
| `HashRepoRoot` | Integrity | path string | hex digest / error | returns error on abs resolution failure |