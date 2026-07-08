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

// CreateGitSummary builds the git status and diff evidence passed to LLM prompts.
func CreateGitSummary(command string, repoRoot string, lastGitHead string, lastUpdatedAt string) (string, error) {
	var sections []string

	// 1. git status --short
	status, err := RunGit(repoRoot, "status", "--short")
	if err != nil {
		status = fmt.Sprintf("(error running git status: %v)", err)
	}
	sections = append(sections, formatGitSection("git status --short", status))

	// 2. git rev-parse HEAD
	head, err := GetGitHead(repoRoot)
	if err != nil {
		head = "(unknown)"
	}
	sections = append(sections, formatGitSection("git rev-parse HEAD", head))

	// 3. git log
	if command == "update" && lastGitHead != "" {
		logRange := fmt.Sprintf("%s..HEAD", lastGitHead)
		logOutput, err := RunGit(repoRoot, "log", logRange, "--name-status", "--oneline")
		if err != nil {
			logOutput = fmt.Sprintf("(error running git log range: %v)", err)
		}
		sections = append(sections, formatGitSection(fmt.Sprintf("git log %s..HEAD --name-status --oneline", lastGitHead), logOutput))
	} else if command == "update" && lastUpdatedAt != "" {
		logOutput, err := RunGit(repoRoot, "log", "--since", lastUpdatedAt, "--name-status", "--oneline")
		if err != nil {
			logOutput = fmt.Sprintf("(error running git log since: %v)", err)
		}
		sections = append(sections, formatGitSection(fmt.Sprintf("git log --since %s --name-status --oneline", lastUpdatedAt), logOutput))
	} else {
		logOutput, err := RunGit(repoRoot, "log", "--max-count=20", "--name-status", "--oneline")
		if err != nil {
			logOutput = fmt.Sprintf("(error running git log max 20: %v)", err)
		}
		if command == "update" {
			sections = append(sections, "No prior Code-Reducer update timestamp was found.")
		}
		sections = append(sections, formatGitSection("git log --max-count=20 --name-status --oneline", logOutput))
	}

	// 4. git diff --name-status HEAD
	diff, err := RunGit(repoRoot, "diff", "--name-status", "HEAD")
	if err != nil {
		diff = fmt.Sprintf("(error running git diff: %v)", err)
	}
	sections = append(sections, formatGitSection("git diff --name-status HEAD", diff))

	return strings.Join(sections, "\n\n"), nil
}

func formatGitSection(command string, output string) string {
	if output == "" {
		output = "(no output)"
	}
	return fmt.Sprintf("$ %s\n%s", command, output)
}
