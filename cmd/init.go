package cmd

import (
	"github.com/arrase/code-reducer/internal/engine"
	"github.com/spf13/cobra"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize project documentation",
	Long:  `Scan the repository and build the initial set of wiki markdown pages.`,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := executeCommand(engine.ModeInit); err != nil {
			return err
		}
		return nil
	},
}

func init() {
	RootCmd.AddCommand(initCmd)
}
