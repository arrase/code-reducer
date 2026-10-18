package engine

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arrase/code-reducer/internal/config"
)

func TestCollectTreeDirs(t *testing.T) {
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

	dirs := collectTreeDirs(tree)
	if !dirs["."] || !dirs["cmd"] || !dirs["internal"] || !dirs["internal/engine"] {
		t.Fatalf("expected all nodes to be collected, got %v", dirs)
	}
}

func TestHashCodeFiles(t *testing.T) {
	tmp := t.TempDir()
	file1 := "file1.txt"
	if err := os.WriteFile(filepath.Join(tmp, file1), []byte("hello"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	validFiles, hashes := hashCodeFiles(tmp, []string{file1, "missing.txt"}, func(string) {})
	if len(validFiles) != 1 || validFiles[0] != file1 {
		t.Fatalf("expected validFiles [%s], got %v", file1, validFiles)
	}
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

func TestOrchestratorRunInitAndUpdate(t *testing.T) {
	repoRoot := t.TempDir()
	file1 := "main.go"
	if err := os.WriteFile(filepath.Join(repoRoot, file1), []byte("package main\n\nfunc main() {}\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	cfg := &config.Config{
		DocsDir:            "docs",
		SystemPrompt:       config.DefaultSystemPrompt,
		ArchitecturePrompt: config.DefaultArchitecturePrompt,
		QuickstartPrompt:   config.DefaultQuickstartPrompt,
		ExtractionSteps:    config.DefaultExtractionSteps,
	}

	o := &orchestrator{client: &mockLLMCaller{numCtx: 2048}}
	if err := o.RunInit(context.Background(), repoRoot, cfg, func(string) {}); err != nil {
		t.Fatalf("unexpected error running RunInit: %v", err)
	}

	if err := o.RunUpdate(context.Background(), repoRoot, cfg, func(string) {}); err != nil {
		t.Fatalf("unexpected error running RunUpdate: %v", err)
	}

	// Deleting quickstart.md with no code changes should still trigger regeneration on update
	qsPath := filepath.Join(repoRoot, "docs", "quickstart.md")
	if err := os.Remove(qsPath); err != nil {
		t.Fatalf("failed to remove quickstart.md: %v", err)
	}
	if err := o.RunUpdate(context.Background(), repoRoot, cfg, func(string) {}); err != nil {
		t.Fatalf("unexpected error running RunUpdate after deleting quickstart.md: %v", err)
	}
	if _, err := os.Stat(qsPath); err != nil {
		t.Fatalf("expected quickstart.md to be regenerated on RunUpdate, got error: %v", err)
	}

	// Orphan module docs and empty directories in docs/modules/ should be cleaned up on update
	orphanDir := filepath.Join(repoRoot, "docs", "modules", "removed_pkg")
	orphanFile := filepath.Join(orphanDir, "README.md")
	if err := os.MkdirAll(orphanDir, 0755); err != nil {
		t.Fatalf("failed to create orphan dir: %v", err)
	}
	if err := os.WriteFile(orphanFile, []byte("stale"), 0644); err != nil {
		t.Fatalf("failed to write orphan file: %v", err)
	}
	if err := o.RunUpdate(context.Background(), repoRoot, cfg, func(string) {}); err != nil {
		t.Fatalf("unexpected error running RunUpdate with orphan module: %v", err)
	}
	if _, err := os.Stat(orphanDir); !os.IsNotExist(err) {
		t.Fatalf("expected orphan module directory %s to be removed, got err: %v", orphanDir, err)
	}
}

func TestComputeStepsHash(t *testing.T) {
	baseCfg := &config.Config{
		ModelID:                     "qwen:7b",
		SystemPrompt:                "sys",
		ModuleSynthesisPrompt:       "mod",
		ArchitecturePrompt:          "arch",
		QuickstartPrompt:            "qs",
		FileFactConsolidationPrompt: "fact",
		ExtractionSteps:             []config.ExtractionStep{{Name: "STEP1", Prompt: "p1"}},
	}
	baseHash := computeStepsHash(baseCfg)

	modifiedModel := *baseCfg
	modifiedModel.ModelID = "qwen:14b"
	if computeStepsHash(&modifiedModel) == baseHash {
		t.Errorf("expected hash to change when ModelID changes")
	}

	modifiedPrompt := *baseCfg
	modifiedPrompt.SystemPrompt = "sys2"
	if computeStepsHash(&modifiedPrompt) == baseHash {
		t.Errorf("expected hash to change when SystemPrompt changes")
	}

	modifiedArch := *baseCfg
	modifiedArch.ArchitecturePrompt = "arch2"
	if computeStepsHash(&modifiedArch) == baseHash {
		t.Errorf("expected hash to change when ArchitecturePrompt changes")
	}

	modifiedQS := *baseCfg
	modifiedQS.QuickstartPrompt = "qs2"
	if computeStepsHash(&modifiedQS) == baseHash {
		t.Errorf("expected hash to change when QuickstartPrompt changes")
	}

	// Verify field boundary shifts do not collide ("ab"+"c" vs "a"+"bc")
	shiftedBoundary := *baseCfg
	shiftedBoundary.ExtractionSteps = []config.ExtractionStep{{Name: "STEP", Prompt: "1p1"}}
	if computeStepsHash(&shiftedBoundary) == baseHash {
		t.Errorf("expected hash to differ when characters shift across step Name/Prompt boundary")
	}
}

func TestLoadMetadataCacheErrorReturnsNil(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := "docs"
	if err := os.MkdirAll(filepath.Join(repoRoot, docsDir), 0755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoRoot, docsDir, metadataFileName), []byte("{invalid-json"), 0644); err != nil {
		t.Fatalf("failed to write corrupted metadata: %v", err)
	}

	cache, err := loadMetadataCache(repoRoot, docsDir)
	if err == nil {
		t.Fatalf("expected error on invalid metadata JSON")
	}
	if cache != nil {
		t.Fatalf("expected nil cache on error, got %v", cache)
	}
}

func TestPrepareOllamaRequest(t *testing.T) {
	client := newLLMClient("ornith:9b", "http://localhost:11434", 4096, func(string) {})
	payload, err := client.prepareOllamaRequest("sys prompt", "user prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	var req ollamaRequest
	if err := json.Unmarshal(payload, &req); err != nil {
		t.Fatalf("failed to unmarshal payload: %v", err)
	}
	if len(req.Messages) != 2 || req.Messages[0].Content != "sys prompt" || req.Messages[1].Content != "user prompt" {
		t.Fatalf("unexpected messages: %+v", req.Messages)
	}
	if req.Options == nil || req.Options.Temperature != 0.1 || req.Options.NumCtx != 4096 {
		t.Fatalf("unexpected options: %+v", req.Options)
	}
}

func TestCallLLMDoneReasonLengthWarning(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		resp := ollamaResponse{
			Message:    Message{Role: "assistant", Content: "partial output"},
			DoneReason: "length",
		}
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	var warnings []string
	client := newLLMClient("ornith:9b", srv.URL, 4096, func(msg string) {
		warnings = append(warnings, msg)
	})

	out, err := client.CallLLM(context.Background(), "sys", "user")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if out != "partial output" {
		t.Fatalf("expected 'partial output', got %q", out)
	}
	if len(warnings) != 1 || !strings.Contains(warnings[0], "done_reason: length") {
		t.Fatalf("expected truncation warning, got %v", warnings)
	}
}
