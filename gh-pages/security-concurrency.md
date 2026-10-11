# Security Sandbox & Concurrency Controls

Code-Reducer is designed to operate safely inside enterprise environments, multi-user systems, and automated CI/CD pipelines.

The internal `security` and `tools` packages enforce strict filesystem boundaries, serialize process execution, and neutralize symlink hijacking and Time-of-Check to Time-of-Use (TOCTOU) vulnerability vectors.

---

## 🛡️ Path Traversal Guard (`security.SafeResolve`)

Every filesystem interaction targeting workspace resources must resolve through `security.SafeResolve`. This prevents malicious paths, broken symlinks, or relative traversal attempts (e.g. `../../etc/passwd`) from accessing files outside the repository root.

```
Input Virtual Path: "cmd/../../etc/passwd"
                        |
                        v
    +---------------------------------------+
    | 1. Resolve Absolute Repo Root Path    |
    |    filepath.Abs & EvalSymlinks        |
    +---------------------------------------+
                        |
                        v
    +---------------------------------------+
    | 2. Bottom-Up Ancestor Traversal Loop  |
    |    Find closest existing directory    |
    +---------------------------------------+
                        |
                        v
    +---------------------------------------+
    | 3. Evaluate Symlinks on Ancestor      |
    |    EvalSymlinks(currentAncestor)      |
    +---------------------------------------+
                        |
                        v
    +---------------------------------------+
    | 4. Reconstruct & Rel Path Verification|
    |    filepath.Rel(resolvedRoot, path)   |
    +---------------------------------------+
                        |
            +-----------+-----------+
            |                       |
      Path Escapes            Path Internal
            |                       |
            v                       v
     [Return Error]         [Return Safe Path]
    ErrPathTraversal
```

### Bottom-Up Symlink Evaluation Algorithm
To defend against symlinks that point outside the repository root—even when target files or subdirectories do not yet exist on disk—`SafeResolve` evaluates symlinks bottom-up across physically existing ancestor directories:

```go
func SafeResolve(repoRoot, inputPath string) (string, error) {
    absRoot, err := filepath.Abs(repoRoot)
    if err != nil {
        return "", fmt.Errorf("failed to get absolute root path: %w", err)
    }
    resolvedRoot, err := filepath.EvalSymlinks(absRoot)
    if err != nil {
        return "", fmt.Errorf("failed to resolve symlinks for root path: %w", err)
    }

    target := filepath.Clean(filepath.Join(resolvedRoot, inputPath))
    current := target
    var nonExistentSuffix []string

    // Find the closest physically existing ancestor
    for {
        _, err := os.Lstat(current)
        if err == nil {
            break
        }
        if !os.IsNotExist(err) {
            return "", fmt.Errorf("failed to stat path component: %w", err)
        }
        parent := filepath.Dir(current)
        if parent == current {
            break
        }
        nonExistentSuffix = append([]string{filepath.Base(current)}, nonExistentSuffix...)
        current = parent
    }

    // Evaluate symlinks on the existing ancestor path
    resolvedAncestor, err := filepath.EvalSymlinks(current)
    if err != nil {
        return "", fmt.Errorf("failed to resolve symlinks for ancestor path: %w", err)
    }

    // Rebuild the resolved full path
    parts := append([]string{resolvedAncestor}, nonExistentSuffix...)
    resolvedPath := filepath.Join(parts...)

    // Verify that resolvedPath is inside resolvedRoot
    rel, err := filepath.Rel(resolvedRoot, resolvedPath)
    if err != nil || rel == ".." || strings.HasPrefix(rel, fmt.Sprintf("..%c", filepath.Separator)) {
        return "", fmt.Errorf("%w: %q", ErrPathTraversal, inputPath)
    }

    return resolvedPath, nil
}
```

---

## 🔒 Atomic Process Locking (`security.AcquireLock`)

To prevent race conditions, corrupted metadata caches, or duplicate LLM inference calls when multiple commands run concurrently, Code-Reducer acquires an exclusive process lockfile (`.code-reducer.lock`) during initialization.

