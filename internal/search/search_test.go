package search

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/arrase/code-reducer/internal/security"
)

func writeTestDoc(t *testing.T, repoRoot, relPath, content string) {
	t.Helper()
	full := filepath.Join(repoRoot, filepath.FromSlash(relPath))
	if err := os.MkdirAll(filepath.Dir(full), 0755); err != nil {
		t.Fatalf("failed to create dir for %s: %v", relPath, err)
	}
	if err := os.WriteFile(full, []byte(content), 0644); err != nil {
		t.Fatalf("failed to write %s: %v", relPath, err)
	}
}

func TestSearchWiki_FencedHeadingsAndBM25Ranking(t *testing.T) {
	repoRoot := t.TempDir()

	writeTestDoc(t, repoRoot, "wiki/index.md", "# Documentation Index\n\n## Overview\n\nextractFileFacts index entry that should be ignored.\n")
	writeTestDoc(t, repoRoot, "wiki/.hidden.md", "# Hidden\n\n## Secret\n\nextractFileFacts hidden content.\n")

	engineDoc := `> **Module:** ` + "`internal/engine`" + ` | **Source Files:** ` + "`chunking.go`" + `, ` + "`synthesize.go`" + `

# internal/engine

Introductory overview of the engine package.

## Synthesis Pipeline

The synthesis pipeline runs ` + "`extractFileFacts`" + ` on each source file and caches extracted facts.

` + "```markdown" + `
## Fake Heading Inside Fence
This fake heading must not split the section.
` + "```" + `

Still inside Synthesis Pipeline after fenced block: ` + "`extractFileFacts`" + ` handles chunking.

## Text Reduction

Text reduction uses ` + "`chunkTextWithOverlap`" + ` in ` + "`chunking.go`" + ` to split large files.
`
	writeTestDoc(t, repoRoot, "wiki/modules/internal/engine/README.md", engineDoc)

	archDoc := `# Architecture Overview

## System Boundaries

Code-Reducer operates within the repository root and uses ` + "`SafeResolve`" + ` for security.
`
	writeTestDoc(t, repoRoot, "wiki/architecture.md", archDoc)

	results, err := SearchWiki(repoRoot, "wiki", "extractFileFacts", nil, 10)
	if err != nil {
		t.Fatalf("SearchWiki failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 matching section (index.md, .hidden.md, and fenced ## ignored), got %d: %+v", len(results), results)
	}

	top := results[0]
	if top.Target != "modules/internal/engine/README.md#synthesis-pipeline" {
		t.Errorf("expected target modules/internal/engine/README.md#synthesis-pipeline, got %s", top.Target)
	}
	if top.Heading != "Synthesis Pipeline" {
		t.Errorf("expected heading 'Synthesis Pipeline', got %q", top.Heading)
	}
	if len(top.Sources) != 2 || top.Sources[0] != "chunking.go" || top.Sources[1] != "synthesize.go" {
		t.Errorf("unexpected Sources: %v", top.Sources)
	}
	if !strings.Contains(top.Snippet, "extractFileFacts") {
		t.Errorf("expected snippet to mention extractFileFacts, got %q", top.Snippet)
	}
}

