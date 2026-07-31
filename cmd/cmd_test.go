package cmd

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/arrase/code-reducer/internal/engine"
)

func TestRootFlags(t *testing.T) {
	// reset flags
	modelIDFlag = ""
	numCtxFlag = ""

	RootCmd.SetArgs([]string{"--model-id", "test-model", "--num-ctx", "4096", "help"}) // run something harmless like help
	_ = RootCmd.Execute()

	if modelIDFlag != "test-model" {
		t.Errorf("Expected modelIDFlag to be 'test-model', got '%s'", modelIDFlag)
	}

	if numCtxFlag != "4096" {
		t.Errorf("Expected numCtxFlag to be '4096', got '%s'", numCtxFlag)
	}
}

func TestCheckInitStatus(t *testing.T) {
	repoRoot := t.TempDir()
	docsDir := filepath.Join(repoRoot, "docs")
	docsRelDir := "docs" // engine expects relative or abs, let's use what checkInitStatus expects

	// Test init before it is initialized
	err := checkInitStatus(repoRoot, docsRelDir, engine.ModeInit)
	if err != nil {
		t.Errorf("Expected nil error for init on uninitialized project, got %v", err)
	}

	// Test update before it is initialized
	err = checkInitStatus(repoRoot, docsRelDir, engine.ModeUpdate)
	if err == nil {
		t.Errorf("Expected error for update on uninitialized project, got nil")
	}

	// Mock initialized state by creating docs dir and metadata file
	err = os.MkdirAll(docsDir, 0755)
	if err != nil {
		t.Fatalf("Failed to create docs dir: %v", err)
	}
	stateFile := filepath.Join(docsDir, ".metadata.json")
	err = os.WriteFile(stateFile, []byte("{}"), 0644)
	if err != nil {
		t.Fatalf("Failed to create metadata file: %v", err)
	}

	// Test init after it is initialized
	err = checkInitStatus(repoRoot, docsRelDir, engine.ModeInit)
	if err == nil {
		t.Errorf("Expected error for init on initialized project, got nil")
	}

	// Test update after it is initialized
	err = checkInitStatus(repoRoot, docsRelDir, engine.ModeUpdate)
	if err != nil {
		t.Errorf("Expected nil error for update on initialized project, got %v", err)
	}
}

func TestPromptString(t *testing.T) {
	input := "test-input\n"
	reader := bufio.NewReader(bytes.NewBufferString(input))

	result, err := promptString(reader, "Prompt", "default")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result != "test-input" {
		t.Errorf("Expected 'test-input', got '%s'", result)
	}

	// Test empty input falls back to default
	input = "\n"
	reader = bufio.NewReader(bytes.NewBufferString(input))
	result, err = promptString(reader, "Prompt", "default")
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if result != "default" {
		t.Errorf("Expected 'default', got '%s'", result)
	}
}

func TestPromptStringList(t *testing.T) {
	input := "a, b, c\n"
	reader := bufio.NewReader(bytes.NewBufferString(input))

	result, modified, err := promptStringList(reader, "Prompt", []string{"default"})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if !modified {
		t.Errorf("Expected modified to be true")
	}
	if len(result) != 3 || result[0] != "a" || result[1] != "b" || result[2] != "c" {
		t.Errorf("Unexpected result: %v", result)
	}

	// Test empty input falls back
	input = "\n"
	reader = bufio.NewReader(bytes.NewBufferString(input))
	result, modified, err = promptStringList(reader, "Prompt", []string{"default"})
	if err != nil {
		t.Errorf("Unexpected error: %v", err)
	}
	if modified {
		t.Errorf("Expected modified to be false")
	}
	if len(result) != 1 || result[0] != "default" {
		t.Errorf("Unexpected result: %v", result)
	}
}