```go
func AcquireLock(repoRoot string) (*SimpleLock, error) {
    lockPath, err := SafeResolve(repoRoot, LockFileName)
    if err != nil {
        return nil, err
    }

    f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, defaultFilePerm)
    if err != nil {
        if os.IsExist(err) {
            return nil, fmt.Errorf("%w: %s. If you are sure no other code-reducer process is running, delete this stale lockfile manually", ErrLockHeld, lockPath)
        }
        return nil, fmt.Errorf("failed to acquire lock at %s: %w", lockPath, err)
    }

    if _, err := f.Write([]byte(fmt.Sprintf("%d\n", os.Getpid()))); err != nil {
        // cleanup on error...
    }

    return &SimpleLock{lockPath: lockPath, file: f}, nil
}
```

### Key Security Properties:
1. **OS-Level Atomicity (`os.O_EXCL`)**: The `O_CREATE | O_EXCL` flags force atomic creation at the kernel level. If `.code-reducer.lock` already exists, execution fails fast immediately without entering a race condition.
2. **Process ID Recording**: Records the active Process ID (PID) into `.code-reducer.lock` to simplify debugging and lock attribution.
3. **Automatic Git Isolation (`EnsureGitignoreHasLockfile`)**: On startup, Code-Reducer inspects `.gitignore` and automatically appends `.code-reducer.lock` if missing, preventing accidental lockfile commits to Git repositories.
4. **Thread-Safe & Idempotent Release**: `SimpleLock.Unlock()` uses a `sync.Mutex` to safely close the open file handle and remove `.code-reducer.lock` upon pipeline teardown.

---

## ⚡ TOCTOU Symlink Hijacking & Safe File I/O

A Time-of-Check to Time-of-Use (TOCTOU) vulnerability occurs if an attacker replaces a target file path with a symlink between path validation and file writing. Code-Reducer mitigates this risk by combining `SafeResolve` with atomic temporary file writes (`WriteFileAtomic`).

### Atomic Safe Writing Pattern (`WriteFileSafely`)

```go
func WriteFileAtomic(targetPath string, data []byte, perm os.FileMode) error {
    dir := filepath.Dir(targetPath)
    if err := os.MkdirAll(dir, defaultDirPerm); err != nil {
        return fmt.Errorf("failed to create directory: %w", err)
    }

    // 1. Create temporary file in target directory
    tmpFile, err := os.CreateTemp(dir, filepath.Base(targetPath)+".tmp.*")
    if err != nil {
        return fmt.Errorf("failed to create temp file: %w", err)
    }
    tmpName := tmpFile.Name()

    var success bool
    defer func() {
        if !success {
            tmpFile.Close()
            os.Remove(tmpName)
        }
    }()

    // 2. Write payload and flush buffers to disk hardware
    if _, err := tmpFile.Write(data); err != nil {
        return fmt.Errorf("failed to write to temp file: %w", err)
    }
    if err := tmpFile.Sync(); err != nil {
        return fmt.Errorf("failed to sync temp file: %w", err)
    }
    if err := tmpFile.Close(); err != nil {
        return fmt.Errorf("failed to close temp file: %w", err)
    }
    if err := os.Chmod(tmpName, perm); err != nil {
        return fmt.Errorf("failed to chmod temp file: %w", err)
    }

    // 3. Atomically replace target path via OS syscall
    if err := os.Rename(tmpName, targetPath); err != nil {
        return fmt.Errorf("failed to rename temp file: %w", err)
    }

    success = true
    return nil
}
```

### Defense Guarantee
1. **Isolated Writes**: Files are written to uniquely named hidden temporary files (`.tmp.*`).
2. **Buffer Flush (`Sync`)**: Data is synced to storage media prior to renaming, preventing zero-byte corruption on unexpected crashes or power loss.
3. **Atomic Rename (`os.Rename`)**: Replaces the destination file path atomically in a single OS kernel operation, eliminating partially written files and preventing symlink swaps.
