package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSanitizeAndValidateMermaid_ValidDiagramsUntouched(t *testing.T) {
	input := `# System Architecture

` + "```mermaid" + `
flowchart TD
    A["CLI Input"] --> B["Engine Orchestrator"]
    B --> C["Wiki Output"]
` + "```" + `

## Sequence

` + "```mermaid" + `
sequenceDiagram
    participant CLI
    participant Engine
    CLI->>Engine: Run(ctx, repoRoot)
    Engine-->>CLI: nil
` + "```" + `
`

	got, degraded := sanitizeAndValidateMermaid(input)
	if degraded != 0 {
		t.Fatalf("expected 0 degraded blocks, got %d", degraded)
	}
	if got != input {
		t.Errorf("expected valid diagrams to remain untouched.\ngot:\n%s\nwant:\n%s", got, input)
	}
}

func TestSanitizeAndValidateMermaid_AutoSanitizesSmallLLMMistakes(t *testing.T) {
	input := "```mermaid\n" +
		"flowchart TD\n" +
		"    A[parse(input): ok] --> end[Finish]\n" +
		"    end --> B(emit<Result>; done)\n" +
		"    S1([Start]) --> DB[(Database)]\n" +
		"    DB --> C((Circle)) --> P([\"parse(input)\"])\n" +
		"    subgraph S\n" +
		"        B --> end\n" +
		"        P -->|done| end\n" +
		"    end\n" +
		"```"

	got, degraded := sanitizeAndValidateMermaid(input)
	if degraded != 0 {
		t.Fatalf("expected 0 degraded blocks after auto-sanitization, got %d:\n%s", degraded, got)
	}

	wantLines := []string{
		`    A["parse(input): ok"] --> endNode[Finish]`,
		`    endNode --> B("emit&lt;Result&gt;, done")`,
		`    S1([Start]) --> DB[(Database)]`,
		`    DB --> C((Circle)) --> P(["parse(input)"])`,
		`        B --> endNode`,
		`        P -->|done| endNode`,
		`    end`,
	}
	for _, want := range wantLines {
		if !strings.Contains(got, want) {
			t.Errorf("expected sanitized output to contain %q, got:\n%s", want, got)
		}
	}
}

func TestSanitizeAndValidateMermaid_DegradesInvalidBlocks(t *testing.T) {
	input := `# Broken Diagrams

` + "```mermaid" + `
unknownDiagramType
    A --> B
` + "```" + `

` + "```mermaid" + `
flowchart TD
    A["unclosed quote] --> B
` + "```" + `

` + "```mermaid" + `
flowchart TD
    A[unclosed bracket --> B
` + "```" + `
`

	got, degraded := sanitizeAndValidateMermaid(input)
	if degraded != 3 {
		t.Fatalf("expected 3 degraded blocks, got %d", degraded)
	}
	if strings.Contains(got, "```mermaid") {
		t.Errorf("expected all invalid mermaid fences to be rewritten to ```text, got:\n%s", got)
	}
	if strings.Count(got, "<!-- code-reducer: mermaid parse failed") != 3 {
		t.Errorf("expected 3 degradation comments, got:\n%s", got)
	}
	if strings.Count(got, "```text") != 3 {
		t.Errorf("expected 3 ```text fences, got:\n%s", got)
	}
}

func TestHeuristicMermaidError(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr string
	}{
		{
			name:    "empty block",
			body:    "   \n  %% only comment\n",
			wantErr: "empty mermaid block",
		},
		{
			name:    "unknown diagram type",
			body:    "magicGraph TD\n    A --> B",
			wantErr: `unknown diagram type "magicGraph"`,
		},
		{
			name:    "header only",
			body:    "flowchart TD\n  %% comment only\n",
			wantErr: "diagram has no content lines",
		},
		{
			name:    "unclosed double quote",
			body:    "flowchart TD\n    A[\"broken] --> B",
			wantErr: "unclosed double quote",
		},
		{
			name:    "unbalanced brackets",
			body:    "flowchart TD\n    A[broken --> B",
			wantErr: "unbalanced brackets",
		},
		{
			name:    "reserved keyword end as node ID",
			body:    "flowchart TD\n    A --> end[Done]",
			wantErr: "reserved keyword 'end' used as node ID",
		},
		{
			name:    "unquoted special chars in label",
			body:    "flowchart TD\n    A[List<T>; ok] --> B",
			wantErr: "unquoted special characters in node label",
		},
		{
			name:    "escaped quotes inside quoted label are valid",
			body:    `flowchart TD` + "\n" + `    A["say \"hello\""] --> B`,
			wantErr: "",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := heuristicMermaidError(tc.body)
			if got != tc.wantErr {
				t.Errorf("heuristicMermaidError() = %q, want %q", got, tc.wantErr)
			}
		})
	}
}

