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
	modelIDFlag             string
	numCtxFlag              string
	thinkFlag               string
	numPredictFlag          string
	slotNumPredictFlag      string
	paragraphNumPredictFlag string
	charsPerTokenFlag       string
	outputTokenReserveFlag  string
	includeTestsFlag        string
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
	RootCmd.PersistentFlags().StringVar(&thinkFlag, "think", "", "Enable reasoning output on reasoning models (default false)")
	RootCmd.PersistentFlags().StringVar(&numPredictFlag, "num-predict", "", "Cap the number of generated tokens per call (default 0, Ollama decides)")
	RootCmd.PersistentFlags().StringVar(&slotNumPredictFlag, "slot-num-predict", "", "Cap the generated tokens for each one-line documentation slot (default 192)")
	RootCmd.PersistentFlags().StringVar(&paragraphNumPredictFlag, "paragraph-num-predict", "", "Cap the generated tokens for each documentation paragraph slot (default 1024)")
	RootCmd.PersistentFlags().StringVar(&charsPerTokenFlag, "chars-per-token", "", "Characters per token used to size prompt payloads (default 3.0)")
	RootCmd.PersistentFlags().StringVar(&outputTokenReserveFlag, "output-token-reserve", "", "Context tokens held back from prompt payloads for generation (default 1024)")
	RootCmd.PersistentFlags().StringVar(&includeTestsFlag, "include-tests", "", "Document test files as well (default false)")
}

func executeCommand(mode engine.Mode) error {
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

	cfg, err := config.ResolveConfig(repoRoot, config.Flags{
		ModelID:             modelIDFlag,
		NumCtx:              numCtxFlag,
		Think:               thinkFlag,
		NumPredict:          numPredictFlag,
		SlotNumPredict:      slotNumPredictFlag,
		ParagraphNumPredict: paragraphNumPredictFlag,
		CharsPerToken:       charsPerTokenFlag,
		OutputTokenReserve:  outputTokenReserveFlag,
		IncludeTests:        includeTestsFlag,
	})
	if err != nil {
		return err
	}

	if err := checkInitStatus(repoRoot, cfg.DocsDir, mode); err != nil {
		return err
	}

	return runEngine(repoRoot, cfg, mode)
}

func checkAndRunSetup(repoRoot string) error {
	needsSetup := !config.ConfigExists(repoRoot)
	isTTY := isatty.IsTerminal(os.Stdin.Fd()) || isatty.IsCygwinTerminal(os.Stdin.Fd())

	if needsSetup {
		if !isTTY {
			return fmt.Errorf("configuration file %s does not exist in the current directory. Please run 'code-reducer setup' to configure the application", config.ConfigFileName)
		}

		if err := RunSetupFlow(repoRoot); err != nil {
			return err
		}
	}
	return nil
}

func checkInitStatus(repoRoot, docsDir string, mode engine.Mode) error {
	hasInit := engine.IsInitialized(repoRoot, docsDir)

	if mode == engine.ModeInit {
		if hasInit {
			return fmt.Errorf("the project has already been initialized. Please use 'code-reducer update' to refresh documentation")
		}
	} else if mode == engine.ModeUpdate {
		if !hasInit {
			return fmt.Errorf("the project has not been initialized yet. Please run 'code-reducer init' first")
		}
	}

	return nil
}

func runEngine(repoRoot string, cfg *config.Config, mode engine.Mode) error {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	runner := engine.NewRunner(cfg)
	err := runner.Run(ctx, repoRoot, mode, func(ev engine.Event) {
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
