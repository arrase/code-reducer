package tools_test

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
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

func TestIsTestFile(t *testing.T) {
	tests := []struct {
		relPath string
		want    bool
	}{
		{"internal/engine/client.go", false},
		{"internal/engine/synthesize_test.go", true},
		{"pkg/parser_test.py", true},
		{"pkg/test_parser.py", true},
		{"src/parser_test.py", true},
		{"src/parser_test.js", true},
		{"src/parser.test.js", true},
		{"src/parser.spec.js", true},
		{"src/parser.test.ts", true},
		{"src/parser.spec.ts", true},
		{"src/OrderServiceTest.java", true},
		{"src/OrderRepositoryTests.cs", true},
		{"src/OrderSpec.scala", false},
		{"src/OrderServiceTests.scala", true},
		{"spec/orders_spec.rb", true},
		{"spec/test_orders.rb", true},
		{"src/orders_test.rs", true},
		{"src/contest.go", false},
		{"src/latest.java", false},
		{"docs/testing.md", false},
		{"test/main.go", false},
	}

	for _, tt := range tests {
		t.Run(tt.relPath, func(t *testing.T) {
			if got := tools.IsTestFile(tt.relPath); got != tt.want {
				t.Errorf("IsTestFile(%q) = %v, want %v", tt.relPath, got, tt.want)
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
		"src/utils_test.go",
		"src/test_utils.py",
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
	discovered, err := tools.DiscoverCodeFiles(repoRoot, tools.DiscoveryOptions{Ignores: ignores})
	if err != nil {
		t.Fatalf("DiscoverCodeFiles() failed: %v", err)
	}

	expected := []string{"src/main.go", "src/utils.go"}
	assertDiscovered(t, discovered, expected)

	withTests, err := tools.DiscoverCodeFiles(repoRoot, tools.DiscoveryOptions{Ignores: ignores, IncludeTests: true})
	if err != nil {
		t.Fatalf("DiscoverCodeFiles() with tests failed: %v", err)
	}
	assertDiscovered(t, withTests, []string{"src/main.go", "src/test_utils.py", "src/utils.go", "src/utils_test.go"})

	ignoresWithTests := append([]string{"**/*_test.go"}, ignores...)
	explicitlyIgnored, err := tools.DiscoverCodeFiles(repoRoot, tools.DiscoveryOptions{Ignores: ignoresWithTests, IncludeTests: true})
	if err != nil {
		t.Fatalf("DiscoverCodeFiles() with an explicit test ignore failed: %v", err)
	}
	assertDiscovered(t, explicitlyIgnored, []string{"src/main.go", "src/test_utils.py", "src/utils.go"})
}

// assertDiscovered checks that discovery returned exactly the expected files.
func assertDiscovered(t *testing.T, got, want []string) {
	t.Helper()
	sort.Strings(got)
	if !reflect.DeepEqual(got, want) {
		t.Errorf("DiscoverCodeFiles() = %v, want %v", got, want)
	}
}
