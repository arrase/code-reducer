package engine

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	o := &orchestrator{client: &scriptedLLM{numCtx: 2048}}
	err := o.RunInit(context.Background(), repoRoot, cfg, func(e Event) {})
	if err != nil {
		t.Fatalf("unexpected error running RunInit: %v", err)
	}
}

func TestOrchestratorRunInitSkipsTestFiles(t *testing.T) {
	repoRoot := t.TempDir()
	for _, f := range []string{"main.go", "main_test.go"} {
		if err := os.WriteFile(filepath.Join(repoRoot, f), []byte("package main\n"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", f, err)
		}
	}

	cfg := &config.Config{DocsDir: "docs", ExtractionSteps: config.DefaultExtractionSteps}

	o := &orchestrator{client: &scriptedLLM{numCtx: 2048}}
	if err := o.RunInit(context.Background(), repoRoot, cfg, func(e Event) {}); err != nil {
		t.Fatalf("unexpected error running RunInit: %v", err)
	}

	cache, err := loadMetadataCache(repoRoot, cfg.DocsDir)
	if err != nil {
		t.Fatalf("failed to load metadata cache: %v", err)
	}
	if _, ok := cache.Files["main_test.go"]; ok {
		t.Error("expected the test file to stay out of the cache")
	}
	if _, ok := cache.Files["main.go"]; !ok {
		t.Error("expected the source file to be documented")
	}
}

func TestRunUpdatePrunesTestFilesWhenTheyBecomeExcluded(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := "docs"
	if err := os.MkdirAll(filepath.Join(repoRoot, docsDir, "modules"), 0755); err != nil {
		t.Fatalf("failed to create modules dir: %v", err)
	}

	sourceFile := "main.go"
	testFile := "main_test.go"
	for _, f := range []string{sourceFile, testFile} {
		if err := os.WriteFile(filepath.Join(repoRoot, f), []byte("package main\n"), 0644); err != nil {
			t.Fatalf("failed to write %s: %v", f, err)
		}
	}

	steps := []config.ExtractionStep{{Name: "API_SIGNATURES", Prompt: "extract"}}
	sourceHash, err := computeSHA256(repoRoot, sourceFile)
	if err != nil {
		t.Fatalf("failed to hash the source file: %v", err)
	}
	cache := &MetadataCache{
		Version:   currentCacheVersion,
		StepsHash: computeStepsHash(steps),
		Files: map[string]FileCacheEntry{
			sourceFile: {SHA256: sourceHash, Facts: "cached source facts"},
			testFile:   {SHA256: "old-test-hash", Facts: "stale test facts"},
		},
		Modules: map[string]string{".": "# Module: .\n\n### File: main_test.go\nStale test facts"},
	}
	if err := saveMetadataCache(repoRoot, docsDir, cache); err != nil {
		t.Fatalf("failed to seed metadata cache: %v", err)
	}
	rootModule := filepath.Join(repoRoot, docsDir, "modules", "README.md")
	if err := os.WriteFile(rootModule, []byte(cache.Modules["."]), 0644); err != nil {
		t.Fatalf("failed to seed the root module page: %v", err)
	}

	cfg := &config.Config{DocsDir: docsDir, ExtractionSteps: steps, IncludeTests: false}
	o := &orchestrator{client: &scriptedLLM{numCtx: 2048}}
	if err := o.RunUpdate(context.Background(), repoRoot, cfg, func(e Event) {}); err != nil {
		t.Fatalf("unexpected error running RunUpdate: %v", err)
	}

	updated, err := loadMetadataCache(repoRoot, docsDir)
	if err != nil {
		t.Fatalf("failed to load metadata cache: %v", err)
	}
	if _, ok := updated.Files[testFile]; ok {
		t.Errorf("expected the cache entry for %s to be pruned, got %v", testFile, updated.Files)
	}
	if _, ok := updated.Files[sourceFile]; !ok {
		t.Error("expected the source file to keep its cache entry")
	}

	page, err := os.ReadFile(rootModule)
	if err != nil {
		t.Fatalf("failed to read the root module page: %v", err)
	}
	if strings.Contains(string(page), testFile) {
		t.Errorf("expected %s to be dropped from the root module page, got:\n%s", testFile, page)
	}
	if !strings.Contains(string(page), sourceFile) {
		t.Errorf("expected %s in the regenerated root module page, got:\n%s", sourceFile, page)
	}
}

func TestOrchestratorRunInitKeepsTheSystemMessageConstant(t *testing.T) {
	repoRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(repoRoot, "main.go"), []byte("package main\n"), 0644); err != nil {
		t.Fatalf("failed to write test file: %v", err)
	}

	steps := config.DefaultExtractionSteps[:2]
	cfg := &config.Config{
		DocsDir:               "docs",
		ExtractionSteps:       steps,
		CharsPerToken:         config.CharsPerTokenDefault,
		SystemPrompt:          "SHARED SYSTEM PROMPT",
		ModuleSynthesisPrompt: "MODULE PAGE STYLE",
		ArchitecturePrompt:    "STANDARD PAGE STYLE",
	}

	client := &scriptedLLM{numCtx: 2048}
	o := &orchestrator{client: client}
	if err := o.RunInit(context.Background(), repoRoot, cfg, func(e Event) {}); err != nil {
		t.Fatalf("unexpected error running RunInit: %v", err)
	}

	if len(client.systems) == 0 {
		t.Fatal("expected the run to call the LLM")
	}
	for i, system := range client.systems {
		if system != cfg.SystemPrompt {
			t.Errorf("call %d: system message = %q, want %q", i, system, cfg.SystemPrompt)
		}
	}

	seen := make(map[string]bool, len(client.prompts))
	for i, prompt := range client.prompts {
		if seen[prompt] {
			t.Errorf("call %d repeated the user message of an earlier call: %q", i, prompt)
		}
		seen[prompt] = true
	}

	for _, step := range steps {
		if got := countCallsContaining(client.prompts, step.Prompt); got != 1 {
			t.Errorf("expected exactly one call carrying step %q in the user turn, got %d", step.Name, got)
		}
	}
	if got := countCallsContaining(client.prompts, cfg.ModuleSynthesisPrompt); got != 4 {
		t.Errorf("expected the 4 module page slots to carry the module style, got %d calls", got)
	}
	if got := countCallsContaining(client.prompts, cfg.ArchitecturePrompt); got != 6 {
		t.Errorf("expected the 6 standard page sections to carry the page style, got %d calls", got)
	}
	for _, varying := range append([]string{cfg.ModuleSynthesisPrompt, cfg.ArchitecturePrompt}, steps[0].Prompt, steps[1].Prompt) {
		if strings.Contains(cfg.SystemPrompt, varying) {
			t.Errorf("expected %q to stay out of the system message", varying)
		}
	}
}

