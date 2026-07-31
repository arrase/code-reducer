package tools

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/arrase/code-reducer/internal/security"
	ignore "github.com/sabhiram/go-gitignore"
)

const (
	defaultDirPerm  = 0755
	defaultFilePerm = 0644
)

// ReadFileSafely resolves the virtual path inside the repository and reads the file content.
func ReadFileSafely(repoRoot, virtualPath string) ([]byte, error) {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(safePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file content: %w", err)
	}

	return content, nil
}

// WriteFileAtomic writes data to a file atomically by first writing to a temporary file
// and then renaming it to the target path.
func WriteFileAtomic(targetPath string, data []byte, perm os.FileMode) error {
	dir := filepath.Dir(targetPath)
	if err := os.MkdirAll(dir, defaultDirPerm); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

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

	if err := os.Rename(tmpName, targetPath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
	}

	success = true
	return nil
}

// WriteFileSafely resolves the virtual path inside the repository and writes content.
// It ensures that directories are created and uses a TOCTOU-safe write pattern.
func WriteFileSafely(repoRoot, virtualPath string, content []byte) error {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return err
	}
	return WriteFileAtomic(safePath, content, defaultFilePerm)
}

// EnsureGitignoreHasLockfile ensures that the lockfile .code-reducer.lock is in the .gitignore.
func EnsureGitignoreHasLockfile(repoRoot string) error {
	gitignorePath, err := security.SafeResolve(repoRoot, ".gitignore")
	if err != nil {
		return err
	}

	data, err := os.ReadFile(gitignorePath)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("error reading .gitignore: %w", err)
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.TrimSpace(line) == security.LockFileName {
			return nil
		}
	}

	contentToAppend := "# Code-Reducer Lockfile\n" + security.LockFileName + "\n"
	if len(data) > 0 && data[len(data)-1] != '\n' {
		contentToAppend = "\n" + contentToAppend
	}

	newData := append(data, []byte(contentToAppend)...)

	return WriteFileAtomic(gitignorePath, newData, defaultFilePerm)
}

// LoadGitignore reads the .gitignore file from repoRoot and returns its active ignore patterns.
func LoadGitignore(repoRoot string) ([]string, error) {
	gitignorePath := filepath.Join(repoRoot, ".gitignore")
	file, err := os.Open(gitignorePath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No .gitignore file is not an error
		}
		return nil, err
	}
	defer file.Close()

	var patterns []string
	scanner := bufio.NewScanner(file)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, scanner.Err()
}

// ShouldIgnoreFile checks if a file (specified by relative path) is ignored.
func ShouldIgnoreFile(relPath string, gitIgnore *ignore.GitIgnore) bool {
	slashRelPath := filepath.ToSlash(relPath)

	// 1. Check user-defined ignores (config + gitignore)
	if gitIgnore != nil && gitIgnore.MatchesPath(slashRelPath) {
		return true
	}

	// 2. Check path components for dot-prefixed items or .egg-info
	components := strings.Split(slashRelPath, "/")
	for _, comp := range components {
		if strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".egg-info") {
			return true
		}
	}

	return false
}

// DiscoverCodeFiles recursively walks the codebase to find high-signal source files.
// It ignores build, dependency, and output files, as well as any paths in the custom ignores list.
func DiscoverCodeFiles(repoRoot string, ignores []string) ([]string, error) {
	var files []string
	gitIgnore := ignore.CompileIgnoreLines(ignores...)

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("error walking path %s: %w", path, err)
		}

		if path == repoRoot {
			return nil
		}

		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return fmt.Errorf("failed to get relative path for %s: %w", path, err)
		}

		slashRel := filepath.ToSlash(rel)

		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".egg-info") || (gitIgnore != nil && gitIgnore.MatchesPath(slashRel)) {
				return filepath.SkipDir
			}
			return nil
		}

		if ShouldIgnoreFile(slashRel, gitIgnore) {
			return nil
		}

		files = append(files, slashRel)
		return nil
	})

	return files, err
}
