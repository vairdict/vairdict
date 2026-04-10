package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

const configPath = "vairdict.yaml"

var enableCmd = &cobra.Command{
	Use:   "enable",
	Short: "Enable features",
}

var disableCmd = &cobra.Command{
	Use:   "disable",
	Short: "Disable features",
}

var enableAutoVairdictCmd = &cobra.Command{
	Use:   "auto-vairdict",
	Short: "Enable automatic merge on passing verdict",
	Long:  `Sets auto_vairdict: true in vairdict.yaml. When enabled, a passing verdict triggers auto-merge via the GitHub API.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setAutoVairdict(true)
	},
}

var disableAutoVairdictCmd = &cobra.Command{
	Use:   "auto-vairdict",
	Short: "Disable automatic merge on passing verdict",
	Long:  `Sets auto_vairdict: false in vairdict.yaml. When disabled (default), a passing verdict only approves the PR.`,
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		return setAutoVairdict(false)
	},
}

func init() {
	enableCmd.AddCommand(enableAutoVairdictCmd)
	disableCmd.AddCommand(disableAutoVairdictCmd)
	rootCmd.AddCommand(enableCmd)
	rootCmd.AddCommand(disableCmd)
}

// setAutoVairdict reads vairdict.yaml, sets auto_vairdict, and writes back.
// Uses raw map manipulation to preserve field ordering as much as the
// yaml.v3 round-trip allows.
func setAutoVairdict(enabled bool) error {
	data, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("reading %s: %w", configPath, err)
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return fmt.Errorf("parsing %s: %w", configPath, err)
	}

	raw["auto_vairdict"] = enabled

	out, err := yaml.Marshal(raw)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}

	if err := os.WriteFile(configPath, out, 0o644); err != nil {
		return fmt.Errorf("writing %s: %w", configPath, err)
	}

	if enabled {
		fmt.Println("auto-vairdict enabled: passing verdicts will auto-merge PRs")
	} else {
		fmt.Println("auto-vairdict disabled: passing verdicts will only approve PRs")
	}
	return nil
}
