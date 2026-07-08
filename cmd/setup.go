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
	Run: func(cmd *cobra.Command, args []string) {
		repoRoot, err := os.Getwd()
		if err != nil {
			fmt.Printf("Error getting current working directory: %v\n", err)
			os.Exit(1)
		}

		RunSetupFlow(repoRoot)
	},
}

func init() {
	RootCmd.AddCommand(setupCmd)
}

// RunSetupFlow guides the user through setting up the configuration file.
func RunSetupFlow(repoRoot string) {
	reader := bufio.NewReader(os.Stdin)
	fmt.Println("Welcome to Code-Reducer CLI Setup")
	fmt.Println("---------------------------------")

	existingModel := "gemma4:26b-a4b-it-qat"
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

	if existingCfg == nil {
		ignores = promptAndAppend(reader, "Enter custom directories/files to ignore (in addition to standard defaults) []: ", nil, config.DefaultIgnores)
		ignoreExtensions = promptAndAppend(reader, "Enter custom file extensions to ignore (in addition to standard defaults) []: ", nil, config.DefaultIgnoredExtensions)
	} else {
		ignores = promptAndAppend(reader, "Enter directories/files to ignore (comma-separated)", existingCfg.Ignore, nil)
		ignoreExtensions = promptAndAppend(reader, "Enter file extensions to ignore (comma-separated)", existingCfg.IgnoreExtensions, nil)
	}

	existingDocsDir := "wiki"
	if existingCfg != nil && existingCfg.DocsDir != "" {
		existingDocsDir = existingCfg.DocsDir
	}
	docsDirInput := promptString(reader, "Enter documentation directory", existingDocsDir)

	newCfg := &config.Config{
		ModelID:          modelInput,
		OllamaBaseURL:    urlInput,
		OllamaNumCtx:     numCtx,
		Ignore:           ignores,
		IgnoreExtensions: ignoreExtensions,
		DocsDir:          docsDirInput,
	}

	// Preserve optional fields if they existed
	if existingCfg != nil {
		newCfg.LangsmithAPIKey = existingCfg.LangsmithAPIKey
		newCfg.LangchainProject = existingCfg.LangchainProject
		newCfg.LangchainTracingV2 = existingCfg.LangchainTracingV2
	}

	err = config.SaveConfig(repoRoot, newCfg)
	if err != nil {
		fmt.Printf("Error saving configuration: %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("Configuration successfully saved to local %s file.\n", config.ConfigFileName)
}

func promptAndAppend(reader *bufio.Reader, promptMsg string, existingList []string, defaults []string) []string {
	var result []string
	if existingList == nil {
		fmt.Print(promptMsg)
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("\nWarning: error reading input (%v), using defaults.\n", err)
			return defaults
		}
		input = strings.TrimSpace(input)
		result = append(result, defaults...)
		if input != "" {
			parts := strings.Split(input, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					result = append(result, trimmed)
				}
			}
		}
	} else {
		existingStr := strings.Join(existingList, ", ")
		fmt.Printf("%s [%s]: ", promptMsg, existingStr)
		input, err := reader.ReadString('\n')
		if err != nil {
			fmt.Printf("\nWarning: error reading input (%v), keeping existing values.\n", err)
			return existingList
		}
		input = strings.TrimSpace(input)

		if input == "" {
			result = existingList
		} else {
			parts := strings.Split(input, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					result = append(result, trimmed)
				}
			}
			if result == nil {
				result = []string{}
			}
		}
	}
	return result
}

func promptString(reader *bufio.Reader, promptMsg, existingVal string) string {
	fmt.Printf("%s [%s]: ", promptMsg, existingVal)
	input, err := reader.ReadString('\n')
	if err != nil {
		fmt.Printf("\nWarning: error reading input (%v), using default: %s\n", err, existingVal)
		return existingVal
	}
	input = strings.TrimSpace(input)
	if input == "" {
		return existingVal
	}
	return input
}