// countCallsContaining counts the recorded user messages that contain needle.
func countCallsContaining(prompts []string, needle string) int {
	count := 0
	for _, p := range prompts {
		if strings.Contains(p, needle) {
			count++
		}
	}
	return count
}

func TestGenerateStandardDocsDerivesQuickstartFromArchitecture(t *testing.T) {
	cfg := &config.Config{
		DocsDir:            "docs",
		CharsPerToken:      config.CharsPerTokenDefault,
		SystemPrompt:       "system",
		ArchitecturePrompt: "architecture",
	}
	client := &scriptedLLM{numCtx: 2048, results: []LLMResult{
		stopResult("ARCH overview prose"),
		stopResult("ARCH boundary prose"),
		stopResult("ARCH interaction prose"),
		stopResult("QS purpose prose"),
		stopResult("QS layout prose"),
		stopResult("QS workflow prose"),
	}}

	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, cfg.DocsDir), 0755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}

	o := &orchestrator{client: client}
	if err := o.GenerateStandardDocs(context.Background(), repoRoot, cfg.DocsDir, "RAW ROOT SUMMARY", cfg, func(t EventType, msg string) {}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(client.prompts) != len(architecturePage.sections)+len(quickstartPage.sections) {
		t.Fatalf("expected one call per section, got %d calls", len(client.prompts))
	}

	for i, section := range architecturePage.sections {
		prompt := client.prompts[i]
		if !strings.Contains(prompt, section.instruction) || !strings.Contains(prompt, architecturePage.sourceLabel) {
			t.Errorf("architecture section %q did not receive its own instruction and the root summary, got:\n%s", section.heading, prompt)
		}
		if !strings.Contains(prompt, "RAW ROOT SUMMARY") {
			t.Errorf("architecture section %q was not built from the root summary, got:\n%s", section.heading, prompt)
		}
	}

	for i, section := range quickstartPage.sections {
		prompt := client.prompts[len(architecturePage.sections)+i]
		if !strings.Contains(prompt, section.instruction) || !strings.Contains(prompt, quickstartPage.sourceLabel) {
			t.Errorf("quickstart section %q did not receive its own instruction and the architecture page, got:\n%s", section.heading, prompt)
		}
		if strings.Contains(prompt, "RAW ROOT SUMMARY") {
			t.Errorf("quickstart section %q was built from the raw root summary, got:\n%s", section.heading, prompt)
		}
		for _, archSection := range architecturePage.sections {
			if !strings.Contains(prompt, archSection.heading) {
				t.Errorf("quickstart section %q is not derived from the architecture page, missing %q, got:\n%s", section.heading, archSection.heading, prompt)
			}
		}
	}

	quick, err := os.ReadFile(filepath.Join(repoRoot, cfg.DocsDir, quickstartPage.fileName))
	if err != nil {
		t.Fatalf("failed to read quickstart.md: %v", err)
	}
	for _, want := range []string{"QS purpose prose", "QS layout prose", "QS workflow prose"} {
		if !strings.Contains(string(quick), want) {
			t.Errorf("expected %q in quickstart.md, got:\n%s", want, quick)
		}
	}
	if strings.Contains(string(quick), "ARCH overview prose") {
		t.Errorf("expected the architecture prose to stay in architecture.md, got:\n%s", quick)
	}
}