func TestSearchWiki_PathHintsBoost(t *testing.T) {
	repoRoot := t.TempDir()

	engineDoc := `> **Module:** ` + "`internal/engine`" + ` | **Source Files:** ` + "`chunking.go`" + `

# Engine

## Configuration Usage

Reads configuration values for pipeline execution.
`
	configDoc := `> **Module:** ` + "`internal/config`" + ` | **Source Files:** ` + "`resolve.go`" + `, ` + "`config.go`" + `

# Config

## Configuration Usage

Reads configuration values for pipeline execution.
`
	writeTestDoc(t, repoRoot, "wiki/modules/internal/engine/README.md", engineDoc)
	writeTestDoc(t, repoRoot, "wiki/modules/internal/config/README.md", configDoc)

	resultsHintEngine, err := SearchWiki(repoRoot, "wiki", "configuration values", []string{"internal/engine/chunking.go"}, 5)
	if err != nil {
		t.Fatalf("SearchWiki with engine hint failed: %v", err)
	}
	if len(resultsHintEngine) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(resultsHintEngine))
	}
	if !strings.HasPrefix(resultsHintEngine[0].Page, "modules/internal/engine/") {
		t.Errorf("expected engine module first when hinted, got %s", resultsHintEngine[0].Page)
	}

	resultsHintConfig, err := SearchWiki(repoRoot, "wiki", "configuration values", []string{"resolve.go"}, 5)
	if err != nil {
		t.Fatalf("SearchWiki with config hint failed: %v", err)
	}
	if len(resultsHintConfig) < 2 {
		t.Fatalf("expected at least 2 results, got %d", len(resultsHintConfig))
	}
	if !strings.HasPrefix(resultsHintConfig[0].Page, "modules/internal/config/") {
		t.Errorf("expected config module first when hinted, got %s", resultsHintConfig[0].Page)
	}
	if resultsHintConfig[0].Score <= resultsHintConfig[1].Score {
		t.Errorf("expected hinted score (%f) > non-hinted score (%f)", resultsHintConfig[0].Score, resultsHintConfig[1].Score)
	}
}

func TestSearchWiki_LimitAndEmptyQuery(t *testing.T) {
	repoRoot := t.TempDir()

	doc := `# Overview

## Section One
Alpha token appears here.

## Section Two
Alpha token appears here too.

## Section Three
Alpha token appears here as well.
`
	writeTestDoc(t, repoRoot, "wiki/architecture.md", doc)

	emptyRes, err := SearchWiki(repoRoot, "wiki", "   ", nil, 5)
	if err != nil {
		t.Fatalf("unexpected error on empty query: %v", err)
	}
	if len(emptyRes) != 0 {
		t.Errorf("expected 0 results on empty query, got %d", len(emptyRes))
	}

	limitedRes, err := SearchWiki(repoRoot, "wiki", "alpha", nil, 2)
	if err != nil {
		t.Fatalf("unexpected error on limited query: %v", err)
	}
	if len(limitedRes) != 2 {
		t.Errorf("expected limit=2 results, got %d", len(limitedRes))
	}
}

