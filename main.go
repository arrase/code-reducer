package main

import (
	"fmt"
	"os"

	"github.com/arrase/code-reducer/cmd"
)

func main() {
	if err := cmd.RootCmd.Execute(); err != nil {
		fmt.Printf("Execution failed: %v\n", err)
		os.Exit(1)
	}
}
