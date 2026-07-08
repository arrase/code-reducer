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
	"github.com/arrase/code-reducer/internal/security"
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

func executeCommand(command string, userMessage string) {
	repoRoot, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current working directory: %v\n", err)
		os.Exit(1)
	}

	// Load configuration file and apply to process environment
	_ = config.LoadAndApplyConfig(repoRoot)

	// Apply flag overrides to environment variables
	if modelIdFlag != "" {
		_ = os.Setenv(config.CodeReducerModelIdEnvKey, modelIdFlag)
	}
	if numCtxFlag != "" {
		_ = os.Setenv(config.OllamaNumCtxEnvKey, numCtxFlag)
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

		// Load and apply the newly created configuration
		_ = config.LoadAndApplyConfig(repoRoot)

		// Re-apply flag overrides to make sure they override the newly created file values
		if modelIdFlag != "" {
			_ = os.Setenv(config.CodeReducerModelIdEnvKey, modelIdFlag)
		}
		if numCtxFlag != "" {
			_ = os.Setenv(config.OllamaNumCtxEnvKey, numCtxFlag)
		}
	}


	// Acquire exclusive repository lock
	lock, err := security.AcquireLock(repoRoot, true)
	if err != nil {
		fmt.Printf("Error acquiring repository lock: %v\n", err)
		os.Exit(1)
	}
	defer lock.Unlock()

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	modelId := os.Getenv(config.CodeReducerModelIdEnvKey)
	baseURL := os.Getenv(config.OllamaBaseUrlEnvKey)
	numCtx := config.GetOllamaContextSize(numCtxFlag)

	client := engine.NewLLMClient(modelId, baseURL, numCtx)

	err = client.RunInitOrUpdate(ctx, command, repoRoot, userMessage, func(ev engine.Event) {
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

// NeedsCredentialSetup checks if critical configuration is missing
func NeedsCredentialSetup() bool {
	return os.Getenv(config.CodeReducerModelIdEnvKey) == ""
}