func TestGenerateStandardDocsRejectsIncompleteLLMOutput(t *testing.T) {
	cfg := &config.Config{DocsDir: "docs", SystemPrompt: "system", ArchitecturePrompt: "architecture"}

	cases := []struct {
		name        string
		results     []LLMResult
		wantErr     error
		archWritten bool
	}{
		{
			name:    "truncated first architecture section",
			results: []LLMResult{truncatedResult("partial arch")},
			wantErr: ErrTruncatedOutput,
		},
		{
			name:    "empty architecture section",
			results: []LLMResult{stopResult("  \n ")},
			wantErr: ErrEmptyOutput,
		},
		{
			name:    "truncated last architecture section",
			results: []LLMResult{stopResult("arch"), stopResult("arch"), truncatedResult("partial arch")},
			wantErr: ErrTruncatedOutput,
		},
		{
			name:        "truncated quickstart section",
			results:     []LLMResult{stopResult("arch"), stopResult("arch"), stopResult("arch"), truncatedResult("partial quick")},
			wantErr:     ErrTruncatedOutput,
			archWritten: true,
		},
		{
			name:        "empty quickstart section",
			results:     []LLMResult{stopResult("arch"), stopResult("arch"), stopResult("arch"), stopResult("")},
			wantErr:     ErrEmptyOutput,
			archWritten: true,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			if err := os.MkdirAll(filepath.Join(repoRoot, cfg.DocsDir), 0755); err != nil {
				t.Fatalf("failed to create docs dir: %v", err)
			}

			o := &orchestrator{client: &scriptedLLM{numCtx: 2048, results: tc.results}}
			err := o.GenerateStandardDocs(context.Background(), repoRoot, cfg.DocsDir, "root summary", cfg, func(t EventType, msg string) {})
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("expected %v, got %v", tc.wantErr, err)
			}

			qsPath := filepath.Join(repoRoot, cfg.DocsDir, "quickstart.md")
			if _, err := os.Stat(qsPath); !os.IsNotExist(err) {
				t.Errorf("expected %s not to be written", qsPath)
			}

			archPath := filepath.Join(repoRoot, cfg.DocsDir, "architecture.md")
			_, archErr := os.Stat(archPath)
			if archExists := archErr == nil; archExists != tc.archWritten {
				t.Fatalf("expected architecture.md existence to be %v, got %v", tc.archWritten, archExists)
			}
		})
	}
}

func TestGenerateStandardDocsWritesFixedSkeletons(t *testing.T) {
	cfg := &config.Config{DocsDir: "docs", SystemPrompt: "system", ArchitecturePrompt: "architecture"}

	repoRoot := t.TempDir()
	if err := os.MkdirAll(filepath.Join(repoRoot, cfg.DocsDir), 0755); err != nil {
		t.Fatalf("failed to create docs dir: %v", err)
	}

	o := &orchestrator{client: &scriptedLLM{numCtx: 2048}}
	if err := o.GenerateStandardDocs(context.Background(), repoRoot, cfg.DocsDir, "# Module: .", cfg, func(t EventType, msg string) {}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	arch, err := os.ReadFile(filepath.Join(repoRoot, cfg.DocsDir, "architecture.md"))
	if err != nil {
		t.Fatalf("failed to read architecture.md: %v", err)
	}
	quick, err := os.ReadFile(filepath.Join(repoRoot, cfg.DocsDir, "quickstart.md"))
	if err != nil {
		t.Fatalf("failed to read quickstart.md: %v", err)
	}

	assertHeadings(t, "architecture.md", string(arch), []string{
		"# Architecture", "## Overview", "## System Boundaries", "## Module Interaction",
	})
	assertHeadings(t, "quickstart.md", string(quick), []string{
		"# Quickstart", "## What This Project Does", "## Project Layout", "## Common Workflows",
	})
}

// assertHeadings checks that the document contains exactly the expected headings, in order.
func assertHeadings(t *testing.T, name, doc string, want []string) {
	t.Helper()
	var got []string
	for _, line := range strings.Split(doc, "\n") {
		if strings.HasPrefix(line, "#") {
			got = append(got, line)
		}
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%s headings = %v, want %v", name, got, want)
	}
}
