package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadConfig(t *testing.T) {
	t.Run("non-existent file", func(t *testing.T) {
		_, err := LoadConfig(t.TempDir())
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("valid file", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &Config{ModelID: "test-model"}
		err := SaveConfig(dir, cfg)
		if err != nil {
			t.Fatalf("failed to save config: %v", err)
		}

		loaded, err := LoadConfig(dir)
		if err != nil {
			t.Fatalf("failed to load config: %v", err)
		}
		if loaded.ModelID != "test-model" {
			t.Errorf("expected test-model, got %s", loaded.ModelID)
		}
	})

	t.Run("invalid YAML", func(t *testing.T) {
		dir := t.TempDir()
		err := os.WriteFile(filepath.Join(dir, ConfigFileName), []byte("invalid: yaml: content:"), 0644)
		if err != nil {
			t.Fatalf("failed to write test file: %v", err)
		}

		_, err = LoadConfig(dir)
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}

func TestResolveConfig(t *testing.T) {
	t.Run("valid config resolution order", func(t *testing.T) {
		dir := t.TempDir()
		cfg := &Config{OllamaNumCtx: 2048, ModelID: "yaml-model"}
		err := SaveConfig(dir, cfg)
		if err != nil {
			t.Fatalf("failed to save config: %v", err)
		}

		t.Setenv(OllamaNumCtxEnvKey, "4096")
		t.Setenv(CodeReducerModelIDEnvKey, "env-model")

		resolved, err := ResolveConfig(dir, "flag-model", "8192")
		if err != nil {
			t.Fatalf("failed to resolve config: %v", err)
		}

		if resolved.OllamaNumCtx != 8192 {
			t.Errorf("expected 8192, got %d", resolved.OllamaNumCtx)
		}
		if resolved.ModelID != "flag-model" {
			t.Errorf("expected flag-model, got %s", resolved.ModelID)
		}
	})

	t.Run("fail fast on invalid num ctx env", func(t *testing.T) {
		t.Setenv(OllamaNumCtxEnvKey, "invalid")
		_, err := ResolveConfig(t.TempDir(), "", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("fail fast on zero num ctx env", func(t *testing.T) {
		t.Setenv(OllamaNumCtxEnvKey, "0")
		_, err := ResolveConfig(t.TempDir(), "", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("fail fast on invalid num ctx flag", func(t *testing.T) {
		_, err := ResolveConfig(t.TempDir(), "", "invalid")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})
}
