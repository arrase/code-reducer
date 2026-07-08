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

	fmt.Printf("Enter LLM Model ID [%s]: ", existingModel)
	modelInput, _ := reader.ReadString('\n')
	modelInput = strings.TrimSpace(modelInput)
	if modelInput == "" {
		modelInput = existingModel
	}

	fmt.Printf("Enter Ollama Base URL [%s]: ", existingBaseURL)
	urlInput, _ := reader.ReadString('\n')
	urlInput = strings.TrimSpace(urlInput)
	if urlInput == "" {
		urlInput = existingBaseURL
	}

	fmt.Printf("Enter Ollama Context Size [%d]: ", existingNumCtx)
	ctxInput, _ := reader.ReadString('\n')
	ctxInput = strings.TrimSpace(ctxInput)
	var numCtx int
	if ctxInput == "" {
		numCtx = existingNumCtx
	} else {
		n, err := strconv.Atoi(ctxInput)
		if err != nil || n <= 0 {
			fmt.Printf("Invalid context size, using default [%d]\n", existingNumCtx)
			numCtx = existingNumCtx
		} else {
			numCtx = n
		}
	}

	var ignores []string
	if existingCfg == nil {
		fmt.Print("Enter custom directories/files to ignore (in addition to standard defaults) []: ")
		ignoreInput, _ := reader.ReadString('\n')
		ignoreInput = strings.TrimSpace(ignoreInput)

		ignores = append(ignores, config.DefaultIgnores...)
		if ignoreInput != "" {
			parts := strings.Split(ignoreInput, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					ignores = append(ignores, trimmed)
				}
			}
		}
	} else {
		existingIgnore := strings.Join(existingCfg.Ignore, ", ")
		fmt.Printf("Enter directories/files to ignore (comma-separated) [%s]: ", existingIgnore)
		ignoreInput, _ := reader.ReadString('\n')
		ignoreInput = strings.TrimSpace(ignoreInput)

		if ignoreInput == "" {
			ignores = existingCfg.Ignore
		} else {
			parts := strings.Split(ignoreInput, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					ignores = append(ignores, trimmed)
				}
			}
			if ignores == nil {
				ignores = []string{}
			}
		}
	}

	var ignoreExtensions []string
	if existingCfg == nil {
		fmt.Print("Enter custom file extensions to ignore (in addition to standard defaults) []: ")
		extInput, _ := reader.ReadString('\n')
		extInput = strings.TrimSpace(extInput)

		ignoreExtensions = append(ignoreExtensions, config.DefaultIgnoredExtensions...)
		if extInput != "" {
			parts := strings.Split(extInput, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					ignoreExtensions = append(ignoreExtensions, trimmed)
				}
			}
		}
	} else {
		existingExtensions := strings.Join(existingCfg.IgnoreExtensions, ", ")
		fmt.Printf("Enter file extensions to ignore (comma-separated) [%s]: ", existingExtensions)
		extInput, _ := reader.ReadString('\n')
		extInput = strings.TrimSpace(extInput)

		if extInput == "" {
			ignoreExtensions = existingCfg.IgnoreExtensions
		} else {
			parts := strings.Split(extInput, ",")
			for _, part := range parts {
				trimmed := strings.TrimSpace(part)
				if trimmed != "" {
					ignoreExtensions = append(ignoreExtensions, trimmed)
				}
			}
			if ignoreExtensions == nil {
				ignoreExtensions = []string{}
			}
		}
	}

	existingDocsDir := "wiki"
	if existingCfg != nil && existingCfg.DocsDir != "" {
		existingDocsDir = existingCfg.DocsDir
	}

	fmt.Printf("Enter documentation directory [%s]: ", existingDocsDir)
	docsDirInput, _ := reader.ReadString('\n')
	docsDirInput = strings.TrimSpace(docsDirInput)
	if docsDirInput == "" {
		docsDirInput = existingDocsDir
	}

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
