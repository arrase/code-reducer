package engine

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arrase/code-reducer/internal/config"
)

type mockLLMCaller struct {
	numCtx        int
	systemPrompts []string
	userPrompts   []string
}

func (m *mockLLMCaller) CallLLM(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	m.systemPrompts = append(m.systemPrompts, systemPrompt)
	m.userPrompts = append(m.userPrompts, userPrompt)
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

	ctx := context.Background()
	p := &pipelineState{
		client:              &mockLLMCaller{numCtx: 2048},
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               cache,
		affectedDirs:        affectedDirs,
		precalculatedHashes: hashes,
		onStatus:            func(string) {},
	}

	// 1. Empty node
	emptyNode := &DirNode{Path: "empty", Children: make(map[string]*DirNode)}
	res, err := synthesizeNode(ctx, p, emptyNode)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if res != "" {
		t.Fatalf("expected empty result, got %s", res)
	}

	// 2. Cached unaffected node
	cache.Modules["cached"] = "Cached Summary"
	cachedNode := &DirNode{Path: "cached", Children: make(map[string]*DirNode)}
	res, err = synthesizeNode(ctx, p, cachedNode)
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

	res, err = synthesizeNode(ctx, p, rootNode)
	if err != nil {
		t.Fatalf("unexpected error on synthesis: %v", err)
	}
	if res == "" {
		t.Fatalf("expected non-empty synthesis result")
	}
	if !IsInitialized(repoRoot, docsDir) {
		t.Fatalf("expected metadata cache to be saved incrementally")
	}

	// Verify provenance header is written to disk while cache.Modules stores clean summary
	rootDocBytes, err := os.ReadFile(filepath.Join(repoRoot, docsDir, "modules", "README.md"))
	if err != nil {
		t.Fatalf("failed to read root module doc: %v", err)
	}
	wantHeader := "> **Module:** `.` | **Source Files:** `main.go`\n\nMocked LLM summary response"
	if string(rootDocBytes) != wantHeader {
		t.Errorf("expected module doc with provenance header %q, got %q", wantHeader, string(rootDocBytes))
	}
	if cache.Modules["."] != "Mocked LLM summary response" {
		t.Errorf("expected cache.Modules[\".\"] to hold clean summary without provenance header, got %q", cache.Modules["."])
	}
}

func TestLanguageInstructionWiring(t *testing.T) {
	repoRoot := t.TempDir()
	sampleFile := "main.go"
	if err := os.WriteFile(filepath.Join(repoRoot, sampleFile), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("failed to write sample file: %v", err)
	}

	mock := &mockLLMCaller{numCtx: 2048}
	cfg := &config.Config{
		DocsDir:               "docs",
		Language:              "Spanish",
		SystemPrompt:          config.DefaultSystemPrompt,
		ModuleSynthesisPrompt: config.DefaultModuleSynthesisPrompt,
		ExtractionSteps: []config.ExtractionStep{
			{Name: "API_SIGNATURES", Prompt: "extract api"},
		},
	}

	p := &pipelineState{
		client:              mock,
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               newEmptyCache(),
		affectedDirs:        map[string]bool{".": true},
		precalculatedHashes: map[string]string{sampleFile: "hash1"},
		onStatus:            func(string) {},
	}

	rootNode := &DirNode{Path: ".", Files: []string{sampleFile}, Children: make(map[string]*DirNode)}
	if _, err := synthesizeNode(context.Background(), p, rootNode); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(mock.systemPrompts) < 2 {
		t.Fatalf("expected at least 2 LLM calls (extraction + synthesis), got %d", len(mock.systemPrompts))
	}
	for i, sp := range mock.systemPrompts {
		if !strings.Contains(sp, "Write all documentation prose, headings, and table descriptions in Spanish.") {
			t.Errorf("call %d system prompt missing Spanish language instruction:\n%s", i, sp)
		}
	}
}

