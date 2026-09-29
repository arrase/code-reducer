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

		resolved, err := ResolveConfig(dir, Flags{ModelID: "flag-model", NumCtx: "8192"})
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
		_, err := ResolveConfig(t.TempDir(), Flags{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("fail fast on zero num ctx env", func(t *testing.T) {
		t.Setenv(OllamaNumCtxEnvKey, "0")
		_, err := ResolveConfig(t.TempDir(), Flags{})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("fail fast on invalid num ctx flag", func(t *testing.T) {
		_, err := ResolveConfig(t.TempDir(), Flags{NumCtx: "invalid"})
		if err == nil {
			t.Fatal("expected error, got nil")
		}
	})

	t.Run("resolve base url from env", func(t *testing.T) {
		t.Setenv(OllamaBaseURLEnvKey, "http://custom-ollama:11434")
		resolved, err := ResolveConfig(t.TempDir(), Flags{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.OllamaBaseURL != "http://custom-ollama:11434" {
			t.Errorf("expected http://custom-ollama:11434, got %s", resolved.OllamaBaseURL)
		}
	})
}

func TestResolveConfigGenerationSettings(t *testing.T) {
	t.Run("defaults", func(t *testing.T) {
		resolved, err := ResolveConfig(t.TempDir(), Flags{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.Think != ThinkDefault {
			t.Errorf("expected Think %v, got %v", ThinkDefault, resolved.Think)
		}
		if resolved.NumPredict != NumPredictDefault {
			t.Errorf("expected NumPredict %d, got %d", NumPredictDefault, resolved.NumPredict)
		}
		if resolved.SlotNumPredict != SlotNumPredictDefault {
			t.Errorf("expected SlotNumPredict %d, got %d", SlotNumPredictDefault, resolved.SlotNumPredict)
		}
		if resolved.ParagraphNumPredict != ParagraphNumPredictDefault {
			t.Errorf("expected ParagraphNumPredict %d, got %d", ParagraphNumPredictDefault, resolved.ParagraphNumPredict)
		}
		if resolved.CharsPerToken != CharsPerTokenDefault {
			t.Errorf("expected CharsPerToken %v, got %v", CharsPerTokenDefault, resolved.CharsPerToken)
		}
		if resolved.OutputTokenReserve != OutputTokenReserveDefault {
			t.Errorf("expected OutputTokenReserve %d, got %d", OutputTokenReserveDefault, resolved.OutputTokenReserve)
		}
		if resolved.IncludeTests != IncludeTestsDefault {
			t.Errorf("expected IncludeTests %v, got %v", IncludeTestsDefault, resolved.IncludeTests)
		}
	})

	t.Run("yaml values applied", func(t *testing.T) {
		dir := t.TempDir()
		err := SaveConfig(dir, &Config{
			Think:               true,
			NumPredict:          2048,
			SlotNumPredict:      256,
			ParagraphNumPredict: 1536,
			CharsPerToken:       2.5,
			OutputTokenReserve:  512,
			IncludeTests:        true,
		})
		if err != nil {
			t.Fatalf("failed to save config: %v", err)
		}

		resolved, err := ResolveConfig(dir, Flags{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resolved.Think {
			t.Error("expected Think true from yaml, got false")
		}
		if resolved.NumPredict != 2048 {
			t.Errorf("expected 2048, got %d", resolved.NumPredict)
		}
		if resolved.SlotNumPredict != 256 {
			t.Errorf("expected 256, got %d", resolved.SlotNumPredict)
		}
		if resolved.ParagraphNumPredict != 1536 {
			t.Errorf("expected 1536, got %d", resolved.ParagraphNumPredict)
		}
		if resolved.CharsPerToken != 2.5 {
			t.Errorf("expected 2.5, got %v", resolved.CharsPerToken)
		}
		if resolved.OutputTokenReserve != 512 {
			t.Errorf("expected 512, got %d", resolved.OutputTokenReserve)
		}
		if !resolved.IncludeTests {
			t.Error("expected IncludeTests true from yaml, got false")
		}
	})

	t.Run("env overrides yaml", func(t *testing.T) {
		dir := t.TempDir()
		err := SaveConfig(dir, &Config{NumPredict: 2048, SlotNumPredict: 256, ParagraphNumPredict: 256, IncludeTests: true})
		if err != nil {
			t.Fatalf("failed to save config: %v", err)
		}

		t.Setenv(CodeReducerThinkEnvKey, "true")
		t.Setenv(OllamaNumPredictEnvKey, "4096")
		t.Setenv(CodeReducerSlotNumPredictEnvKey, "192")
		t.Setenv(CodeReducerParagraphNumPredictEnvKey, "1920")
		t.Setenv(CodeReducerIncludeTestsEnvKey, "false")

		resolved, err := ResolveConfig(dir, Flags{})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if !resolved.Think {
			t.Error("expected Think true from env, got false")
		}
		if resolved.NumPredict != 4096 {
			t.Errorf("expected 4096, got %d", resolved.NumPredict)
		}
		if resolved.SlotNumPredict != 192 {
			t.Errorf("expected 192, got %d", resolved.SlotNumPredict)
		}
		if resolved.ParagraphNumPredict != 1920 {
			t.Errorf("expected 1920, got %d", resolved.ParagraphNumPredict)
		}
		if resolved.IncludeTests {
			t.Error("expected IncludeTests false from env, got true")
		}
	})

	t.Run("flags override env", func(t *testing.T) {
		t.Setenv(CodeReducerThinkEnvKey, "true")
		t.Setenv(OllamaNumPredictEnvKey, "4096")
		t.Setenv(CodeReducerSlotNumPredictEnvKey, "192")
		t.Setenv(CodeReducerParagraphNumPredictEnvKey, "1920")

		resolved, err := ResolveConfig(t.TempDir(), Flags{
			Think:               "false",
			NumPredict:          "1024",
			SlotNumPredict:      "64",
			ParagraphNumPredict: "800",
			CharsPerToken:       "3.5",
			OutputTokenReserve:  "256",
			IncludeTests:        "true",
		})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if resolved.Think {
			t.Error("expected Think false from flag, got true")
		}
		if resolved.NumPredict != 1024 {
			t.Errorf("expected 1024, got %d", resolved.NumPredict)
		}
		if resolved.SlotNumPredict != 64 {
			t.Errorf("expected 64, got %d", resolved.SlotNumPredict)
		}
		if resolved.ParagraphNumPredict != 800 {
			t.Errorf("expected 800, got %d", resolved.ParagraphNumPredict)
		}
		if resolved.CharsPerToken != 3.5 {
			t.Errorf("expected 3.5, got %v", resolved.CharsPerToken)
		}
		if resolved.OutputTokenReserve != 256 {
			t.Errorf("expected 256, got %d", resolved.OutputTokenReserve)
		}
		if !resolved.IncludeTests {
			t.Error("expected IncludeTests true from flag, got false")
		}
	})

	t.Run("fail fast on invalid generation settings", func(t *testing.T) {
		cases := []struct {
			name   string
			env    string
			envVal string
			flags  Flags
		}{
			{name: "invalid think env", env: CodeReducerThinkEnvKey, envVal: "maybe"},
			{name: "invalid num predict env", env: OllamaNumPredictEnvKey, envVal: "-1"},
			{name: "invalid slot num predict env", env: CodeReducerSlotNumPredictEnvKey, envVal: "0"},
			{name: "invalid slot num predict env text", env: CodeReducerSlotNumPredictEnvKey, envVal: "many"},
			{name: "negative paragraph num predict env", env: CodeReducerParagraphNumPredictEnvKey, envVal: "-1"},
			{name: "invalid paragraph num predict env text", env: CodeReducerParagraphNumPredictEnvKey, envVal: "many"},
			{name: "invalid think flag", flags: Flags{Think: "maybe"}},
			{name: "invalid num predict flag", flags: Flags{NumPredict: "-1"}},
			{name: "invalid slot num predict flag", flags: Flags{SlotNumPredict: "0"}},
			{name: "invalid paragraph num predict flag", flags: Flags{ParagraphNumPredict: "0"}},
			{name: "negative paragraph num predict flag", flags: Flags{ParagraphNumPredict: "-1"}},
			{name: "invalid chars per token flag", flags: Flags{CharsPerToken: "0"}},
			{name: "invalid chars per token flag text", flags: Flags{CharsPerToken: "many"}},
			{name: "invalid output token reserve flag", flags: Flags{OutputTokenReserve: "-5"}},
			{name: "invalid include tests env", env: CodeReducerIncludeTestsEnvKey, envVal: "sometimes"},
			{name: "invalid include tests flag", flags: Flags{IncludeTests: "sometimes"}},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				if tc.env != "" {
					t.Setenv(tc.env, tc.envVal)
				}
				if _, err := ResolveConfig(t.TempDir(), tc.flags); err == nil {
					t.Fatal("expected error, got nil")
				}
			})
		}
	})
}
