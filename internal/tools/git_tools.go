package tools

import (
	"bytes"
	"fmt"
	"os/exec"
	"strings"
)

// RunGit executes a git command in the specified repo directory.
func RunGit(repoRoot string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"--no-pager"}, args...)...)
	cmd.Dir = repoRoot
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	trimmedOut := strings.TrimSpace(stdout.String())
	if err != nil {
		trimmedErr := strings.TrimSpace(stderr.String())
		return trimmedOut, fmt.Errorf("git command failed: %v, stderr: %s", err, trimmedErr)
	}
	return trimmedOut, nil
}

// VerifyGitRepo checks if git is available and the directory is a git repository.
func VerifyGitRepo(repoRoot string) error {
	_, err := RunGit(repoRoot, "rev-parse", "--is-inside-work-tree")
	if err != nil {
		return fmt.Errorf("not a git repository (or any of the parent directories)")
	}
	return nil
}
