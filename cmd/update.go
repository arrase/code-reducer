package cmd

import (
	"github.com/arrase/code-reducer/internal/engine"
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update project documentation incrementally",
	Long:  `Scan changed files since the last documented commit and update the wiki pages.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := executeCommand(engine.ModeUpdate); err != nil {
			return err
		}
		return nil
	},
}

func init() {
	RootCmd.AddCommand(updateCmd)
}
