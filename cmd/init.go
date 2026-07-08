package cmd

import (
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init [message]",
	Short: "Initialize project documentation",
	Long:  `Scan the repository and build the initial set of wiki markdown pages.`,
	Args:  cobra.MaximumNArgs(1),
	Run: func(cmd *cobra.Command, args []string) {
		userMessage := ""
		if len(args) > 0 {
			userMessage = args[0]
		}
		executeCommand("init", userMessage)
	},
}

func init() {
	RootCmd.AddCommand(initCmd)
}
