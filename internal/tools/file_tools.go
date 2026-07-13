package tools

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/arrase/code-reducer/internal/security"
	ignore "github.com/sabhiram/go-gitignore"
)

const (
	binaryDetectionBufSize = 1024
	defaultDirPerm         = 0755
	defaultFilePerm        = 0644
)

// ReadFileSafely resolves the virtual path inside the repository and reads the file content.
// It implements TOCTOU mitigation similar to WriteFileSafely.
func ReadFileSafely(repoRoot, virtualPath string) ([]byte, error) {
	safePath, err := security.SafeResolve(repoRoot, virtualPath)
	if err != nil {
		return nil, err
	}

	f, err := os.Open(safePath)
	if err != nil {
		return nil, fmt.Errorf("failed to open file for reading: %w", err)
	}
	defer f.Close()

	fiLstat, err := os.Lstat(safePath)
	if err != nil {
		return nil, fmt.Errorf("failed to lstat file: %w", err)
	}

	if fiLstat.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("security violation: symlink detected on read: %s", safePath)
	}

	fiFstat, err := f.Stat()
	if err != nil {
		return nil, fmt.Errorf("failed to fstat file: %w", err)
	}

	if !os.SameFile(fiLstat, fiFstat) {
		return nil, fmt.Errorf("security violation: TOCTOU symlink race detected on read: %s", safePath)
	}

	content, err := io.ReadAll(f)
	if err != nil {
		return nil, fmt.Errorf("failed to read file content: %w", err)
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
	if err := os.MkdirAll(dir, defaultDirPerm); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	tmpFile, err := os.CreateTemp(dir, filepath.Base(safePath)+".tmp.*")
	if err != nil {
		return fmt.Errorf("failed to create temp file: %w", err)
	}
	tmpName := tmpFile.Name()
	defer func() {
		tmpFile.Close()
		os.Remove(tmpName)
	}()

	if _, err := tmpFile.Write(content); err != nil {
		return fmt.Errorf("failed to write to temp file: %w", err)
	}
	if err := tmpFile.Sync(); err != nil {
		return fmt.Errorf("failed to sync temp file: %w", err)
	}
	if err := tmpFile.Close(); err != nil {
		return fmt.Errorf("failed to close temp file: %w", err)
	}
	if err := os.Chmod(tmpName, defaultFilePerm); err != nil {
		return fmt.Errorf("failed to chmod temp file: %w", err)
	}

	if err := os.Rename(tmpName, safePath); err != nil {
		return fmt.Errorf("failed to rename temp file: %w", err)
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
func ShouldIgnoreFile(repoRoot, relPath string, gitIgnore *ignore.GitIgnore) bool {
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

	// 3. Check if it's a known text file to avoid IsBinaryFile I/O bottleneck
	name := filepath.Base(slashRelPath)
	ext := strings.ToLower(filepath.Ext(name))
	knownTextExts := map[string]bool{
		".go": true, ".js": true, ".ts": true, ".py": true, ".md": true,
		".txt": true, ".json": true, ".yaml": true, ".yml": true,
		".html": true, ".css": true, ".java": true, ".c": true, ".cpp": true,
		".h": true, ".hpp": true, ".rb": true, ".php": true, ".sh": true,
		".rs": true, ".swift": true, ".kt": true, ".xml": true, ".sql": true,
		".mod": true, ".sum": true,
	}
	if knownTextExts[ext] {
		return false
	}

	// 4. Check if it's a binary file
	absPath, err := security.SafeResolve(repoRoot, slashRelPath)
	if err != nil {
		return true // Treat unsafe paths outside repo root as ignored
	}
	return IsBinaryFile(absPath)
}

// DiscoverCodeFiles recursively walks the codebase to find high-signal source files.
// It ignores build, dependency, and output files, as well as any paths in the custom ignores list.
func DiscoverCodeFiles(repoRoot string, ignores []string) ([]string, error) {
	var files []string
	gitIgnore := ignore.CompileIgnoreLines(ignores...)

	err := filepath.WalkDir(repoRoot, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: error walking path %s: %v\n", path, err)
			return nil // Skip items with errors
		}

		if path == repoRoot {
			return nil
		}

		rel, err := filepath.Rel(repoRoot, path)
		if err != nil {
			fmt.Fprintf(os.Stderr, "Warning: failed to get relative path for %s: %v\n", path, err)
			return nil
		}

		slashRel := filepath.ToSlash(rel)

		if d.IsDir() {
			name := d.Name()
			if strings.HasPrefix(name, ".") || strings.HasSuffix(name, ".egg-info") || (gitIgnore != nil && gitIgnore.MatchesPath(slashRel)) {
				return filepath.SkipDir
			}
			return nil
		}

		if ShouldIgnoreFile(repoRoot, slashRel, gitIgnore) {
			return nil
		}

		files = append(files, slashRel)
		return nil
	})

	return files, err
}



// IsBinaryFile checks if a file is binary by scanning the first 1024 bytes for null bytes.
func IsBinaryFile(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return true
	}
	defer f.Close()

	buf := make([]byte, binaryDetectionBufSize)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return true
	}

	for i := 0; i < n; i++ {
		if buf[i] == 0 {
			return true
		}
	}
	return false
}

