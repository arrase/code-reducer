package cmd

import (
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update",
	Short: "Update project documentation incrementally",
	Long:  `Scan changed files since the last documented commit and update the wiki pages.`,
	Run: func(cmd *cobra.Command, args []string) {
		executeCommand("update")
	},
}

func init() {
	RootCmd.AddCommand(updateCmd)
}
