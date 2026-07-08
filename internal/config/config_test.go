package config

import (
	"os"
	"testing"
)

func TestConfigFlow(t *testing.T) {
	tmpDir, err := os.MkdirTemp("", "code-reducer-test")
	if err != nil {
		t.Fatalf("failed to create temp dir: %v", err)
	}
	defer os.RemoveAll(tmpDir)

	// 1. Verify ConfigExists is false initially
	if ConfigExists(tmpDir) {
		t.Errorf("ConfigExists should be false for empty directory")
	}

	// 2. Save a configuration
	cfg := &Config{
		ModelID:            "test-model",
		OllamaBaseURL:      "http://test-host:11434",
		OllamaNumCtx:       4096,
		LangsmithAPIKey:    "test-key",
		LangchainProject:   "test-project",
		LangchainTracingV2: "true",
	}

	err = SaveConfig(tmpDir, cfg)
	if err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// 3. Verify ConfigExists is now true
	if !ConfigExists(tmpDir) {
		t.Errorf("ConfigExists should be true after SaveConfig")
	}

	// 4. Load configuration
	loaded, err := LoadConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadConfig failed: %v", err)
	}

	if loaded.ModelID != cfg.ModelID {
		t.Errorf("expected ModelID %q, got %q", cfg.ModelID, loaded.ModelID)
	}
	if loaded.OllamaBaseURL != cfg.OllamaBaseURL {
		t.Errorf("expected OllamaBaseURL %q, got %q", cfg.OllamaBaseURL, loaded.OllamaBaseURL)
	}
	if loaded.OllamaNumCtx != cfg.OllamaNumCtx {
		t.Errorf("expected OllamaNumCtx %d, got %d", cfg.OllamaNumCtx, loaded.OllamaNumCtx)
	}

	// 5. Test LoadAndApplyConfig
	// Clean environment variables first to avoid cross-contamination
	os.Unsetenv(CodeReducerModelIdEnvKey)
	os.Unsetenv(OllamaBaseUrlEnvKey)
	os.Unsetenv(OllamaNumCtxEnvKey)

	err = LoadAndApplyConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadAndApplyConfig failed: %v", err)
	}

	if os.Getenv(CodeReducerModelIdEnvKey) != "test-model" {
		t.Errorf("expected env %s to be set to %q", CodeReducerModelIdEnvKey, "test-model")
	}
	if os.Getenv(OllamaBaseUrlEnvKey) != "http://test-host:11434" {
		t.Errorf("expected env %s to be set to %q", OllamaBaseUrlEnvKey, "http://test-host:11434")
	}
	if os.Getenv(OllamaNumCtxEnvKey) != "4096" {
		t.Errorf("expected env %s to be set to %q", OllamaNumCtxEnvKey, "4096")
	}

	// 6. Test environment override priority: parent env should not be overwritten
	os.Setenv(CodeReducerModelIdEnvKey, "parent-override")
	err = LoadAndApplyConfig(tmpDir)
	if err != nil {
		t.Fatalf("LoadAndApplyConfig failed: %v", err)
	}

	if os.Getenv(CodeReducerModelIdEnvKey) != "parent-override" {
		t.Errorf("expected env %s to remain %q (parent override), got %q", CodeReducerModelIdEnvKey, "parent-override", os.Getenv(CodeReducerModelIdEnvKey))
	}
}
