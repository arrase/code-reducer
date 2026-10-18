package cmd

import (
	"github.com/arrase/code-reducer/internal/engine"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Incrementally update documentation for modified files using cached SHA-256 hashes.",
	Long:  `Scan changed files since the last documented commit and update the wiki pages.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		return executeCommand(engine.ModeUpdate)
	},
}

func init() {
	RootCmd.AddCommand(updateCmd)
}
