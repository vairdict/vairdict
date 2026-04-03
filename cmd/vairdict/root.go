package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var rootCmd = &cobra.Command{
	Use:   "vairdict",
	Short: "AI development process engine",
	Long: `VAIrdict orchestrates and judges AI-driven development across three phases:
plan, code, and quality. Each phase has a producer agent, a judge agent,
and a requeue loop with automatic escalation.`,
	SilenceUsage:  true,
	SilenceErrors: true,
}

func execute() {
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintf(os.Stderr, "Error: %s\n", err)
		os.Exit(1)
	}
}
