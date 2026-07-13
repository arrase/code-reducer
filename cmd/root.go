package cmd

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/mattn/go-isatty"
	"github.com/spf13/cobra"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/engine"
	"github.com/arrase/code-reducer/internal/tools"
)

var (
	modelIDFlag string
	numCtxFlag  string
)

var RootCmd = &cobra.Command{
	Use:               "code-reducer",
	Short:             "Code-Reducer is a documentation agent that writes and maintains a project wiki.",
	Long:              `A pure Go port of Code-Reducer CLI, optimized for performance and local LLM execution.`,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
	SilenceUsage:      true,
	SilenceErrors:     true,
}

func init() {
	RootCmd.PersistentFlags().StringVar(&modelIDFlag, "model-id", "", "Specify LLM model ID")
	RootCmd.PersistentFlags().StringVar(&numCtxFlag, "num-ctx", "", "Specify Ollama context window size")
}

func executeCommand(mode string) error {
	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %w", err)
	}

	if err := tools.VerifyGitRepo(repoRoot); err != nil {
		return err
	}

	needsSetup := !config.ConfigExists(repoRoot)
	isTTY := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())

	if needsSetup {
		if !isTTY {
			return fmt.Errorf("configuration file %s does not exist in the current directory. Please run 'code-reducer setup' to configure the application", config.ConfigFileName)
		}

		// Run implicit setup flow
		if err := RunSetupFlow(repoRoot); err != nil {
			return err
		}
	}

	// Resolve the merged configuration
	cfg, err := config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag)
	if err != nil {
		return err
	}

	// Command flow checks
	hasInit := engine.IsInitialized(repoRoot, cfg.DocsDir)

	if mode == "init" {
		if hasInit {
			return fmt.Errorf("the project has already been initialized. Please use 'code-reducer update' to refresh documentation")
		}
	} else if mode == "update" {
		if !hasInit {
			return fmt.Errorf("the project has not been initialized yet. Please run 'code-reducer init' first")
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := engine.NewRunner(cfg)
	err = runner.Run(ctx, repoRoot, engine.Mode(mode), func(ev engine.Event) {
		if ev.Type == engine.EventStatus {
			fmt.Println(ev.Message)
		} else if ev.Type == engine.EventError {
			fmt.Fprintf(os.Stderr, "Error: %s\n", ev.Message)
		} else {
			fmt.Println(ev.Message)
		}
	})

	if err != nil {
		return fmt.Errorf("documentation run failed: %w", err)
	}

	return nil
}
