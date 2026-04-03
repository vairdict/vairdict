package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"github.com/vairdict/vairdict/internal/bootstrap"
)

var initCmd = &cobra.Command{
	Use:   "init",
	Short: "Initialize a new vairdict project",
	Long: `Run the interactive bootstrap flow to generate a vairdict.yaml
configuration file in the current directory. Detects language,
linter, and formatter from the repo and prompts for missing info.`,
	Args: cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		dir, err := os.Getwd()
		if err != nil {
			return fmt.Errorf("getting working directory: %w", err)
		}

		if err := bootstrap.Run(dir); err != nil {
			return fmt.Errorf("initializing project: %w", err)
		}

		return nil
	},
}

func init() {
	rootCmd.AddCommand(initCmd)
}
