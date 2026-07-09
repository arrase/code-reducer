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
	modelIdFlag string
	numCtxFlag  string
)

var RootCmd = &cobra.Command{
	Use:               "code-reducer",
	Short:             "Code-Reducer is a documentation agent that writes and maintains a project wiki.",
	Long:              `A pure Go port of Code-Reducer CLI, optimized for performance and local LLM execution.`,
	CompletionOptions: cobra.CompletionOptions{DisableDefaultCmd: true},
}

func init() {
	RootCmd.PersistentFlags().StringVar(&modelIdFlag, "model-id", "", "Specify LLM model ID")
	RootCmd.PersistentFlags().StringVar(&numCtxFlag, "num-ctx", "", "Specify Ollama context window size")
}

func executeCommand(mode string) {
	repoRoot, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current working directory: %v\n", err)
		os.Exit(1)
	}

	if err := tools.VerifyGitRepo(repoRoot); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %v\n", err)
		os.Exit(1)
	}

	needsSetup := !config.ConfigExists(repoRoot)
	isTTY := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())

	if needsSetup {
		if !isTTY {
			fmt.Printf("Error: Configuration file %s does not exist in the current directory. Please run 'code-reducer setup' to configure the application.\n", config.ConfigFileName)
			os.Exit(1)
		}

		// Run implicit setup flow
		RunSetupFlow(repoRoot)
	}

	// Resolve the merged configuration
	cfg := config.ResolveConfig(repoRoot, modelIdFlag, numCtxFlag)

	// Command flow checks
	lastSHA, hasInit := engine.GetLastDocumentedCommit(repoRoot, cfg.DocsDir)

	if mode == "init" {
		if hasInit {
			fmt.Fprintf(os.Stderr, "Error: The project has already been initialized (last documented commit: %s). Please use 'code-reducer update' to refresh documentation.\n", lastSHA)
			os.Exit(1)
		}
	} else if mode == "update" {
		if !hasInit {
			fmt.Fprintf(os.Stderr, "Error: The project has not been initialized yet. Please run 'code-reducer init' first.\n")
			os.Exit(1)
		}
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := engine.NewRunner(cfg)
	err = runner.Run(ctx, repoRoot, mode, func(ev engine.Event) {
		if ev.Type == "status" {
			fmt.Println(ev.Message)
		} else if ev.Type == "error" {
			fmt.Fprintf(os.Stderr, "Error: %s\n", ev.Message)
		} else {
			fmt.Println(ev.Message)
		}
	})

	if err != nil {
		fmt.Printf("Documentation Run Failed: %v\n", err)
		os.Exit(1)
	}
}
