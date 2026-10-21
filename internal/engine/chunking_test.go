package engine

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func assertChunks(t *testing.T, text string, maxRunes, overlapRunes int, want ...string) {
	t.Helper()
	chunks, err := chunkTextWithOverlap(text, maxRunes, overlapRunes)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !slices.Equal(chunks, want) {
		t.Fatalf("expected %q, got %q", want, chunks)
	}
}

func TestChunkTextWithOverlap(t *testing.T) {
	t.Run("invalid maxRunes", func(t *testing.T) {
		_, err := chunkTextWithOverlap("test", 0, 0)
		if err == nil {
			t.Fatal("expected error for maxRunes <= 0")
		}
	})

	t.Run("invalid overlapRunes", func(t *testing.T) {
		if _, err := chunkTextWithOverlap("test", 10, 10); err == nil {
			t.Fatal("expected error for overlapRunes >= maxRunes")
		}
		if _, err := chunkTextWithOverlap("test", 10, -1); err == nil {
			t.Fatal("expected error for overlapRunes < 0")
		}
	})

	t.Run("text shorter than maxRunes", func(t *testing.T) {
		assertChunks(t, "hello", 10, 2, "hello")
	})

	t.Run("text exactly maxRunes", func(t *testing.T) {
		assertChunks(t, "hello", 5, 2, "hello")
	})

	t.Run("chunking with overlap", func(t *testing.T) {
		assertChunks(t, "abcdefg", 4, 2, "abcd", "cdef", "efg")
	})

	t.Run("chunking splits at last newline in tail window", func(t *testing.T) {
		assertChunks(t, "line1\nline2\nline3\n", 14, 2, "line1\nline2\n", "2\nline3\n")
	})
}

func TestStripOuterMarkdownFence(t *testing.T) {
	t.Run("strips single outer markdown fence", func(t *testing.T) {
		input := "```markdown\n# Title\nBody\n```"
		got := stripOuterMarkdownFence(input)
		if got != "# Title\nBody" {
			t.Errorf("expected '# Title\\nBody', got %q", got)
		}
	})

	t.Run("preserves document with multiple code blocks", func(t *testing.T) {
		input := "```go\nfunc a() {}\n```\n\nSome text\n\n```go\nfunc b() {}\n```"
		got := stripOuterMarkdownFence(input)
		if got != input {
			t.Errorf("expected multiple code blocks to remain intact, got %q", got)
		}
	})

	t.Run("leaves plain text untouched", func(t *testing.T) {
		input := "# Just Markdown\nSome content"
		if got := stripOuterMarkdownFence(input); got != input {
			t.Errorf("expected %q, got %q", input, got)
		}
	})
}

func TestReduceItems(t *testing.T) {
	ctx := context.Background()
	items := []string{"item1", "item2", "item3", "item4", "item5"}
	maxChars := 15 // Enough for "item1item2" but not 3 items

	reduceFn := func(batch []string) (string, error) {
		return strings.Join(batch, "-"), nil
	}

	res, err := reduceItems(ctx, items, maxChars, reduceFn)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, item := range items {
		if !strings.Contains(res, item) {
			t.Errorf("expected result to contain %s, got %s", item, res)
		}
	}
	if !strings.Contains(res, "Summarize and consolidate the key technical facts from this partial batch without repeating boilerplate headers") {
		t.Errorf("expected intermediate partial batch instruction in result, got %s", res)
	}
}

func TestExpandOversizedItems(t *testing.T) {
	items := []string{"small", "this is a very long item that exceeds limit"}
	expanded, err := expandOversizedItems(items, 10)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(expanded) <= 2 {
		t.Fatalf("expected more than 2 items after expansion, got %d", len(expanded))
	}
	if expanded[0] != "small" {
		t.Fatalf("expected first item 'small', got %s", expanded[0])
	}
}

func TestBatchItems(t *testing.T) {
	items := []string{"aaa", "bbb", "ccc"}
	batches := batchItems(items, 6)
	if len(batches) != 2 {
		t.Fatalf("expected 2 batches, got %d", len(batches))
	}
}

func TestCountRunes(t *testing.T) {
	items := []string{"hello", "world"}
	if total := countRunes(items); total != 10 {
		t.Fatalf("expected 10 runes, got %d", total)
	}
}

func TestCalculateFileLimit(t *testing.T) {
	// Below floor
	limit1 := calculateFileLimit(100, "system prompt")
	if limit1 != defaultChunkOverlap*2 {
		t.Fatalf("expected floor %d, got %d", defaultChunkOverlap*2, limit1)
	}

	// Above floor, subtracting system prompt length
	limit2 := calculateFileLimit(8192, "1234567890")
	expected := 8192*maxCharsMultiplier - 10
	if limit2 != expected {
		t.Fatalf("expected %d, got %d", expected, limit2)
	}
}
