package security

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const LockFileName = ".code-reducer.lock"

// SafeResolve cleans the input path and ensures it lies strictly inside the repository.
func SafeResolve(repoRoot, inputPath string) (string, error) {
	absRoot, err := filepath.Abs(repoRoot)
	if err != nil {
		return "", fmt.Errorf("failed to get absolute root path: %w", err)
	}

	resolved := filepath.Clean(filepath.Join(absRoot, inputPath))
	rel, err := filepath.Rel(absRoot, resolved)
	if err != nil || strings.HasPrefix(rel, "..") {
		return "", fmt.Errorf("security violation: path traversal detected: %q", inputPath)
	}

	return resolved, nil
}

type SimpleLock struct {
	lockPath string
	file     *os.File
}

// Unlock releases the lock by closing the file and removing it.
func (l *SimpleLock) Unlock() error {
	var err1, err2 error
	if l.file != nil {
		err1 = l.file.Close()
	}
	err2 = os.Remove(l.lockPath)
	if err1 != nil {
		return err1
	}
	return err2
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

	// If .gitignore doesn't exist, create it with the lockfile
	if _, err := os.Stat(gitignorePath); os.IsNotExist(err) {
		return os.WriteFile(gitignorePath, []byte(LockFileName+"\n"), 0644)
	}

	file, err := os.Open(gitignorePath)
	if err != nil {
		return fmt.Errorf("failed to open .gitignore: %w", err)
	}

	scanner := bufio.NewScanner(file)
	found := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == LockFileName {
			found = true
			break
		}
	}

	scanErr := scanner.Err()
	file.Close()

	if scanErr != nil {
		return fmt.Errorf("error reading .gitignore: %w", scanErr)
	}

	if !found {
		// Append LockFileName to .gitignore
		f, err := os.OpenFile(gitignorePath, os.O_APPEND|os.O_WRONLY, 0644)
		if err != nil {
			return fmt.Errorf("failed to open .gitignore for appending: %w", err)
		}
		defer f.Close()

		if _, err := f.WriteString("\n# Code-Reducer Lockfile\n" + LockFileName + "\n"); err != nil {
			return fmt.Errorf("failed to write to .gitignore: %w", err)
		}
	}

	return nil
}
