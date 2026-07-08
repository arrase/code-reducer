package tools

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arrase/code-reducer/internal/security"
)

// ReadFileSafely resolves the virtual path inside the repository and reads the file content.
func ReadFileSafely(repoRoot, virtualPath string) ([]byte, error) {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return nil, err
	}

	content, err := os.ReadFile(safePath)
	if err != nil {
		return nil, fmt.Errorf("failed to read file: %w", err)
	}

	return content, nil
}

// WriteFileSafely resolves the virtual path inside the repository and writes content.
// It ensures that directories are created and uses a TOCTOU-safe write pattern.
func WriteFileSafely(repoRoot, virtualPath string, content []byte) error {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return err
	}

	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Open the file without truncating first to prevent truncating a followed symlink target
	f, err := os.OpenFile(safePath, os.O_WRONLY|os.O_CREATE, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %w", err)
	}
	defer f.Close()

	// TOCTOU mitigation: verify the file path is not a symlink
	fiLstat, err := os.Lstat(safePath)
	if err != nil {
		return fmt.Errorf("failed to lstat file: %w", err)
	}
	if fiLstat.Mode()&os.ModeSymlink != 0 {
		return fmt.Errorf("security violation: symlink detected on write: %s", safePath)
	}

	fiFstat, err := f.Stat()
	if err != nil {
		return fmt.Errorf("failed to fstat file: %w", err)
	}

	// Verify the file descriptor points to the exact same file path check
	if !os.SameFile(fiLstat, fiFstat) {
		return fmt.Errorf("security violation: TOCTOU symlink race detected on write: %s", safePath)
	}

	// Truncate safely only after verifying it is not a symlink
	if err := f.Truncate(0); err != nil {
		return fmt.Errorf("failed to truncate file: %w", err)
	}

	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("failed to write file content: %w", err)
	}

	return nil
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
func ShouldIgnoreFile(repoRoot, relPath string, customIgnores []string, ignoredExtensions []string) bool {
	// 1. Check user-defined ignores (config + gitignore)
	if ShouldIgnorePath(relPath, customIgnores) {
		return true
	}

	// 2. Check path components for dot-prefixed items or .egg-info
	components := strings.Split(relPath, string(filepath.Separator))
	for _, comp := range components {
		if strings.HasPrefix(comp, ".") || strings.HasSuffix(comp, ".egg-info") {
			return true
		}
	}

	// 3. Check filename extensions & suffixes
	name := filepath.Base(relPath)
	ext := strings.ToLower(filepath.Ext(name))
	for _, iext := range ignoredExtensions {
		normIext := iext
		if !strings.HasPrefix(normIext, ".") {
			normIext = "." + normIext
		}
		if ext == strings.ToLower(normIext) {
			return true
		}
	}
	if NameSuffixIgnored(name) {
		return true
	}

	// 4. Check if it's a binary file
	absPath, err := security.SafeResolve(repoRoot, relPath)
	if err != nil {
		return true // Treat unsafe paths outside repo root as ignored
	}
	return IsBinaryFile(absPath)
}

// DiscoverCodeFiles recursively walks the codebase to find high-signal source files.
// It ignores build, dependency, and output files, as well as any paths in the custom ignores list.
func DiscoverCodeFiles(repoRoot string, ignores []string, ignoredExtensions []string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip items with errors
		}

		if path == repoRoot {
			return nil
		}

		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			return nil
		}

		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".egg-info") || ShouldIgnorePath(rel, ignores) {
				return filepath.SkipDir
			}
			return nil
		}

		if ShouldIgnoreFile(repoRoot, rel, ignores, ignoredExtensions) {
			return nil
		}

		files = append(files, rel)
		return nil
	})

	return files, err
}

func ShouldIgnorePath(relPath string, ignores []string) bool {
	relClean := filepath.Clean(relPath)
	for _, pattern := range ignores {
		patternClean := filepath.Clean(filepath.FromSlash(strings.TrimPrefix(pattern, "/")))
		if relClean == patternClean {
			return true
		}
		prefix := patternClean + string(filepath.Separator)
		if strings.HasPrefix(relClean, prefix) {
			return true
		}
		components := strings.Split(relClean, string(filepath.Separator))
		for _, comp := range components {
			if comp == patternClean {
				return true
			}
			if strings.ContainsAny(patternClean, "*?[]") {
				if matched, _ := filepath.Match(patternClean, comp); matched {
					return true
				}
			}
		}
		if strings.ContainsAny(patternClean, "*?[]") {
			if matched, _ := filepath.Match(patternClean, relClean); matched {
				return true
			}
		}
	}
	return false
}

// IsBinaryFile checks if a file is binary by scanning the first 1024 bytes for null bytes.
func IsBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	buf := make([]byte, 1024)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return false
	}

	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	return false
}

func NameSuffixIgnored(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "-lock.json") || strings.HasSuffix(lower, ".lock.yaml") || strings.HasSuffix(lower, "pnpm-lock.yaml")
}
