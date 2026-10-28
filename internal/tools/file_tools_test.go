package tools_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/arrase/code-reducer/internal/tools"
	ignore "github.com/sabhiram/go-gitignore"
)

func TestWriteFileSafely(t *testing.T) {
	repoRoot := t.TempDir()

	err := tools.WriteFileSafely(repoRoot, "src/new_file.txt", []byte("hello"))
	if err != nil {
		t.Fatalf("WriteFileSafely() failed: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(repoRoot, "src", "new_file.txt"))
	if err != nil {
		t.Fatalf("Failed to read created file: %v", err)
	}
	if string(content) != "hello" {
		t.Errorf("Expected 'hello', got '%s'", string(content))
	}
}

func TestFileExistsAndRemoveSafely(t *testing.T) {
	repoRoot := t.TempDir()

	if tools.FileExistsSafely(repoRoot, "src/app.go") {
		t.Fatal("Expected FileExistsSafely to be false for missing file")
	}

	if err := tools.WriteFileSafely(repoRoot, "src/app.go", []byte("package src")); err != nil {
		t.Fatalf("WriteFileSafely() failed: %v", err)
	}

	if !tools.FileExistsSafely(repoRoot, "src/app.go") {
		t.Fatal("Expected FileExistsSafely to be true for existing file")
	}

	if tools.FileExistsSafely(repoRoot, "../outside.go") {
		t.Fatal("Expected FileExistsSafely to be false on path traversal")
	}

	if err := tools.RemoveFileSafely(repoRoot, "src/app.go"); err != nil {
		t.Fatalf("RemoveFileSafely() failed: %v", err)
	}

	if tools.FileExistsSafely(repoRoot, "src/app.go") {
		t.Fatal("Expected FileExistsSafely to be false after removal")
	}

	// Removing again should be a no-op
	if err := tools.RemoveFileSafely(repoRoot, "src/app.go"); err != nil {
		t.Fatalf("RemoveFileSafely() on non-existent file failed: %v", err)
	}

	// Path traversal should return an error
	if err := tools.RemoveFileSafely(repoRoot, "../outside.go"); err == nil {
		t.Fatal("Expected RemoveFileSafely to error on path traversal")
	}
}

func TestLoadGitignore(t *testing.T) {
	repoRoot := t.TempDir()

	// No gitignore initially
	patterns, err := tools.LoadGitignore(repoRoot)
	if err != nil {
		t.Fatalf("LoadGitignore() failed when file is missing: %v", err)
	}
	if len(patterns) != 0 {
		t.Errorf("Expected no patterns, got %v", patterns)
	}

	// Create gitignore
	content := []byte("# Comment\n*.log\n\nbuild/\n")
	if err := os.WriteFile(filepath.Join(repoRoot, ".gitignore"), content, 0644); err != nil {
		t.Fatal(err)
	}

	patterns, err = tools.LoadGitignore(repoRoot)
	if err != nil {
		t.Fatalf("LoadGitignore() failed: %v", err)
	}
	expected := []string{"*.log", "build/"}
	if !reflect.DeepEqual(patterns, expected) {
		t.Errorf("LoadGitignore() returned %v, expected %v", patterns, expected)
	}
}

func TestShouldIgnoreFile(t *testing.T) {
	gitIgnore := ignore.CompileIgnoreLines("*.log", "build/")

	tests := []struct {
		name    string
		relPath string
		want    bool
	}{
		{"Normal file", "src/main.go", false},
		{"Ignored by pattern 1", "test.log", true},
		{"Ignored by pattern 2", "build/output.bin", true},
		{"Hidden file", "src/.hidden.txt", true},
		{"Hidden directory", ".git/config", true},
		{"Egg info", "src/my_package.egg-info/PKG-INFO", true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tools.ShouldIgnoreFile(tt.relPath, gitIgnore); got != tt.want {
				t.Errorf("ShouldIgnoreFile() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestDiscoverCodeFiles(t *testing.T) {
	repoRoot := t.TempDir()

	dirs := []string{
		"src",
		"src/.hidden",
		"build",
	}
	for _, d := range dirs {
		if err := os.MkdirAll(filepath.Join(repoRoot, d), 0755); err != nil {
			t.Fatal(err)
		}
	}

	textFiles := []string{
		"src/main.go",
		"src/utils.go",
		"src/main_test.go",
		"src/app.test.js",
		"src/app.spec.ts",
		"src/test_runner.py",
		"src/runner_test.py",
		"src/.hidden/secret.go",
		"build/gen.go",
		"test.log",
		"go.sum",
		"package-lock.json",
		"Makefile",
		"Dockerfile",
		"README.md",
		"notes.txt",
		"logo.svg",
	}
	for _, f := range textFiles {
		if err := os.WriteFile(filepath.Join(repoRoot, f), []byte("package main"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	// Binary file with a null byte inside first 512 bytes (but valid code extension)
	if err := os.WriteFile(filepath.Join(repoRoot, "src/compiled_blob.dat"), []byte{'a', 'b', 0, 'c'}, 0644); err != nil {
		t.Fatal(err)
	}

	// Symlink to a code file should be skipped
	_ = os.Symlink(filepath.Join(repoRoot, "src/main.go"), filepath.Join(repoRoot, "src/symlink.go"))

	ignores := []string{"*.log", "build/"}
	discovered, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		t.Fatalf("DiscoverCodeFiles() failed: %v", err)
	}

	expected := []string{"src/main.go", "src/utils.go"}
	if len(discovered) != len(expected) {
		t.Fatalf("Expected %d files, got %d: %v", len(expected), len(discovered), discovered)
	}

	found := make(map[string]bool)
	for _, f := range discovered {
		found[f] = true
	}
	for _, f := range expected {
		if !found[f] {
			t.Errorf("Expected to find %s", f)
		}
	}
}

func TestEnsureGitignoreHasLockfile(t *testing.T) {
	repoRoot := t.TempDir()

	if err := os.WriteFile(filepath.Join(repoRoot, ".gitignore"), []byte("*.log"), 0644); err != nil {
		t.Fatal(err)
	}

	if err := tools.EnsureGitignoreHasLockfile(repoRoot); err != nil {
		t.Fatalf("EnsureGitignoreHasLockfile() failed: %v", err)
	}

	// Second call should be a no-op
	if err := tools.EnsureGitignoreHasLockfile(repoRoot); err != nil {
		t.Fatalf("EnsureGitignoreHasLockfile() second call failed: %v", err)
	}
}

func TestVerifyGitRepo(t *testing.T) {
	nonGitDir := t.TempDir()
	if err := tools.VerifyGitRepo(nonGitDir); err == nil {
		t.Fatal("Expected error for non-git directory, got nil")
	}
}
