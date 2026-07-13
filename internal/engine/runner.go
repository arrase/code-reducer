package engine

import (
	"context"
	"fmt"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/security"
)

// Mode represents the operation mode of the documentation pipeline.
type Mode string

const (
	// ModeInit initializes the project documentation.
	ModeInit   Mode = "init"
	// ModeUpdate updates the existing project documentation incrementally.
	ModeUpdate Mode = "update"
)

// Runner orchestrates the execution of the documentation pipeline.
type Runner struct {
	cfg *config.Config
}

// NewRunner creates a new Runner instance with the given configuration.
func NewRunner(cfg *config.Config) *Runner {
	return &Runner{
		cfg: cfg,
	}
}

// Run executes the documentation pipeline for the specified mode.
func (r *Runner) Run(ctx context.Context, repoRoot string, mode Mode, onEvent func(Event)) error {
	logEvent := makeLogEvent(onEvent)

	// 1. Ensure lockfile is in gitignore
	if err := security.EnsureGitignoreHasLockfile(repoRoot); err != nil {
		logEvent(EventStatus, fmt.Sprintf("Warning: failed to ensure gitignore has lockfile: %v", err))
	}

	// 2. Acquire repository lock
	lock, err := security.AcquireLock(repoRoot)
	if err != nil {
		return fmt.Errorf("failed to acquire repository lock: %w", err)
	}
	defer lock.Unlock()

	// 3. Instantiate LLM Client & Orchestrator
	client := newLLMClient(r.cfg.ModelID, r.cfg.OllamaBaseURL, r.cfg.OllamaNumCtx)
	orch := &orchestrator{client: client}

	// 4. Run the documentation pipeline
	if mode == ModeInit {
		if err := orch.RunInit(ctx, repoRoot, r.cfg, onEvent); err != nil {
			return err
		}
	} else if mode == ModeUpdate {
		if err := orch.RunUpdate(ctx, repoRoot, r.cfg, onEvent); err != nil {
			return err
		}
	} else {
		return fmt.Errorf("unsupported mode: %s", mode)
	}

	return nil
}
