package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

// version is set at build time via ldflags.
var version = "dev"

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of vairdict",
	Long:  "Print the version string, set at build time via ldflags.",
	Args:  cobra.NoArgs,
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("vairdict %s\n", version)
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