func TestExtractFileFactsMultiChunkKVCacheOrder(t *testing.T) {
	repoRoot := t.TempDir()
	sampleFile := "large.go"
	content := strings.Repeat("func partA() {}\n", 60) + strings.Repeat("func partB() {}\n", 60)
	if err := os.WriteFile(filepath.Join(repoRoot, sampleFile), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write sample file: %v", err)
	}

	mock := &mockLLMCaller{numCtx: 500}
	cfg := &config.Config{
		SystemPrompt:    "sys",
		ExtractionSteps: config.DefaultExtractionSteps,
	}
	p := &pipelineState{
		client:              mock,
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               newEmptyCache(),
		precalculatedHashes: map[string]string{sampleFile: "hash"},
		onStatus:            func(string) {},
	}

	_, err := extractFileFacts(context.Background(), p, sampleFile, []string{sampleFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	numSteps := len(cfg.ExtractionSteps)
	if len(mock.userPrompts) < numSteps*2 {
		t.Fatalf("expected at least %d prompts for multi-chunk extraction, got %d", numSteps*2, len(mock.userPrompts))
	}

	// Verify all 4 extraction steps for chunk 0 run consecutively with the exact same code prefix
	prefixEnd := strings.Index(mock.userPrompts[0], "\n---\nTask (")
	if prefixEnd == -1 {
		t.Fatalf("unexpected prompt format: %s", mock.userPrompts[0])
	}
	chunk0Prefix := mock.userPrompts[0][:prefixEnd]
	for i := 1; i < numSteps; i++ {
		if !strings.HasPrefix(mock.userPrompts[i], chunk0Prefix) {
			t.Errorf("step %d did not share chunk 0 prefix for Ollama KV cache reuse", i)
		}
	}
}

type scriptedLLMCaller struct {
	numCtx    int
	responses []string
	calls     int
}

func (s *scriptedLLMCaller) CallLLM(_ context.Context, _, _ string) (string, error) {
	idx := s.calls
	s.calls++
	if idx < len(s.responses) {
		return s.responses[idx], nil
	}
	return "None", nil
}

func (s *scriptedLLMCaller) NumCtx() int {
	return s.numCtx
}

func TestExtractFileFactsNoneShortCircuit(t *testing.T) {
	repoRoot := t.TempDir()
	sampleFile := "stateless.go"
	content := strings.Repeat("func helper() {}\n", 60) + strings.Repeat("func helper2() {}\n", 60)
	if err := os.WriteFile(filepath.Join(repoRoot, sampleFile), []byte(content), 0644); err != nil {
		t.Fatalf("failed to write sample file: %v", err)
	}

	cfg := &config.Config{
		SystemPrompt: "sys",
		ExtractionSteps: []config.ExtractionStep{
			{Name: "API_SIGNATURES", Prompt: "extract api"},
			{Name: "STATE_AND_CONCURRENCY", Prompt: "extract state"},
		},
	}

	// 2 chunks x 2 steps = 4 extraction calls:
	// Chunk 0: API_SIGNATURES returns "- func Helper()", STATE_AND_CONCURRENCY returns "None."
	// Chunk 1: API_SIGNATURES returns "- None", STATE_AND_CONCURRENCY returns "None"
	// Neither step should trigger a consolidation LLM call (total calls must be 4, not 5 or 6).
	caller := &scriptedLLMCaller{
		numCtx:    500,
		responses: []string{"- func Helper()", "None.", "- None", "None"},
	}

	p := &pipelineState{
		client:              caller,
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               newEmptyCache(),
		precalculatedHashes: map[string]string{sampleFile: "hash"},
		onStatus:            func(string) {},
	}

	facts, err := extractFileFacts(context.Background(), p, sampleFile, []string{sampleFile})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if caller.calls != 4 {
		t.Fatalf("expected exactly 4 extraction LLM calls (0 consolidation calls), got %d", caller.calls)
	}
	if strings.Contains(facts, "STATE_AND_CONCURRENCY") || strings.Contains(facts, "None") {
		t.Errorf("expected None step to be omitted from facts, got:\n%s", facts)
	}
	if !strings.Contains(facts, "#### [API_SIGNATURES]\n- func Helper()") {
		t.Errorf("expected API_SIGNATURES fact to be preserved, got:\n%s", facts)
	}
}
