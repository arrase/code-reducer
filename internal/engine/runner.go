package engine

import (
	"context"
	"fmt"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/security"
)

type Runner struct {
	cfg *config.Config
}

func NewRunner(cfg *config.Config) *Runner {
	return &Runner{
		cfg: cfg,
	}
}

func (r *Runner) Run(ctx context.Context, repoRoot string, mode string, onEvent func(Event)) error {
	logEvent := func(t, m string) {
		if onEvent != nil {
			onEvent(Event{Type: t, Message: m})
		}
	}

	// 1. Ensure lockfile is in gitignore
	if err := security.EnsureGitignoreHasLockfile(repoRoot); err != nil {
		logEvent("status", fmt.Sprintf("Warning: failed to ensure gitignore has lockfile: %v", err))
	}

	// 2. Acquire repository lock
	lock, err := security.AcquireLock(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to acquire repository lock: %w", err)
	}
	defer lock.Unlock()

	// 3. Instantiate LLM Client
	client := NewLLMClient(r.cfg.ModelID, r.cfg.OllamaBaseURL, r.cfg.OllamaNumCtx)

	// 4. Run the documentation pipeline
	if mode == "init" {
		if err := client.RunInit(ctx, repoRoot, r.cfg, onEvent); err != nil {
			return err
		}
	} else if mode == "update" {
		if err := client.RunUpdate(ctx, repoRoot, r.cfg, onEvent); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("unsupported mode: %s", mode)
	}

	return nil
}
