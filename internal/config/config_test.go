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
		if err := SaveConfig(dir, &Config{OllamaNumCtx: 2048, ModelID: "yaml-model", Language: "yaml-lang"}); err != nil {
			t.Fatalf("failed to save config: %v", err)
		}

		t.Setenv(OllamaNumCtxEnvKey, "4096")
		t.Setenv(CodeReducerModelIDEnvKey, "env-model")
		t.Setenv(CodeReducerLanguageEnvKey, "env-lang")

		resolved := mustResolveConfig(t, dir, "flag-model", "8192", "flag-lang")
		assertConfigField(t, "OllamaNumCtx", resolved.OllamaNumCtx, 8192)
		assertConfigField(t, "ModelID", resolved.ModelID, "flag-model")
		assertConfigField(t, "Language", resolved.Language, "flag-lang")
		assertConfigField(t, "QuickstartPrompt", resolved.QuickstartPrompt, DefaultQuickstartPrompt)
	})

	t.Run("language 4-tier resolution", func(t *testing.T) {
		dir := t.TempDir()

		// Tier 4: Default ("")
		assertResolvedLanguage(t, dir, "", "")

		// Tier 3: YAML config
		if err := SaveConfig(dir, &Config{Language: "Spanish"}); err != nil {
			t.Fatalf("failed to save config: %v", err)
		}
		assertResolvedLanguage(t, dir, "", "Spanish")

		// Tier 2: Environment variable overrides YAML
		t.Setenv(CodeReducerLanguageEnvKey, "French")
		assertResolvedLanguage(t, dir, "", "French")

		// Tier 1: CLI flag overrides Env and YAML
		assertResolvedLanguage(t, dir, "German", "German")
	})

	t.Run("fail fast on invalid num ctx env", func(t *testing.T) {
		t.Setenv(OllamaNumCtxEnvKey, "invalid")
		assertResolveError(t, t.TempDir(), "", "", "")
	})

	t.Run("fail fast on zero num ctx env", func(t *testing.T) {
		t.Setenv(OllamaNumCtxEnvKey, "0")
		assertResolveError(t, t.TempDir(), "", "", "")
	})

	t.Run("fail fast on invalid num ctx flag", func(t *testing.T) {
		assertResolveError(t, t.TempDir(), "", "invalid", "")
	})

	t.Run("resolve base url from env", func(t *testing.T) {
		t.Setenv(OllamaBaseURLEnvKey, "http://custom-ollama:11434")
		resolved := mustResolveConfig(t, t.TempDir(), "", "", "")
		assertConfigField(t, "OllamaBaseURL", resolved.OllamaBaseURL, "http://custom-ollama:11434")
	})
}

func mustResolveConfig(t *testing.T, dir, flagModel, flagCtx, flagLang string) *Config {
	t.Helper()
	resolved, err := ResolveConfig(dir, flagModel, flagCtx, flagLang)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	return resolved
}

func assertResolveError(t *testing.T, dir, flagModel, flagCtx, flagLang string) {
	t.Helper()
	if _, err := ResolveConfig(dir, flagModel, flagCtx, flagLang); err == nil {
		t.Fatal("expected error, got nil")
	}
}

func assertConfigField[T comparable](t *testing.T, name string, got, want T) {
	t.Helper()
	if got != want {
		t.Errorf("%s: expected %v, got %v", name, want, got)
	}
}

func assertResolvedLanguage(t *testing.T, dir, flagLang, want string) {
	t.Helper()
	resolved := mustResolveConfig(t, dir, "", "", flagLang)
	assertConfigField(t, "Language", resolved.Language, want)
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
