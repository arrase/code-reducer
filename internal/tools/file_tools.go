package tools

import (
	"bytes"
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

var (
	skippedFileNames = map[string]bool{
		"go.sum":                   true,
		"package-lock.json":        true,
		"pnpm-lock.yaml":           true,
		"mkdocs.yml":               true,
		"mkdocs.yaml":              true,
		"sonar-project.properties": true,
		"LICENSE":                  true,
		"Makefile":                 true,
		"Dockerfile":               true,
	}

	skippedExtensions = map[string]bool{
		".lock":  true,
		".md":    true,
		".txt":   true,
		".svg":   true,
		".png":   true,
		".jpg":   true,
		".jpeg":  true,
		".gif":   true,
		".ico":   true,
		".pdf":   true,
		".bin":   true,
		".exe":   true,
		".so":    true,
		".dylib": true,
		".dll":   true,
		".wasm":  true,
		".pyc":   true,
	}

	skippedTestSuffixes = []string{
		"_test.go",
		".test.js",
		".test.ts",
		".test.tsx",
		".spec.js",
		".spec.ts",
		".spec.tsx",
		"_test.py",
	}
)

// FileExistsSafely resolves the virtual path inside the repository and checks if the file exists.
func FileExistsSafely(repoRoot, virtualPath string) bool {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return false
	}
	_, err = os.Stat(safePath)
	return err == nil
}

// RemoveFileSafely resolves the virtual path inside the repository and removes the file, ignoring if it does not exist.
func RemoveFileSafely(repoRoot, virtualPath string) error {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return err
	}
	if err := os.Remove(safePath); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// ReadFileSafely resolves the virtual path inside the repository and reads the file content.
func ReadFileSafely(repoRoot, virtualPath string) ([]byte, error) {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return nil, err
	}
	return os.ReadFile(safePath)
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

	data = append(data, []byte(contentToAppend)...)

	return WriteFileAtomic(gitignorePath, data, defaultFilePerm)
}

// LoadGitignore reads the .gitignore file from repoRoot and returns its active ignore patterns.
func LoadGitignore(repoRoot string) ([]string, error) {
	data, err := ReadFileSafely(repoRoot, ".gitignore")
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}

	var patterns []string
	for _, rawLine := range strings.Split(string(data), "\n") {
		line := strings.TrimSpace(rawLine)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		patterns = append(patterns, line)
	}
	return patterns, nil
}

// ShouldIgnoreFile checks if a file (specified by relative path) is ignored.
func ShouldIgnoreFile(relPath string, gitIgnore *ignore.GitIgnore) bool {
	slashRelPath := filepath.ToSlash(relPath)

	// 1. Check user-defined ignores (config + gitignore)
	if gitIgnore.MatchesPath(slashRelPath) {
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

func shouldSkipCodeFile(name string) bool {
	if skippedFileNames[name] || skippedExtensions[strings.ToLower(filepath.Ext(name))] {
		return true
	}
	if strings.HasPrefix(name, "test_") && strings.HasSuffix(name, ".py") {
		return true
	}
	for _, suffix := range skippedTestSuffixes {
		if strings.HasSuffix(name, suffix) {
			return true
		}
	}
	return false
}

func isBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()

	var buf [512]byte
	n, _ := f.Read(buf[:])
	return bytes.IndexByte(buf[:n], 0) != -1
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
			if ShouldIgnoreFile(slashRel, gitIgnore) {
				return filepath.SkipDir
			}
			return nil
		}

		if !d.Type().IsRegular() || ShouldIgnoreFile(slashRel, gitIgnore) || shouldSkipCodeFile(d.Name()) || isBinaryFile(path) {
			return nil
		}

		files = append(files, slashRel)
		return nil
	})

	return files, err
}
