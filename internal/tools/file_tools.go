package tools

import (
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
// It ensures that directories are created.
func WriteFileSafely(repoRoot, virtualPath string, content []byte) error {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return err
	}

	dir := filepath.Dir(safePath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	// Open the file
	f, err := os.OpenFile(safePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open file for writing: %w", err)
	}
	defer f.Close()

	// TOCTOU mitigation: verify the file path is not a symlink after open
	fi, err := os.Lstat(safePath)
	if err == nil && (fi.Mode()&os.ModeSymlink != 0) {
		return fmt.Errorf("security violation: TOCTOU symlink detected on write: %s", safePath)
	}

	if _, err := f.Write(content); err != nil {
		return fmt.Errorf("failed to write file content: %w", err)
	}

	return nil
}

// DiscoverCodeFiles recursively walks the codebase to find high-signal source files.
// It ignores build, dependency, and output files.
func DiscoverCodeFiles(repoRoot string) ([]string, error) {
	var files []string
	ignoredDirs := map[string]bool{
		".git":             true,
		"node_modules":     true,
		"dist":             true,
		"build":            true,
		"cache":            true,
		"code-reducer":     true,
		".gemini":          true,
		"bower_components": true,
		"__pycache__":      true,
		".pytest_cache":    true,
		".mypy_cache":      true,
		".tox":             true,
		"venv":             true,
		".venv":            true,
	}

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return nil // Skip items with errors
		}

		if d.IsDir() {
			if path == repoRoot {
				return nil
			}
			name := d.Name()
			if ignoredDirs[name] || strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".egg-info") {
				return filepath.SkipDir
			}
			return nil
		}

		// Ignore binary/build/lock files
		ext := strings.ToLower(filepath.Ext(path))
		if ext == ".png" || ext == ".jpg" || ext == ".jpeg" || ext == ".gif" || ext == ".pdf" ||
			ext == ".exe" || ext == ".dll" || ext == ".so" || ext == ".o" || ext == ".a" ||
			ext == ".zip" || ext == ".gz" || ext == ".tar" || ext == ".lock" || ext == ".pyc" ||
			ext == ".pyo" || ext == ".pyd" || nameSuffixIgnored(d.Name()) {
			return nil
		}

		// Extra safety: Check if it's a binary file using null-byte detection
		if IsBinaryFile(path) {
			return nil
		}

		// Add relative path
		rel, err := filepath.Rel(repoRoot, path)
		if err == nil {
			files = append(files, rel)
		}
		return nil
	})

	return files, err
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

func nameSuffixIgnored(name string) bool {
	lower := strings.ToLower(name)
	return strings.HasSuffix(lower, "-lock.json") || strings.HasSuffix(lower, ".lock.yaml") || strings.HasSuffix(lower, "pnpm-lock.yaml")
}
