package cmd

import (
	"github.com/spf13/cobra"
)

var updateCmd = &cobra.Command{
	Use:   "update [message]",
	Short: "Update existing project documentation",
	Long:  `Scan git changes and selectively update stale markdown pages.`,
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		userMessage := ""
		if len(args) > 0 {
			userMessage = args[0]
		}
		executeCommand("update", userMessage)
	},
}

func init() {
	RootCmd.AddCommand(updateCmd)
}
