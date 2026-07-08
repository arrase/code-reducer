package security

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/gofrs/flock"
)

const LockFileName = ".code-reducer.lock"

// SafeResolve cleans the input path and ensures it lies strictly inside the repository.
// It mitigates TOCTOU vulnerabilities for non-existent files by evaluating symlinks on the parent dir.
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

	// For creating new files safely, we check the closest existing directory in the path
	// to prevent writing into malicious symlinked folders before creation.
	dirToCheck := resolved
	for {
		if _, err := os.Stat(dirToCheck); err == nil {
			break
		}
		parent := filepath.Dir(dirToCheck)
		if parent == dirToCheck {
			break
		}
		dirToCheck = parent
	}

	realPath, err := filepath.EvalSymlinks(dirToCheck)
	if err == nil {
		realRel, err := filepath.Rel(absRoot, realPath)
		if err != nil || strings.HasPrefix(realRel, "..") {
			return "", fmt.Errorf("security violation: symlink points outside repository: %q", realPath)
		}
	}

	return resolved, nil
}

// AcquireLock acquires a flock file lock in the repoRoot.
// It uses a shared lock (RLock) if exclusive is false, and an exclusive lock (Lock) if exclusive is true.
func AcquireLock(repoRoot string, exclusive bool) (*flock.Flock, error) {
	lockPath := filepath.Join(repoRoot, LockFileName)
	fileLock := flock.New(lockPath)

	var locked bool
	var err error

	if exclusive {
		locked, err = fileLock.TryLock()
	} else {
		locked, err = fileLock.TryRLock()
	}

	if err != nil {
		return nil, fmt.Errorf("failed to acquire lock at %s: %w", lockPath, err)
	}
	if !locked {
		return nil, fmt.Errorf("lock at %s is already held by another process", lockPath)
	}

	// If it is an exclusive lock, write the current PID to the lockfile (ignore errors)
	if exclusive {
		_ = os.WriteFile(lockPath, []byte(fmt.Sprintf("%d\n", os.Getpid())), 0644)
	}

	return fileLock, nil
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
	defer file.Close()

	scanner := bufio.NewScanner(file)
	found := false
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == LockFileName {
			found = true
			break
		}
	}

	if err := scanner.Err(); err != nil {
		return fmt.Errorf("error reading .gitignore: %w", err)
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
