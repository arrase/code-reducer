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
	output := stdout.String() + stderr.String()
	trimmed := strings.TrimSpace(output)
	if err != nil {
		// Return output even on error as some git commands output error details on stdout/stderr
		return trimmed, fmt.Errorf("git command failed: %v, output: %s", err, trimmed)
	}
	return trimmed, nil
}

// GetGitHead returns the current HEAD commit hash.
func GetGitHead(repoRoot string) (string, error) {
	head, err := RunGit(repoRoot, "rev-parse", "HEAD")
	if err != nil {
		return "", err
	}
	return head, nil
}

