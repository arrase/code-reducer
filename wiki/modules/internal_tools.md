# internal/tools — Repository Utilities Module

## Overview

This module provides deterministic, safety-hardened utilities for repository introspection: safe file I/O with TOCTOU protection against symlink races, recursive source-file discovery that filters build artifacts/dependencies/cache/binary files, git command execution wrappers, and filesystem-root SHA-256 hashing. All exported functions accept a virtual path or repository root as input; internal helpers handle path normalization, pattern matching, and binary detection before the primary operation executes.

## Safe File I/O (`file_tools.go`)

### `ReadFileSafely`

Resolves a virtual (relative) path to an absolute filesystem location under sandboxed conditions and reads its content into memory. Returns `nil` on any failure — no partial results are exposed. The resolver prevents traversal beyond the repository boundary; the reader returns zero-length or empty bytes only when the target is absent rather than an error value, allowing callers a uniform success path for "file not present" scenarios.

### `WriteFileSally`

Writes content to the resolved absolute path with TOCTOU-safe checks: before opening the file descriptor, it verifies that no symlink race can intercept the write by checking that the target inode is unchanged between resolution and open. If the destination is a symlink, truncation of the followed target is prevented — the function detects the symlink state pre-write and refuses to truncate the underlying real file.

### Data Flow

```
virtual_path → resolve_absolute → TOCTOU check (symlink race guard) → open O_RDWR|O_CREATE|O_TRUNC → write content → close
```

## File Discovery (`file_tools.go`)

### `DiscoverCodeFiles`

Recursively walks the repository root directory tree to collect relative paths of source files. Filters exclude: build artifacts, dependency directories, cache directories, binary files, and user-supplied ignore patterns. Returns a sorted slice of cleaned relative paths for deterministic iteration across platforms.

### `ShouldIgnorePath` (internal)

Determines whether a cleaned relative path matches any entry in the provided ignore list using four match modes: exact string equality, prefix matching, component-level substring matching (any single path separator-delimited segment), and glob pattern matching (`filepath.Match`). Returns `true` on first match.

### `IsBinaryFile` (internal)

Reads up to 1024 bytes from a file handle; returns `true` if any byte in the prefix contains a null byte (`\x00`), indicating a binary rather than text source file. Used as an exclusion predicate during discovery walks.

### `NameSuffixIgnored` (internal)

Returns `true` when the final path component ends with known lockfile suffixes: `.lock.json`, `.lock.yaml`, `pnpm-lock.yaml`. Prevents lockfile entries from appearing in discovered code sets.

## Git Integration (`git_tools.go`)

### `RunGit`

Executes a specified git command (as an argument slice) in the provided repository root with `os/exec.CommandContext`-style error propagation. Returns combined stdout/stderr as a single string and any non-zero exit status mapped to an `error`. The process is spawned with inherited environment variables; working directory is explicitly set to the supplied root, preventing drift from parent processes.

### `GetGitHead`

Returns the current HEAD commit hash for the repository rooted at the provided directory by delegating to `RunGit` with arguments `[git rev-parse HEAD]`. Returns an empty string on error rather than a partial hash.

### `VerifyGitRepo`

Confirms two preconditions: (1) that the git executable exists on the system path, and (2) that the supplied directory is inside a valid git working tree (`git rev-parse --is-inside-work-tree`). Returns an error describing which precondition failed if either check does not pass. Used as an admission gate before invoking other git-dependent operations.

## Content Hashing (`registry.go`)

### `HashRepoRoot` (exported)

Computes a SHA-256 hash of the resolved absolute filesystem path of the repository root and returns its hexadecimal representation as a string. The hash is deterministic for a given commit state — identical file contents produce identical hashes regardless of walk order, symlink state, or metadata differences. Used to fingerprint repository snapshots without requiring full tree traversal.