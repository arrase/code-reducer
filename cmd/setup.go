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

// RunSetupFlow guides the user through setting up the configuration file.
func RunSetupFlow(repoRoot string) error {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Welcome to Code-Reducer CLI Setup")
	fmt.Println("---------------------------------")

	existingModel := config.OllamaDefaultModelID
	existingBaseURL := config.OllamaDefaultBaseURL
	existingNumCtx := config.OllamaDefaultNumCtx

	var existingCfg *config.Config
	cfg, err := config.LoadConfig(repoRoot)
	if err == nil && cfg != nil {
		existingCfg = cfg
		if cfg.ModelID != "" {
			existingModel = cfg.ModelID
		}
		if cfg.OllamaBaseURL != "" {
			existingBaseURL = cfg.OllamaBaseURL
		}
		if cfg.OllamaNumCtx > 0 {
			existingNumCtx = cfg.OllamaNumCtx
		}
	}

	modelInput := promptString(reader, "Enter LLM Model ID", existingModel)
	urlInput := promptString(reader, "Enter Ollama Base URL", existingBaseURL)
	
	ctxInputStr := promptString(reader, "Enter Ollama Context Size", strconv.Itoa(existingNumCtx))
	var numCtx int
	if n, err := strconv.Atoi(ctxInputStr); err == nil && n > 0 {
		numCtx = n
	} else {
		numCtx = existingNumCtx
	}

	var ignores []string
	var ignoreExtensions []string

	if existingCfg != nil {
		ignores = existingCfg.Ignore
		ignoreExtensions = existingCfg.IgnoreExtensions
	}

	ignores = promptStringList(reader, "Enter custom directories/files to ignore (comma-separated)", ignores)
	ignoreExtensions = promptStringList(reader, "Enter custom file extensions to ignore (comma-separated)", ignoreExtensions)

	existingDocsDir := "wiki"
	if existingCfg != nil && existingCfg.DocsDir != "" {
		existingDocsDir = existingCfg.DocsDir
	}
	docsDirInput := promptString(reader, "Enter documentation directory", existingDocsDir)

	var extractionSteps []config.ExtractionStep
	if existingCfg != nil && len(existingCfg.ExtractionSteps) > 0 {
		extractionSteps = existingCfg.ExtractionSteps
	} else {
		extractionSteps = config.DefaultExtractionSteps
	}

	newCfg := &config.Config{
		ModelID:          modelInput,
		OllamaBaseURL:    urlInput,
		OllamaNumCtx:     numCtx,
		DocsDir:          docsDirInput,
		ExtractionSteps:  extractionSteps,
		Ignore:           ignores,
		IgnoreExtensions: ignoreExtensions,
	}

	err = config.SaveConfig(repoRoot, newCfg)
	if err != nil {
		return fmt.Errorf("error saving configuration: %w", err)
	}
	fmt.Printf("Configuration successfully saved to local %s file.\n", config.ConfigFileName)
	return nil
}

func promptStringList(reader *bufio.Reader, promptMsg string, existingList []string) []string {
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
		fmt.Fprintf(os.Stderr, "\nWarning: error reading input (%v), keeping existing values.\n", err)
		return existingList
	}

	input = strings.TrimSpace(input)
	if input == "" {
		return existingList
	}

	lowerInput := strings.ToLower(input)
	if lowerInput == "clear" || lowerInput == "none" {
		return []string{}
	}

	parts := strings.Split(input, ",")
	for _, part := range parts {
		trimmed := strings.TrimSpace(part)
		if trimmed != "" {
			result = append(result, trimmed)
		}
	}
	
	return result
}

func promptString(reader *bufio.Reader, promptMsg, existingVal string) string {
	fmt.Printf("%s [%s]: ", promptMsg, existingVal)
	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Fprintf(os.Stderr, "\nWarning: error reading input (%v), using default: %s\n", err, existingVal)
		return existingVal
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return existingVal
	}
	return input
}
