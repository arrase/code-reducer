package cmd

import (
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize project documentation",
	Long:  `Scan the repository and build the initial set of wiki markdown pages.`,
	Run: func(cmd *cobra.Command, args []string) {
		executeCommand("init")
	},
}

func init() {
	RootCmd.AddCommand(initCmd)
}
