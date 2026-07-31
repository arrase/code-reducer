package security

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	LockFileName    = ".code-reducer.lock"
	defaultFilePerm = 0644
)

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
	parts := append([]string{resolvedAncestor}, nonExistentSuffix...)
	resolvedPath := filepath.Join(parts...)

	// Verify that resolvedPath is inside resolvedRoot
	rel, err := filepath.Rel(resolvedRoot, resolvedPath)
	if err != nil || rel == ".." || strings.HasPrefix(rel, fmt.Sprintf("..%c", filepath.Separator)) {
		return "", fmt.Errorf("%w: %q", ErrPathTraversal, inputPath)
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
	err = l.file.Close()
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

	f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, defaultFilePerm)
	if err != nil {
		if os.IsExist(err) {
			return nil, fmt.Errorf("%w: %s. If you are sure no other code-reducer process is running, delete this stale lockfile manually", ErrLockHeld, lockPath)
		}
		return nil, fmt.Errorf("failed to acquire lock at %s: %w", lockPath, err)
	}

	if _, err := f.Write([]byte(fmt.Sprintf("%d\n", os.Getpid()))); err != nil {
		errClose := f.Close()
		errRemove := os.Remove(lockPath)
		var errMsg string
		if errClose != nil {
			errMsg += fmt.Sprintf(", close failed: %v", errClose)
		}
		if errRemove != nil {
			errMsg += fmt.Sprintf(", remove failed: %v", errRemove)
		}
		return nil, fmt.Errorf("failed to write pid to lockfile%s: %w", errMsg, err)
	}

	return &SimpleLock{lockPath: lockPath, file: f}, nil
}
