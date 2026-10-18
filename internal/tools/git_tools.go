package tools

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// VerifyGitRepo checks if git is available and the directory is a git repository.
func VerifyGitRepo(repoRoot string) error {
	cmd := exec.Command("git", "--no-pager", "rev-parse", "--is-inside-work-tree")
	cmd.Dir = repoRoot
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("not a git repository (or any of the parent directories): %w, stderr: %s", err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
