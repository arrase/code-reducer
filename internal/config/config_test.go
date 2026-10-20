package config

import (
	"os"
	"path/filepath"
	"strings"
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
		cfg := &Config{ModelID: "test-model", Language: "Spanish"}
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
		if loaded.Language != "Spanish" {
			t.Errorf("expected Spanish, got %s", loaded.Language)
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
		cfg := &Config{OllamaNumCtx: 2048, ModelID: "yaml-model", Language: "yaml-lang"}
		err := SaveConfig(dir, cfg)
		if err != nil {
			t.Fatalf("failed to save config: %v", err)
		}

		t.Setenv(OllamaNumCtxEnvKey, "4096")
		t.Setenv(CodeReducerModelIDEnvKey, "env-model")
		t.Setenv(CodeReducerLanguageEnvKey, "env-lang")

		resolved, err := ResolveConfig(dir, "flag-model", "8192", "flag-lang")
		if err != nil {
			t.Fatalf("failed to resolve config: %v", err)
		}

		if resolved.OllamaNumCtx != 8192 {
			t.Errorf("expected 8192, got %d", resolved.OllamaNumCtx)
		}
		if resolved.ModelID != "flag-model" {
			t.Errorf("expected flag-model, got %s", resolved.ModelID)
		}
		if resolved.Language != "flag-lang" {
			t.Errorf("expected flag-lang, got %s", resolved.Language)
		}
		if resolved.QuickstartPrompt != DefaultQuickstartPrompt {
			t.Errorf("expected default quickstart prompt, got %s", resolved.QuickstartPrompt)
		}
	})

	t.Run("language 4-tier resolution", func(t *testing.T) {
		dir := t.TempDir()

		// Tier 4: Default ("")
		resolved, err := ResolveConfig(dir, "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.Language != "" {
			t.Errorf("expected empty default language, got %q", resolved.Language)
		}

		// Tier 3: YAML config
		if err := SaveConfig(dir, &Config{Language: "Spanish"}); err != nil {
			t.Fatalf("failed to save config: %v", err)
		}
		resolved, err = ResolveConfig(dir, "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.Language != "Spanish" {
			t.Errorf("expected Spanish from YAML, got %q", resolved.Language)
		}

		// Tier 2: Environment variable overrides YAML
		t.Setenv(CodeReducerLanguageEnvKey, "French")
		resolved, err = ResolveConfig(dir, "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.Language != "French" {
			t.Errorf("expected French from env, got %q", resolved.Language)
		}

		// Tier 1: CLI flag overrides Env and YAML
		resolved, err = ResolveConfig(dir, "", "", "German")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.Language != "German" {
			t.Errorf("expected German from CLI flag, got %q", resolved.Language)
		}
	})

	t.Run("fail fast on invalid num ctx env", func(t *testing.T) {
		t.Setenv(OllamaNumCtxEnvKey, "invalid")
		_, err := ResolveConfig(t.TempDir(), "", "", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("fail fast on zero num ctx env", func(t *testing.T) {
		t.Setenv(OllamaNumCtxEnvKey, "0")
		_, err := ResolveConfig(t.TempDir(), "", "", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("fail fast on invalid num ctx flag", func(t *testing.T) {
		_, err := ResolveConfig(t.TempDir(), "", "invalid", "")
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("resolve base url from env", func(t *testing.T) {
		t.Setenv(OllamaBaseURLEnvKey, "http://custom-ollama:11434")
		resolved, err := ResolveConfig(t.TempDir(), "", "", "")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.OllamaBaseURL != "http://custom-ollama:11434" {
			t.Errorf("expected http://custom-ollama:11434, got %s", resolved.OllamaBaseURL)
		}
	})
}

func TestLanguageInstruction(t *testing.T) {
	if got := LanguageInstruction(""); got != "" {
		t.Errorf("expected empty string for empty language, got %q", got)
	}
	if got := LanguageInstruction("   "); got != "" {
		t.Errorf("expected empty string for whitespace language, got %q", got)
	}

	got := LanguageInstruction("Spanish")
	if !strings.Contains(got, "Write all documentation prose, headings, and table descriptions in Spanish.") {
		t.Errorf("missing language prose instruction in %q", got)
	}
	if !strings.Contains(got, "Keep all code identifiers, function signatures, file paths, CLI commands, environment variables, and Mermaid syntax keywords unchanged.") {
		t.Errorf("missing identifier preservation instruction in %q", got)
	}
}
