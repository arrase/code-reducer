package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arrase/code-reducer/internal/config"
)

func TestMarkAllTreeAffected(t *testing.T) {
	tree := &DirNode{
		Path: ".",
		Children: map[string]*DirNode{
			"cmd": {
				Path: "cmd",
			},
			"internal": {
				Path: "internal",
				Children: map[string]*DirNode{
					"engine": {Path: "internal/engine"},
				},
			},
		},
	}

	affected := markAllTreeAffected(tree)
	if !affected["."] || !affected["cmd"] || !affected["internal"] || !affected["internal/engine"] {
		t.Fatalf("expected all nodes to be affected, got %v", affected)
	}
}

func TestComputeFilesHashes(t *testing.T) {
	tmp := t.TempDir()
	file1 := "file1.txt"
	if err := os.WriteFile(filepath.Join(tmp, file1), []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	logEvent := func(t EventType, msg string) {}
	hashes := computeFilesHashes(tmp, []string{file1, "missing.txt"}, logEvent)
	if hashes[file1] == "" {
		t.Fatalf("expected hash for file1, got empty")
	}
	if _, ok := hashes["missing.txt"]; ok {
		t.Fatalf("did not expect hash for missing file")
	}
}

func TestUpdateAgentGuidelines(t *testing.T) {
	tmp := t.TempDir()

	// Initial creation
	if err := updateAgentGuidelines(tmp, "docs"); err != nil {
		t.Fatalf("failed to create agent guidelines: %v", err)
	}

	content, err := os.ReadFile(filepath.Join(tmp, agentsFileName))
	if err != nil {
		t.Fatalf("failed to read agent guidelines: %v", err)
	}
	if len(content) == 0 {
		t.Fatalf("expected non-empty guidelines")
	}

	// Idempotent update
	if err := updateAgentGuidelines(tmp, "docs"); err != nil {
		t.Fatalf("failed second run: %v", err)
	}
}

func TestOrchestratorRunInit(t *testing.T) {
	repoRoot := t.TempDir()
	file1 := "main.go"
	if err := os.WriteFile(filepath.Join(repoRoot, file1), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg := &config.Config{
		DocsDir:         "docs",
		ExtractionSteps: config.DefaultExtractionSteps,
	}

	o := &orchestrator{client: &mockLLMCaller{numCtx: 2048}}
	err := o.RunInit(context.Background(), repoRoot, cfg, func(e Event) {})
	if err != nil {
		t.Fatalf("unexpected error running RunInit: %v", err)
	}
}
