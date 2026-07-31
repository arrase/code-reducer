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

	// Create a structure
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

	files := []string{
		"src/main.go",
		"src/utils.go",
		"src/.hidden/secret.txt",
		"build/output.bin",
		"test.log",
	}
	for _, f := range files {
		if err := os.WriteFile(filepath.Join(repoRoot, f), []byte("test"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	ignores := []string{"*.log", "build/"}
	discovered, err := tools.DiscoverCodeFiles(repoRoot, ignores)
	if err != nil {
		t.Fatalf("DiscoverCodeFiles() failed: %v", err)
	}

	expected := []string{"src/main.go", "src/utils.go"}
	if len(discovered) != len(expected) {
		t.Fatalf("Expected %d files, got %d: %v", len(expected), len(discovered), discovered)
	}

	// Order might vary depending on OS, but WalkDir is usually deterministic.
	// We sort just in case or do a map check.
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