func TestGenerateWikiIndex(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := "wiki"

	mustWrite := func(relPath, content string) {
		t.Helper()
		full := filepath.Join(repoRoot, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("wiki/architecture.md", "# Architecture\n")
	mustWrite("wiki/quickstart.md", "# Quickstart\n")
	mustWrite("wiki/modules/README.md", "# Root Module\n\nCoordinates the CLI entrypoint and top-level configuration.\n")
	mustWrite("wiki/modules/internal/engine/README.md", "# Engine Module\n\nExecutes the hierarchical Map-Reduce documentation pipeline.\n")

	tree := buildTree([]string{
		"main.go",
		"internal/engine/orchestrator.go",
		"internal/engine/synthesize.go",
	})
	cache := newEmptyCache()
	cache.Modules["."] = "# Root Module\n\nCoordinates the CLI entrypoint and top-level configuration.\n"
	cache.Modules["internal/engine"] = "# Engine Module\n\n```mermaid\nflowchart TD\n    A --> B\n```\n\nExecutes the hierarchical Map-Reduce documentation pipeline.\n"

	if err := generateWikiIndex(repoRoot, docsDir, tree, cache); err != nil {
		t.Fatalf("generateWikiIndex failed: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(repoRoot, "wiki/index.md"))
	if err != nil {
		t.Fatalf("failed to read generated index.md: %v", err)
	}
	index := string(data)

	expectedSnippets := []string{
		"# Documentation Index",
		"- [Architecture Overview](architecture.md)",
		"- [Developer Quickstart](quickstart.md)",
		"- [Root (.)](modules/README.md) — Coordinates the CLI entrypoint and top-level configuration. (1 file: main.go)",
		"- [internal/engine](modules/internal/engine/README.md) — Executes the hierarchical Map-Reduce documentation pipeline. (2 files: orchestrator.go, synthesize.go)",
	}
	for _, snippet := range expectedSnippets {
		if !strings.Contains(index, snippet) {
			t.Errorf("expected index.md to contain %q, got:\n%s", snippet, index)
		}
	}
}

func TestValidateMarkdownLinks(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := "wiki"

	mustWrite := func(relPath, content string) {
		t.Helper()
		full := filepath.Join(repoRoot, relPath)
		if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	mustWrite("wiki/architecture.md", "# Architecture Overview\n\n## Data Flow\n\nDetails here.\n")
	mustWrite("wiki/modules/internal/engine/README.md", "# Engine\n\n## Synthesis\n\nEngine details.\n")

	quickstartContent := `# Developer Quickstart

## Setup

Valid link: [Architecture](architecture.md#data-flow)
Valid case-insensitive anchor link: [Arch Upper](architecture.md#Data-Flow)
Valid same-file anchor: [Jump to Setup](#setup)
Valid line anchor: [Arch Line](architecture.md#L1-L3)
External link: [Go](https://golang.org)
Image link: ![Diagram](nonexistent.png)
Inline code link: ` + "`[IgnoreMe](missing-inline.md)`" + `

` + "```markdown" + `
[IgnoreFenced](missing-fenced.md)
` + "```" + `

Small-LLM auto-repairable link: [Engine](internal/engine/README.md)
Small-LLM auto-repairable dir link with anchor: [Engine Synthesis](internal/engine#synthesis)
Broken file link: [Missing](does-not-exist.md)
Broken anchor link: [Bad Anchor](architecture.md#missing-heading)
Root-absolute link: [RootAbs](/wiki/architecture.md)
`
	mustWrite("wiki/quickstart.md", quickstartContent)

	brokenCount, err := validateMarkdownLinks(repoRoot, docsDir)
	if err != nil {
		t.Fatalf("validateMarkdownLinks failed: %v", err)
	}
	if brokenCount != 3 {
		t.Fatalf("expected 3 broken links (missing file, missing anchor, root-absolute), got %d", brokenCount)
	}

	gotBytes, err := os.ReadFile(filepath.Join(repoRoot, "wiki/quickstart.md"))
	if err != nil {
		t.Fatal(err)
	}
	got := string(gotBytes)

	// 1. Verify smart auto-repair rewrote small-LLM module links in-place
	if !strings.Contains(got, "[Engine](modules/internal/engine/README.md)") {
		t.Errorf("expected smart auto-repair to rewrite [Engine](internal/engine/README.md) to [Engine](modules/internal/engine/README.md), got:\n%s", got)
	}
	if !strings.Contains(got, "[Engine Synthesis](modules/internal/engine/README.md#synthesis)") {
		t.Errorf("expected smart auto-repair to rewrite [Engine Synthesis](internal/engine#synthesis) to [Engine Synthesis](modules/internal/engine/README.md#synthesis), got:\n%s", got)
	}

	// 2. Verify broken links were stamped
	expectedStamps := []string{
		`<!-- code-reducer: broken internal link [does-not-exist.md] file "does-not-exist.md" does not exist -->`,
		`<!-- code-reducer: broken internal link [architecture.md#missing-heading] heading anchor "#missing-heading" does not exist in "architecture.md" -->`,
		`<!-- code-reducer: broken internal link [/wiki/architecture.md] root-absolute link is not portable; use a relative path -->`,
	}
	for _, stamp := range expectedStamps {
		if !strings.Contains(got, stamp) {
			t.Errorf("expected quickstart.md to contain stamp %q, got:\n%s", stamp, got)
		}
	}

	// 3. Verify links inside inline code or fenced code were NOT stamped
	if strings.Contains(got, "missing-inline.md]") && strings.Contains(got, "broken internal link [missing-inline.md]") {
		t.Errorf("expected inline code link to be ignored, got:\n%s", got)
	}
	if strings.Contains(got, "broken internal link [missing-fenced.md]") {
		t.Errorf("expected fenced code link to be ignored, got:\n%s", got)
	}

	// 4. Verify idempotency: re-running validateMarkdownLinks does not duplicate stamps
	brokenCount2, err := validateMarkdownLinks(repoRoot, docsDir)
	if err != nil {
		t.Fatalf("second validateMarkdownLinks failed: %v", err)
	}
	if brokenCount2 != 3 {
		t.Fatalf("expected 3 broken links on second run, got %d", brokenCount2)
	}
	gotBytes2, err := os.ReadFile(filepath.Join(repoRoot, "wiki/quickstart.md"))
	if err != nil {
		t.Fatal(err)
	}
	if string(gotBytes2) != got {
		t.Errorf("expected validateMarkdownLinks to be idempotent.\nfirst:\n%s\nsecond:\n%s", got, string(gotBytes2))
	}
}

func TestFinalizeWiki_EndToEnd(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := "wiki"

	fullArch := filepath.Join(repoRoot, "wiki/architecture.md")
	if err := os.MkdirAll(filepath.Dir(fullArch), 0755); err != nil {
		t.Fatal(err)
	}
	archContent := "# Architecture\n\n```mermaid\nflowchart TD\n    A[init(cfg)] --> end[Done]\n```\n\nSee [Engine](internal/engine/README.md).\n"
	if err := os.WriteFile(fullArch, []byte(archContent), 0644); err != nil {
		t.Fatal(err)
	}

	fullMod := filepath.Join(repoRoot, "wiki/modules/internal/engine/README.md")
	if err := os.MkdirAll(filepath.Dir(fullMod), 0755); err != nil {
		t.Fatal(err)
	}
	modContent := "# Engine\n\nCore map-reduce engine.\n"
	if err := os.WriteFile(fullMod, []byte(modContent), 0644); err != nil {
		t.Fatal(err)
	}

	tree := buildTree([]string{"internal/engine/orchestrator.go"})
	cache := newEmptyCache()
	cache.Modules["internal/engine"] = modContent

	var statuses []string
	if err := finalizeWiki(repoRoot, docsDir, tree, cache, func(msg string) {
		statuses = append(statuses, msg)
	}); err != nil {
		t.Fatalf("finalizeWiki failed: %v", err)
	}

	updatedArch, err := os.ReadFile(fullArch)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(updatedArch), `A["init(cfg)"] --> endNode[Done]`) {
		t.Errorf("expected architecture.md mermaid diagram to be sanitized, got:\n%s", string(updatedArch))
	}
	if !strings.Contains(string(updatedArch), `[Engine](modules/internal/engine/README.md)`) {
		t.Errorf("expected architecture.md link to be auto-repaired, got:\n%s", string(updatedArch))
	}

	if _, err := os.Stat(filepath.Join(repoRoot, "wiki/index.md")); err != nil {
		t.Errorf("expected wiki/index.md to be created, got err: %v", err)
	}
	if len(statuses) == 0 {
		t.Errorf("expected status messages to be emitted")
	}
}
