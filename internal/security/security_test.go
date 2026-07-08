package security

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeResolve(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "code-reducer-security-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	repoRoot := filepath.Join(tmpDir, "repo")
	if err := os.MkdirAll(repoRoot, 0755); err != nil {
		t.Fatalf("failed to create repo root: %v", err)
	}

	// Create a dummy file inside the repo
	validFile := filepath.Join(repoRoot, "valid.md")
	if err := os.WriteFile(validFile, []byte("valid content"), 0644); err != nil {
		t.Fatalf("failed to create valid file: %v", err)
	}

	// 1. Test valid path inside repository
	resolved, err := SafeResolve(repoRoot, "valid.md")
	if err != nil {
		t.Errorf("unexpected error resolving valid path: %v", err)
	}
	if resolved != validFile {
		t.Errorf("expected resolved path %q, got %q", validFile, resolved)
	}

	// 2. Test path traversal attack
	_, err = SafeResolve(repoRoot, "../secret.txt")
	if err == nil {
		t.Error("expected error for path traversal, got nil")
	} else if !strings.Contains(err.Error(), "path traversal detected") {
		t.Errorf("expected path traversal error message, got: %v", err)
	}

	// 3. Test absolute path (virtual root) - should be safe
	resolvedAbs, err := SafeResolve(repoRoot, "/etc/passwd")
	if err != nil {
		t.Errorf("unexpected error for virtual absolute path: %v", err)
	}
	expectedAbs := filepath.Join(repoRoot, "etc/passwd")
	if resolvedAbs != expectedAbs {
		t.Errorf("expected %q, got %q", expectedAbs, resolvedAbs)
	}

	// 4. Test symlink pointing outside repository
	outsideDir := filepath.Join(tmpDir, "outside")
	if err := os.MkdirAll(outsideDir, 0755); err != nil {
		t.Fatalf("failed to create outside dir: %v", err)
	}
	outsideFile := filepath.Join(outsideDir, "secret.md")
	if err := os.WriteFile(outsideFile, []byte("secrets"), 0644); err != nil {
		t.Fatalf("failed to create outside file: %v", err)
	}

	symlinkPath := filepath.Join(repoRoot, "malicious_link")
	if err := os.Symlink(outsideDir, symlinkPath); err != nil {
		t.Fatalf("failed to create symlink: %v", err)
	}

	_, err = SafeResolve(repoRoot, "malicious_link/secret.md")
	if err == nil {
		t.Error("expected error for symlink pointing outside, got nil")
	} else if !strings.Contains(err.Error(), "symlink points outside repository") {
		t.Errorf("expected symlink outside error message, got: %v", err)
	}
}

func TestAcquireLock(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "code-reducer-lock-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. Acquire exclusive lock
	lock1, err := AcquireLock(tmpDir, true)
	if err != nil {
		t.Fatalf("unexpected error acquiring lock1: %v", err)
	}
	defer lock1.Unlock()

	// Verify PID is written to file
	lockPath := filepath.Join(tmpDir, LockFileName)
	if _, err := os.Stat(lockPath); os.IsNotExist(err) {
		t.Error("lock file was not created")
	}

	// 2. Attempt to acquire another exclusive lock (should fail)
	_, err = AcquireLock(tmpDir, true)
	if err == nil {
		t.Error("expected error acquiring second exclusive lock, got nil")
	}

	// 3. Attempt to acquire shared lock (should fail while exclusive is held)
	_, err = AcquireLock(tmpDir, false)
	if err == nil {
		t.Error("expected error acquiring shared lock while exclusive is held, got nil")
	}
}

func TestEnsureGitignoreHasLockfile(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "code-reducer-gitignore-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	err = EnsureGitignoreHasLockfile(tmpDir)
	if err != nil {
		t.Fatalf("failed to ensure gitignore has lockfile: %v", err)
	}

	gitignorePath := filepath.Join(tmpDir, ".gitignore")
	data, err := os.ReadFile(gitignorePath)
	if err != nil {
		t.Fatalf("failed to read .gitignore: %v", err)
	}

	if !strings.Contains(string(data), LockFileName) {
		t.Errorf("expected .gitignore to contain %q, got: %s", LockFileName, string(data))
	}
}
