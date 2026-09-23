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
	if err != nil || cfg == nil {
		return &config.Config{
			ModelID:                     config.OllamaDefaultModelID,
			OllamaBaseURL:               config.OllamaDefaultBaseURL,
			OllamaNumCtx:                config.OllamaDefaultNumCtx,
			DocsDir:                     config.DefaultDocsDir,
			ExtractionSteps:             config.DefaultExtractionSteps,
			SystemPrompt:                config.DefaultSystemPrompt,
			ModuleSynthesisPrompt:       config.DefaultModuleSynthesisPrompt,
			ArchitecturePrompt:          config.DefaultArchitecturePrompt,
			FileFactConsolidationPrompt: config.DefaultFileFactConsolidationPrompt,
		}
	}

	if cfg.ModelID == "" {
		cfg.ModelID = config.OllamaDefaultModelID
	}
	if cfg.OllamaBaseURL == "" {
		cfg.OllamaBaseURL = config.OllamaDefaultBaseURL
	}
	if cfg.OllamaNumCtx <= 0 {
		cfg.OllamaNumCtx = config.OllamaDefaultNumCtx
	}
	if cfg.DocsDir == "" {
		cfg.DocsDir = config.DefaultDocsDir
	}
	if len(cfg.ExtractionSteps) == 0 {
		cfg.ExtractionSteps = config.DefaultExtractionSteps
	}
	if cfg.SystemPrompt == "" {
		cfg.SystemPrompt = config.DefaultSystemPrompt
	}
	if cfg.ModuleSynthesisPrompt == "" {
		cfg.ModuleSynthesisPrompt = config.DefaultModuleSynthesisPrompt
	}
	if cfg.ArchitecturePrompt == "" {
		cfg.ArchitecturePrompt = config.DefaultArchitecturePrompt
	}
	if cfg.FileFactConsolidationPrompt == "" {
		cfg.FileFactConsolidationPrompt = config.DefaultFileFactConsolidationPrompt
	}
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
	userInputIgnores, ignoresModified, err := promptStringList(reader, "Enter directories, files, or patterns to ignore (comma-separated)", defaultIgnores)
	if err != nil {
		return nil, fmt.Errorf("error reading ignores: %w", err)
	}
	if !ignoresModified {
		if defaultIgnores != nil {
			return defaultIgnores, nil
		}
		return []string{}, nil
	}
	if len(userInputIgnores) > 0 {
		return userInputIgnores, nil
	}
	return []string{}, nil
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

	newCfg := &config.Config{
		ModelID:                     modelInput,
		OllamaBaseURL:               urlInput,
		OllamaNumCtx:                numCtx,
		DocsDir:                     docsDirInput,
		ExtractionSteps:             current.ExtractionSteps,
		Ignore:                      ignores,
		SystemPrompt:                current.SystemPrompt,
		ModuleSynthesisPrompt:       current.ModuleSynthesisPrompt,
		ArchitecturePrompt:          current.ArchitecturePrompt,
		FileFactConsolidationPrompt: current.FileFactConsolidationPrompt,
	}

	err = config.SaveConfig(repoRoot, newCfg)
	if err != nil {
		return fmt.Errorf("error saving configuration: %w", err)
	}
	fmt.Printf("Configuration successfully saved to local %s file.\n", config.ConfigFileName)
	return nil
}

func promptStringList(reader *bufio.Reader, promptMsg string, existingList []string) ([]string, bool, error) {
	var result []string
	existingStr := ""
	if len(existingList) > 0 {
		existingStr = strings.Join(existingList, ", ")
	}

	if existingStr == "" {
		fmt.Printf("%s: ", promptMsg)
	} else {
		fmt.Printf("%s [%s]: ", promptMsg, existingStr)
	}

	input, err := reader.ReadString('\n')
	if err != nil {
		return nil, false, err
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return existingList, false, nil
	}

	lowerInput := strings.ToLower(input)
	if lowerInput == "clear" || lowerInput == "none" {
		return []string{}, true, nil
	}

	parts := strings.Split(input, ",")
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}

	return result, true, nil
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
