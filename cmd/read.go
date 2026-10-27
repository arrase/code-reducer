package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/search"
)

var readCmd = &cobra.Command{
	Use:   "read <page[#section]>",
	Short: "Read a full documentation page or a specific heading section",
	Long:  `Read a markdown page or heading section from the generated wiki (e.g., 'architecture.md' or 'modules/internal/engine/README.md#synthesis-pipeline').`,
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}

		cfg, err := config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag, languageFlag)
		if err != nil {
			return err
		}
		content, err := search.ReadWikiSection(repoRoot, cfg.DocsDir, args[0])
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		if strings.HasSuffix(content, "\n") {
			_, _ = fmt.Fprint(out, content)
		} else {
			_, _ = fmt.Fprintln(out, content)
		}
		return nil
	},
}

func init() {
	RootCmd.AddCommand(readCmd)
}
