package cmd

import (
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/arrase/code-reducer/internal/config"
	"github.com/arrase/code-reducer/internal/search"
)

var (
	searchPathHintsFlag []string
	searchLimitFlag     int
)

var searchCmd = &cobra.Command{
	Use:   "search <query>",
	Short: "Search project documentation sections using BM25 ranking",
	Long:  `Search generated wiki markdown pages at section granularity and return ranked targets for 'code-reducer read'.`,
	Args:  cobra.MinimumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		repoRoot, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("failed to get current working directory: %w", err)
		}

		cfg, err := config.ResolveConfig(repoRoot, modelIDFlag, numCtxFlag, languageFlag)
		if err != nil {
			return err
		}
		query := strings.Join(args, " ")

		results, err := search.SearchWiki(repoRoot, cfg.DocsDir, query, searchPathHintsFlag, searchLimitFlag)
		if err != nil {
			return err
		}

		out := cmd.OutOrStdout()
		if len(results) == 0 {
			_, _ = fmt.Fprintf(out, "No results found for %q.\n", query)
			return nil
		}

		for i, r := range results {
			_, _ = fmt.Fprintf(out, "[%d] %s (score: %.2f)\n", i+1, r.Target, r.Score)
			_, _ = fmt.Fprintf(out, "    Heading: %s\n", r.Heading)
			if len(r.Sources) > 0 {
				_, _ = fmt.Fprintf(out, "    Sources: %s\n", strings.Join(r.Sources, ", "))
			}
			if r.Snippet != "" {
				_, _ = fmt.Fprintf(out, "    Snippet: %s\n", r.Snippet)
			}
		}
		return nil
	},
}

func init() {
	searchCmd.Flags().StringSliceVarP(&searchPathHintsFlag, "path", "p", nil, "Boost sections matching these source paths")
	searchCmd.Flags().IntVarP(&searchLimitFlag, "limit", "n", 5, "Maximum number of results")
	RootCmd.AddCommand(searchCmd)
}
