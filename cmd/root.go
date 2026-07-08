package cmd

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"
	"strings"
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
	printFlag   bool
	initFlag    bool
	updateFlag  bool
	dryRunFlag  bool
)

var RootCmd = &cobra.Command{
	Use:   "code-reducer [message]",
	Short: "Code-Reducer is a documentation agent that writes and maintains a project wiki.",
	Long:  `A pure Go port of Code-Reducer CLI, optimized for performance and local LLM execution.`,
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		// Load existing environment variables
		_, _ = config.LoadEnv()

		userMessage := ""
		if len(args) > 0 {
			userMessage = args[0]
		}

		// Determine command mode
		commandMode := "update"
		if initFlag {
			commandMode = "init"
		}

		executeCommand(commandMode, userMessage)
	},
}

func init() {
	RootCmd.PersistentFlags().StringVar(&modelIdFlag, "model-id", "", "Specify LLM model ID")
	RootCmd.PersistentFlags().StringVar(&modelIdFlag, "modelId", "", "Specify LLM model ID (alias)")
	RootCmd.PersistentFlags().StringVar(&numCtxFlag, "num-ctx", "", "Specify Ollama context window size")
	RootCmd.PersistentFlags().BoolVarP(&printFlag, "print", "p", false, "Run once and print the final assistant output (non-interactive)")
	RootCmd.PersistentFlags().BoolVar(&initFlag, "init", false, "Initialize project documentation")
	RootCmd.PersistentFlags().BoolVar(&updateFlag, "update", false, "Update existing project documentation")
	RootCmd.PersistentFlags().BoolVar(&dryRunFlag, "dry-run", false, "Show plan without invoking agent")
}

func executeCommand(command string, userMessage string) {
	repoRoot, err := os.Getwd()
	if err != nil {
		fmt.Printf("Error getting current working directory: %v\n", err)
		os.Exit(1)
	}

	// Apply flag overrides to environment variables
	if modelIdFlag != "" {
		_ = os.Setenv(config.CodeReducerModelIdEnvKey, modelIdFlag)
	}
	if numCtxFlag != "" {
		_ = os.Setenv(config.OllamaNumCtxEnvKey, numCtxFlag)
	}

	needsSetup := NeedsCredentialSetup()
	isTTY := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())

	if needsSetup {
		if !isTTY {
			fmt.Println("Error: Credentials/Model ID not configured. Please set the model ID using --model-id or the CODE_REDUCER_MODEL_ID environment variable.")
			os.Exit(1)
		}

		// Interactive CLI onboarding
		reader := bufio.NewReader(os.Stdin)
		fmt.Println("Welcome to Code-Reducer CLI Setup")
		fmt.Println("---------------------------------")
		
		fmt.Print("Enter LLM Model ID (e.g. gemma4:26b-a4b-it-qat): ")
		modelInput, _ := reader.ReadString('\n')
		modelInput = strings.TrimSpace(modelInput)
		if modelInput == "" {
			modelInput = "gemma4:26b-a4b-it-qat"
		}

		fmt.Printf("Enter Ollama Base URL [%s]: ", config.OllamaDefaultBaseURL)
		urlInput, _ := reader.ReadString('\n')
		urlInput = strings.TrimSpace(urlInput)
		if urlInput == "" {
			urlInput = config.OllamaDefaultBaseURL
		}

		fmt.Printf("Enter Ollama Context Size [%d]: ", config.OllamaDefaultNumCtx)
		ctxInput, _ := reader.ReadString('\n')
		ctxInput = strings.TrimSpace(ctxInput)
		if ctxInput == "" {
			ctxInput = strconv.Itoa(config.OllamaDefaultNumCtx)
		}

		updates := map[string]string{
			config.CodeReducerModelIdEnvKey: modelInput,
			config.OllamaBaseUrlEnvKey:      urlInput,
			config.OllamaNumCtxEnvKey:       ctxInput,
		}

		err = config.SaveEnv(updates)
		if err != nil {
			fmt.Printf("Error saving configuration: %v\n", err)
			os.Exit(1)
		}
		fmt.Println("Configuration successfully saved to local .env file.")
		fmt.Println()
	}

	if dryRunFlag {
		fmt.Println("Dry-run mode: configuration is verified, but agent was not executed.")
		return
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

