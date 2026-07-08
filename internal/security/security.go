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

// resolveSymlinksRecursively resolves symbolic links recursively up to a max depth.
// It resolves broken symlinks as well, returning their target path.
func resolveSymlinksRecursively(path string, depth int) (string, error) {
	if depth > 32 {
		return "", fmt.Errorf("too many levels of symbolic links")
	}
	fi, err := os.Lstat(path)
	if err != nil {
		// If it does not exist, it is resolved as far as possible
		return path, nil
	}
	if fi.Mode()&os.ModeSymlink != 0 {
		target, err := os.Readlink(path)
		if err != nil {
			return "", err
		}
		if !filepath.IsAbs(target) {
			target = filepath.Join(filepath.Dir(path), target)
		}
		return resolveSymlinksRecursively(filepath.Clean(target), depth+1)
	}
	return path, nil
}

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
		if _, err := os.Lstat(dirToCheck); err == nil {
			break
		}
		parent := filepath.Dir(dirToCheck)
		if parent == dirToCheck {
			break
		}
		dirToCheck = parent
	}

	realPath, err := resolveSymlinksRecursively(dirToCheck, 0)
	if err != nil {
		return "", fmt.Errorf("failed to resolve symlinks: %w", err)
	}

	realRel, err := filepath.Rel(absRoot, realPath)
	if err != nil || strings.HasPrefix(realRel, "..") {
		return "", fmt.Errorf("security violation: symlink points outside repository: %q", realPath)
	}

	return resolved, nil
}

// AcquireLock acquires a flock file lock in the repoRoot.
// It uses a shared lock (RLock) if exclusive is false, and an exclusive lock (Lock) if exclusive is true.
func AcquireLock(repoRoot string, exclusive bool) (*flock.Flock, error) {
	lockPath, err := SafeResolve(repoRoot, LockFileName)
	if err != nil {
		return nil, err
	}
	fileLock := flock.New(lockPath)

	var locked bool

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

	// If it is an exclusive lock, write the current PID to the lockfile safely
	if exclusive {
		// Use a TOCTOU-safe write to prevent symlink hijack of the lockfile
		f, err := os.OpenFile(lockPath, os.O_WRONLY|os.O_CREATE, 0644)
		if err != nil {
			_ = fileLock.Unlock()
			return nil, fmt.Errorf("failed to open lockfile for writing: %w", err)
		}
		defer f.Close()

		fiLstat, err := os.Lstat(lockPath)
		if err != nil {
			_ = fileLock.Unlock()
			return nil, fmt.Errorf("failed to lstat lockfile: %w", err)
		}
		if fiLstat.Mode()&os.ModeSymlink != 0 {
			_ = fileLock.Unlock()
			return nil, fmt.Errorf("security violation: symlink detected on lockfile: %s", lockPath)
		}

		fiFstat, err := f.Stat()
		if err != nil {
			_ = fileLock.Unlock()
			return nil, fmt.Errorf("failed to fstat lockfile: %w", err)
		}
		if !os.SameFile(fiLstat, fiFstat) {
			_ = fileLock.Unlock()
			return nil, fmt.Errorf("security violation: TOCTOU symlink race detected on lockfile: %s", lockPath)
		}

		if err := f.Truncate(0); err != nil {
			_ = fileLock.Unlock()
			return nil, fmt.Errorf("failed to truncate lockfile: %w", err)
		}

		if _, err := f.Write([]byte(fmt.Sprintf("%d\n", os.Getpid()))); err != nil {
			_ = fileLock.Unlock()
			return nil, fmt.Errorf("failed to write pid to lockfile: %w", err)
		}
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
