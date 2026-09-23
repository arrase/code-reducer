package engine

import (
	"context"
	"strings"
	"testing"
)

func TestChunkTextWithOverlap(t *testing.T) {
	t.Run("invalid maxRunes", func(t *testing.T) {
		_, err := chunkTextWithOverlap("test", 0, 0)
		if err == nil {
			t.Fatal("expected error for maxRunes <= 0")
		}
	})

	t.Run("invalid overlapRunes", func(t *testing.T) {
		_, err := chunkTextWithOverlap("test", 10, 10)
		if err == nil {
			t.Fatal("expected error for overlapRunes >= maxRunes")
		}
	})

	t.Run("text shorter than maxRunes", func(t *testing.T) {
		chunks, err := chunkTextWithOverlap("hello", 10, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(chunks) != 1 || chunks[0] != "hello" {
			t.Fatalf("expected ['hello'], got %v", chunks)
		}
	})

	t.Run("text exactly maxRunes", func(t *testing.T) {
		chunks, err := chunkTextWithOverlap("hello", 5, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(chunks) != 1 || chunks[0] != "hello" {
			t.Fatalf("expected ['hello'], got %v", chunks)
		}
	})

	t.Run("chunking with overlap", func(t *testing.T) {
		chunks, err := chunkTextWithOverlap("abcdefg", 4, 2)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if len(chunks) != 3 {
			t.Fatalf("expected 3 chunks, got %d", len(chunks))
		}
		if chunks[0] != "abcd" {
			t.Errorf("expected chunk 0 to be 'abcd', got '%s'", chunks[0])
		}
		if chunks[1] != "cdef" {
			t.Errorf("expected chunk 1 to be 'cdef', got '%s'", chunks[1])
		}
		if chunks[2] != "efg" {
			t.Errorf("expected chunk 2 to be 'efg', got '%s'", chunks[2])
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
	limit1 := calculateFileLimit(100)
	if limit1 <= 0 {
		t.Fatalf("expected positive limit, got %d", limit1)
	}

	// Above floor
	limit2 := calculateFileLimit(8192)
	if limit2 <= limit1 {
		t.Fatalf("expected limit2 > limit1, got %d <= %d", limit2, limit1)
	}
}
