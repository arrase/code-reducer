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
	modelIDFlag  string
	numCtxFlag   string
	languageFlag string
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
	RootCmd.PersistentFlags().StringVar(&languageFlag, "language", "", "Target language for generated documentation (e.g. English, Spanish, es, en)")
}

func executeCommand(mode engine.RunMode) error {
	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("failed to get current working directory: %w", err)
	}

	if err := tools.VerifyGitRepo(repoRoot); err != nil {
		return err
	}

	if err := checkAndRunSetup(repoRoot); err != nil {
		return err
	}

	cfg, err := config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag, languageFlag)
	if err != nil {
		return err
	}

	if err := checkInitStatus(repoRoot, cfg.DocsDir, mode); err != nil {
		return err
	}

	return runEngine(repoRoot, cfg, mode)
}

func checkAndRunSetup(repoRoot string) error {
	if config.ConfigExists(repoRoot) {
		return nil
	}
	isTTY := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())
	if !isTTY {
		return fmt.Errorf("configuration file %s does not exist in the current directory. Please run 'code-reducer setup' to configure the application", config.ConfigFileName)
	}
	return RunSetupFlow(repoRoot)
}

func checkInitStatus(repoRoot, docsDir string, mode engine.RunMode) error {
	hasInit := engine.IsInitialized(repoRoot, docsDir)

	if mode == engine.ModeInit && hasInit {
		return fmt.Errorf("the project has already been initialized. Please use 'code-reducer update' to refresh documentation")
	}
	if mode == engine.ModeUpdate && !hasInit {
		return fmt.Errorf("the project has not been initialized yet. Please run 'code-reducer init' first")
	}

	return nil
}

func runEngine(repoRoot string, cfg *config.Config, mode engine.RunMode) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := engine.Run(ctx, repoRoot, cfg, mode, func(msg string) { fmt.Println(msg) }); err != nil {
		return fmt.Errorf("documentation run failed: %w", err)
	}

	return nil
}
