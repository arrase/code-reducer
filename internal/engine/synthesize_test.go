package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/arrase/code-reducer/internal/config"
)

const mockLLMContent = "Mocked LLM summary response"

// scriptedLLM is a fake llmCaller that replays one scripted result per call, so a
// test can let the extraction succeed and make a following slot fail. It also
// records the system message, the user message and the generation bound of every
// call. Calls beyond the script return generic content.
type scriptedLLM struct {
	numCtx  int
	results []LLMResult
	calls   int
	systems []string
	prompts []string
	bounds  []int
}

func (s *scriptedLLM) next() (LLMResult, error) {
	if s.calls >= len(s.results) {
		s.calls++
		return LLMResult{Content: mockLLMContent, DoneReason: "stop"}, nil
	}
	res := s.results[s.calls]
	s.calls++
	return res, nil
}

func (s *scriptedLLM) CallLLM(ctx context.Context, systemPrompt string, messages []Message, jsonFormat bool) (LLMResult, error) {
	s.systems = append(s.systems, systemPrompt)
	s.prompts = append(s.prompts, messages[0].Content)
	s.bounds = append(s.bounds, 0)
	return s.next()
}

func (s *scriptedLLM) CallLLMWithNumPredict(ctx context.Context, systemPrompt string, messages []Message, numPredict int) (LLMResult, error) {
	s.systems = append(s.systems, systemPrompt)
	s.prompts = append(s.prompts, messages[0].Content)
	s.bounds = append(s.bounds, numPredict)
	return s.next()
}

func (s *scriptedLLM) NumCtx() int {
	if s.numCtx == 0 {
		return 2048
	}
	return s.numCtx
}

func stopResult(content string) LLMResult {
	return LLMResult{Content: content, DoneReason: "stop", EvalCount: 42, PromptCount: 100}
}

func truncatedResult(content string) LLMResult {
	return LLMResult{Content: content, DoneReason: "length", EvalCount: 1024, PromptCount: 19997}
}

func newTestPipeline(t *testing.T, client llmCaller, cfg *config.Config) *pipelineState {
	t.Helper()
	return &pipelineState{
		client:              client,
		repoRoot:            t.TempDir(),
		cfg:                 cfg,
		cache:               newEmptyCache(),
		affectedDirs:        map[string]bool{},
		precalculatedHashes: map[string]string{},
		logEvent:            func(t EventType, msg string) {},
	}
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
		client:              &scriptedLLM{numCtx: 2048},
		repoRoot:            repoRoot,
		cfg:                 cfg,
		cache:               cache,
		affectedDirs:        affectedDirs,
		precalculatedHashes: hashes,
		logEvent:            func(t EventType, msg string) {},
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
}

func TestExtractFileFactsRejectsIncompleteLLMOutput(t *testing.T) {
	step := config.ExtractionStep{Name: "API_SIGNATURES", Prompt: "extract"}

	cases := []struct {
		name    string
		res     LLMResult
		wantErr error
	}{
		{name: "truncated by length", res: truncatedResult("partial facts"), wantErr: ErrTruncatedOutput},
		{name: "truncated after a complete sentence", res: truncatedResult("func Call() error. The rest of the list was cut"), wantErr: ErrTruncatedOutput},
		{name: "empty content on stop", res: stopResult("   \n\t "), wantErr: ErrEmptyOutput},
		{name: "empty content on length", res: truncatedResult(""), wantErr: ErrTruncatedOutput},
		{name: "empty fence on stop", res: stopResult("```json\n```"), wantErr: ErrEmptyOutput},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newTestPipeline(t, &scriptedLLM{results: []LLMResult{tc.res}},
				&config.Config{DocsDir: "docs", ExtractionSteps: []config.ExtractionStep{step}})

			sampleFile := "main.go"
			if err := os.WriteFile(filepath.Join(p.repoRoot, sampleFile), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
				t.Fatalf("failed to write sample file: %v", err)
			}

			facts, err := extractFileFacts(context.Background(), p, sampleFile, ".", promptCharBudget(2048, 0, config.CharsPerTokenDefault))
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v (facts: %q)", tc.wantErr, err, facts)
			}
			if facts != "" {
				t.Errorf("expected no facts to be returned, got %q", facts)
			}
			if len(p.cache.Files) != 0 {
				t.Errorf("expected no cache entry to be written, got %v", p.cache.Files)
			}
		})
	}
}

func TestExtractFileFactsUnreadableFileIsNotAnLLMError(t *testing.T) {
	p := newTestPipeline(t, &scriptedLLM{},
		&config.Config{DocsDir: "docs", ExtractionSteps: config.DefaultExtractionSteps})

	facts, err := extractFileFacts(context.Background(), p, "does-not-exist.go", ".", 1024)
	if err != nil {
		t.Fatalf("expected nil error for unreadable file, got %v", err)
	}
	if facts != "" {
		t.Errorf("expected empty facts, got %q", facts)
	}
	if len(p.cache.Files) != 0 {
		t.Errorf("expected no cache entry, got %v", p.cache.Files)
	}
}

func TestSynthesizeNodeDoesNotPersistFailedSlot(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := "docs"
	modulesDir := filepath.Join(repoRoot, docsDir, "modules")
	if err := os.MkdirAll(modulesDir, 0755); err != nil {
		t.Fatalf("failed to create modules dir: %v", err)
	}

	sampleFile := "main.go"
	if err := os.WriteFile(filepath.Join(repoRoot, sampleFile), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("failed to write sample file: %v", err)
	}

	step := config.ExtractionStep{Name: "API_SIGNATURES", Prompt: "extract"}
	cache := newEmptyCache()
	p := &pipelineState{
		client: &scriptedLLM{numCtx: 2048, results: []LLMResult{
			stopResult("extracted facts"),
			truncatedResult("half a responsibility"),
		}},
		repoRoot:            repoRoot,
		cfg:                 &config.Config{DocsDir: docsDir, ExtractionSteps: []config.ExtractionStep{step}},
		cache:               cache,
		affectedDirs:        map[string]bool{"sub": true},
		precalculatedHashes: map[string]string{sampleFile: "hash123"},
		logEvent:            func(t EventType, msg string) {},
	}

	node := &DirNode{Path: "sub", Files: []string{sampleFile}, Children: make(map[string]*DirNode)}
	res, err := synthesizeNode(context.Background(), p, node)
	if !errors.Is(err, ErrTruncatedOutput) {
		t.Fatalf("expected ErrTruncatedOutput, got %v (res: %q)", err, res)
	}
	if _, ok := cache.Modules["sub"]; ok {
		t.Error("expected no module cache entry when a slot fails")
	}
	moduleFile := filepath.Join(modulesDir, "sub", "README.md")
	if _, err := os.Stat(moduleFile); !os.IsNotExist(err) {
		t.Errorf("expected %s not to be written", moduleFile)
	}
	if len(cache.Files) != 1 {
		t.Errorf("expected the successfully extracted file facts to be cached, got %v", cache.Files)
	}
}
