package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const LockFileName = ".code-reducer.lock"

// SafeResolve cleans the input path and ensures it lies strictly inside the repository.
// It resolves symlinks on the existing ancestor parts to prevent path traversal via symlinks.
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
	resolvedPath := resolvedAncestor
	if len(nonExistentSuffix) > 0 {
		resolvedPath = filepath.Join(append([]string{resolvedAncestor}, nonExistentSuffix...)...)
	}
	resolvedPath = filepath.Clean(resolvedPath)

	// Verify that resolvedPath is inside resolvedRoot
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("security violation: path traversal detected: %q", inputPath)
	}

	return resolvedPath, nil
}

type SimpleLock struct {
	lockPath string
	file     *os.File
	mu       sync.Mutex
	closed   bool
}

// Unlock releases the lock by closing the file and removing it.
// It is idempotent and thread-safe.
func (l *SimpleLock) Unlock() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		return nil
	}
	l.closed = true

	var err error
	if l.file != nil {
		err = l.file.Close()
		l.file = nil
	}
	if removeErr := os.Remove(l.lockPath); removeErr != nil && !os.IsNotExist(removeErr) {
		if err == nil {
			err = removeErr
		}
	}
	return err
}

// AcquireLock acquires a simple file lock in the repoRoot.
// It uses O_EXCL to ensure atomicity.
func AcquireLock(repoRoot string) (*SimpleLock, error) {
	lockPath, err := SafeResolve(repoRoot, LockFileName)
	if err != nil {
		return nil, err
	}

	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0644)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("lock at %s is already held by another process. If you are sure no other code-reducer process is running, delete this stale lockfile manually", lockPath)
		}
		return nil, fmt.Errorf("failed to acquire lock at %s: %w", lockPath, err)
	}

	if _, err := f.Write([]byte(fmt.Sprintf("%d\n", os.Getpid()))); err != nil {
		f.Close()
		os.Remove(lockPath)
		return nil, fmt.Errorf("failed to write pid to lockfile: %w", err)
	}

	return &SimpleLock{lockPath: lockPath, file: f}, nil
}

// EnsureGitignoreHasLockfile ensures that the lockfile .code-reducer.lock is in the .gitignore.
func EnsureGitignoreHasLockfile(repoRoot string) error {
	gitignorePath := filepath.Join(repoRoot, ".gitignore")

	data, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error reading .gitignore: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == LockFileName {
			return nil
		}
	}

	f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open .gitignore for appending: %w", err)
	}
	defer f.Close()

	if _, err := f.WriteString("\n# Code-Reducer Lockfile\n" + LockFileName + "\n"); err != nil {
		return fmt.Errorf("failed to write to .gitignore: %w", err)
	}

	return nil
}
