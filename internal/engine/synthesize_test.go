package engine

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/arrase/code-reducer/internal/config"
)

type mockLLMCaller struct {
	numCtx int
}

func (m *mockLLMCaller) CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (string, error) {
	return "Mocked LLM summary response", nil
}

func (m *mockLLMCaller) NumCtx() int {
	if m.numCtx == 0 {
		return 2048
	}
	return m.numCtx
}

func TestSynthesizeNode(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := "docs"
	modulesDir := filepath.Join(repoRoot, docsDir, "modules")
	if err := os.MkdirAll(modulesDir, 0755); err != nil {
		t.Fatalf("failed to create modules dir: %v", err)
	}

	// Create sample file
	sampleFile := "main.go"
	if err := os.WriteFile(filepath.Join(repoRoot, sampleFile), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("failed to write sample file: %v", err)
	}

	cfg := &config.Config{
		DocsDir:         docsDir,
		ExtractionSteps: config.DefaultExtractionSteps,
	}

	cache := newEmptyCache()
	affectedDirs := map[string]bool{".": true, "sub": true}
	hashes := map[string]string{sampleFile: "hash123"}

	p := &pipelineContext{
		ctx:                 context.Background(),
		client:              &mockLLMCaller{numCtx: 2048},
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               cache,
		affectedDirs:        affectedDirs,
		precalculatedHashes: hashes,
		logEvent:            func(t EventType, msg string) {},
	}

	// 1. Empty node
	emptyNode := &DirNode{Path: "empty", Children: make(map[string]*DirNode)}
	res, err := synthesizeNode(p, emptyNode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "" {
		t.Fatalf("expected empty result, got %s", res)
	}

	// 2. Cached unaffected node
	cache.Modules["cached"] = "Cached Summary"
	cachedNode := &DirNode{Path: "cached", Children: make(map[string]*DirNode)}
	res, err = synthesizeNode(p, cachedNode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "Cached Summary" {
		t.Fatalf("expected cached summary, got %s", res)
	}

	// 3. Node with file and child
	childNode := &DirNode{Path: "sub", Files: []string{sampleFile}, Children: make(map[string]*DirNode)}
	rootNode := &DirNode{
		Path:     ".",
		Files:    []string{sampleFile},
		Children: map[string]*DirNode{"sub": childNode},
	}

	res, err = synthesizeNode(p, rootNode)
	if err != nil {
		t.Fatalf("unexpected error on synthesis: %v", err)
	}
	if res == "" {
		t.Fatalf("expected non-empty synthesis result")
	}
}