func TestReadWikiSection(t *testing.T) {
	repoRoot := t.TempDir()

	archContent := "# Architecture Overview\n\n## System Boundaries\n\nBoundary details.\n"
	writeTestDoc(t, repoRoot, "wiki/architecture.md", archContent)

	modContent := `> **Module:** ` + "`internal/engine`" + ` | **Source Files:** ` + "`orchestrator.go`" + `

# internal/engine

## Public API

Top-level API description.

### Functions

- ` + "`Run()`" + `
- ` + "`RunInit()`" + `

` + "```go" + `
// ## Not a heading inside code fence
func Run() {}
` + "```" + `

## Details

First details section.

## Details

Second duplicate details section.
`
	writeTestDoc(t, repoRoot, "wiki/modules/internal/engine/README.md", modContent)

	t.Run("read full page with and without docsDir prefix", func(t *testing.T) {
		got1, err := ReadWikiSection(repoRoot, "wiki", "architecture.md")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got1 != archContent {
			t.Errorf("expected full content, got %q", got1)
		}

		got2, err := ReadWikiSection(repoRoot, "wiki", "wiki/architecture.md")
		if err != nil {
			t.Fatalf("unexpected error with wiki/ prefix: %v", err)
		}
		if got2 != archContent {
			t.Errorf("expected full content with wiki/ prefix, got %q", got2)
		}
	})

	t.Run("read specific H2 section including nested H3 and fenced code", func(t *testing.T) {
		got, err := ReadWikiSection(repoRoot, "wiki", "modules/internal/engine/README.md#public-api")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !strings.HasPrefix(got, "## Public API") {
			t.Errorf("expected section to start with '## Public API', got %q", got)
		}
		if !strings.Contains(got, "### Functions") || !strings.Contains(got, "func Run() {}") {
			t.Errorf("expected nested H3 and code fence in section, got %q", got)
		}
		if strings.Contains(got, "First details section") {
			t.Errorf("expected section to stop before next H2, got %q", got)
		}
	})

	t.Run("read duplicate heading anchors", func(t *testing.T) {
		first, err := ReadWikiSection(repoRoot, "wiki", "modules/internal/engine/README.md#details")
		if err != nil {
			t.Fatalf("unexpected error reading #details: %v", err)
		}
		if !strings.Contains(first, "First details section.") || strings.Contains(first, "Second duplicate") {
			t.Errorf("unexpected content for #details: %q", first)
		}

		second, err := ReadWikiSection(repoRoot, "wiki", "modules/internal/engine/README.md#details-1")
		if err != nil {
			t.Fatalf("unexpected error reading #details-1: %v", err)
		}
		if !strings.Contains(second, "Second duplicate details section.") || strings.Contains(second, "First details") {
			t.Errorf("unexpected content for #details-1: %q", second)
		}
	})

	t.Run("error on missing anchor lists available anchors", func(t *testing.T) {
		_, err := ReadWikiSection(repoRoot, "wiki", "modules/internal/engine/README.md#non-existent")
		if err == nil {
			t.Fatalf("expected error for missing anchor, got nil")
		}
		if !strings.Contains(err.Error(), "public-api") || !strings.Contains(err.Error(), "details-1") {
			t.Errorf("expected error to list available anchors, got: %v", err)
		}
	})

	t.Run("error on path traversal", func(t *testing.T) {
		writeTestDoc(t, repoRoot, "secret.md", "# Secret outside wiki\n")

		for _, badTarget := range []string{"../../etc/passwd", "../secret.md", "/etc/passwd"} {
			_, err := ReadWikiSection(repoRoot, "wiki", badTarget)
			if err == nil {
				t.Errorf("expected error for traversal target %q, got nil", badTarget)
			}
			if !errors.Is(err, security.ErrPathTraversal) {
				t.Errorf("expected ErrPathTraversal for %q, got: %v", badTarget, err)
			}
		}
	})

	t.Run("reject hidden files and non-markdown files", func(t *testing.T) {
		writeTestDoc(t, repoRoot, "wiki/.metadata.json", `{"files":{}}`)
		writeTestDoc(t, repoRoot, "wiki/.hidden/secret.md", "# Hidden\n")
		writeTestDoc(t, repoRoot, "wiki/notes.txt", "plain text")

		for _, badTarget := range []string{".metadata.json", "wiki/.metadata.json", ".hidden/secret.md", "notes.txt"} {
			if _, err := ReadWikiSection(repoRoot, "wiki", badTarget); err == nil {
				t.Errorf("expected ReadWikiSection to reject %q, got nil", badTarget)
			}
		}
	})
}

func TestSearchWiki_AcronymCamelCaseAndH3Fallback(t *testing.T) {
	repoRoot := t.TempDir()

	doc := `# Markdown Utilities

### Heading Parser

Uses ` + "`parseATXHeading`" + ` to extract heading levels and titles.
`
	writeTestDoc(t, repoRoot, "wiki/modules/internal/tools/README.md", doc)

	results, err := SearchWiki(repoRoot, "wiki", "atx heading", nil, 5)
	if err != nil {
		t.Fatalf("SearchWiki failed: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result from H3-only doc via acronym camelCase split, got %d: %+v", len(results), results)
	}
	if results[0].Target != "modules/internal/tools/README.md#heading-parser" {
		t.Errorf("expected H3 fallback target modules/internal/tools/README.md#heading-parser, got %s", results[0].Target)
	}
}
