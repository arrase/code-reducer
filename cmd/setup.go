package cmd

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/arrase/code-reducer/internal/config"
)

var setupCmd = &cobra.Command{
	Use:   "setup",
	Short: "Configure the application",
	Long:  `Run the interactive configuration setup to generate the .code-reducer.yaml file.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}

		return RunSetupFlow(repoRoot)
	},
}

func init() {
	RootCmd.AddCommand(setupCmd)
}

func loadInitialSetupConfig(repoRoot string) *config.Config {
	cfg, err := config.LoadConfig(repoRoot)
	if err != nil {
		cfg = &config.Config{}
	}
	config.ApplyDefaults(cfg)
	return cfg
}

func promptContextSize(reader *bufio.Reader, defaultVal int) (int, error) {
	ctxInputStr, err := promptString(reader, "Enter Ollama Context Size", strconv.Itoa(defaultVal))
	if err != nil {
		return 0, fmt.Errorf("error reading context size: %w", err)
	}
	if n, err := strconv.Atoi(ctxInputStr); err == nil && n > 0 {
		return n, nil
	}
	return defaultVal, nil
}

func promptIgnores(reader *bufio.Reader, defaultIgnores []string) ([]string, error) {
	ignores, err := promptStringList(reader, "Enter directories, files, or patterns to ignore (comma-separated)", defaultIgnores)
	if err != nil {
		return nil, fmt.Errorf("error reading ignores: %w", err)
	}
	return ignores, nil
}

// RunSetupFlow guides the user through setting up the configuration file.
func RunSetupFlow(repoRoot string) error {
	return runSetupFlowWithReader(bufio.NewReader(os.Stdin), repoRoot)
}

func runSetupFlowWithReader(reader *bufio.Reader, repoRoot string) error {
	fmt.Println("Welcome to Code-Reducer CLI Setup")
	fmt.Println("---------------------------------")

	current := loadInitialSetupConfig(repoRoot)

	modelInput, err := promptString(reader, "Enter LLM Model ID", current.ModelID)
	if err != nil {
		return fmt.Errorf("error reading model ID: %w", err)
	}
	urlInput, err := promptString(reader, "Enter Ollama Base URL", current.OllamaBaseURL)
	if err != nil {
		return fmt.Errorf("error reading base URL: %w", err)
	}

	numCtx, err := promptContextSize(reader, current.OllamaNumCtx)
	if err != nil {
		return err
	}

	ignores, err := promptIgnores(reader, current.Ignore)
	if err != nil {
		return err
	}

	docsDirInput, err := promptString(reader, "Enter documentation directory", current.DocsDir)
	if err != nil {
		return fmt.Errorf("error reading docs dir: %w", err)
	}

	languageInput, err := promptString(reader, "Enter Output Language (leave empty for default English)", current.Language)
	if err != nil {
		return fmt.Errorf("error reading language: %w", err)
	}

	current.ModelID = modelInput
	current.OllamaBaseURL = urlInput
	current.OllamaNumCtx = numCtx
	current.DocsDir = docsDirInput
	current.Language = languageInput
	current.Ignore = ignores

	if err := config.SaveConfig(repoRoot, current); err != nil {
		return fmt.Errorf("error saving configuration: %w", err)
	}
	fmt.Printf("Configuration successfully saved to local %s file.\n", config.ConfigFileName)
	return nil
}

func promptStringList(reader *bufio.Reader, promptMsg string, existingList []string) ([]string, error) {
	input, err := promptString(reader, promptMsg, strings.Join(existingList, ", "))
	if err != nil {
		return nil, err
	}

	lowerInput := strings.ToLower(input)
	if input == "" || lowerInput == "clear" || lowerInput == "none" {
		return []string{}, nil
	}

	var result []string
	for _, part := range strings.Split(input, ",") {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			result = append(result, trimmed)
		}
	}
	return result, nil
}

func promptString(reader *bufio.Reader, promptMsg, existingVal string) (string, error) {
	if existingVal == "" {
		fmt.Printf("%s: ", promptMsg)
	} else {
		fmt.Printf("%s [%s]: ", promptMsg, existingVal)
	}
	input, err := reader.ReadString('\n')
	if err != nil {
		return "", err
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return existingVal, nil
	}
	return input, nil
}
