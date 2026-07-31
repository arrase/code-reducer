package security_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/arrase/code-reducer/internal/security"
)

func TestSafeResolve(t *testing.T) {
	repoRoot := t.TempDir()

	// Create some files and directories for testing
	if err := os.MkdirAll(filepath.Join(repoRoot, "src", "sub"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "src", "file.txt"), []byte("data"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, "..config"), []byte("config"), 0644); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name      string
		inputPath string
		wantErr   bool
	}{
		{
			name:      "Valid relative path",
			inputPath: "src/file.txt",
			wantErr:   false,
		},
		{
			name:      "Dot-prefixed filename",
			inputPath: "..config",
			wantErr:   false,
		},
		{
			name:      "Path traversal attempt 1",
			inputPath: "../out_of_repo.txt",
			wantErr:   true,
		},
		{
			name:      "Path traversal attempt 2",
			inputPath: "src/../../out_of_repo.txt",
			wantErr:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := security.SafeResolve(repoRoot, tt.inputPath)
			if (err != nil) != tt.wantErr {
				t.Errorf("SafeResolve() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestSafeResolveSymlinkEscape(t *testing.T) {
	repoRoot := t.TempDir()
	outOfRepo := t.TempDir()

	// Create a symlink inside the repo pointing outside
	symlinkPath := filepath.Join(repoRoot, "escape")
	if err := os.Symlink(outOfRepo, symlinkPath); err != nil {
		t.Skip("Symlinks not supported on this OS/filesystem")
	}

	_, err := security.SafeResolve(repoRoot, filepath.Join("escape", "file.txt"))
	if err == nil {
		t.Error("SafeResolve() should have returned an error for symlink escape")
	}
}

func TestAcquireLock(t *testing.T) {
	repoRoot := t.TempDir()

	lock1, err := security.AcquireLock(repoRoot)
	if err != nil {
		t.Fatalf("AcquireLock() failed: %v", err)
	}
	if lock1 == nil {
		t.Fatal("Expected lock, got nil")
	}

	// Try to acquire again
	_, err = security.AcquireLock(repoRoot)
	if err == nil {
		t.Fatal("AcquireLock() should have failed when lock is already held")
	}

	// Unlock
	if err := lock1.Unlock(); err != nil {
		t.Errorf("Unlock() failed: %v", err)
	}

	// Acquire again after unlock
	lock2, err := security.AcquireLock(repoRoot)
	if err != nil {
		t.Fatalf("AcquireLock() failed after unlock: %v", err)
	}
	lock2.Unlock()
}
