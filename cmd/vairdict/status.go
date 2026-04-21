package main

import (
	"fmt"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

var statusCmd = &cobra.Command{
	Use:   "status [task-id]",
	Short: "Show task status",
	Long: `List all tasks with their state, phase, loop count, and last score.
If a task ID is provided, show detailed information including the full
verdict history, assumptions, and attempts.`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		// Show auto-vairdict status from config.
		cfg, cfgErr := config.LoadConfig("vairdict.yaml")
		if cfgErr == nil {
			autoMerge := "disabled"
			if cfg.AutoVairdict {
				autoMerge = "enabled"
			}
			fmt.Printf("auto-vairdict: %s\n\n", autoMerge)
		}

		dbPath, err := state.DefaultDBPath()
		if err != nil {
			return fmt.Errorf("resolving database path: %w", err)
		}

		store, err := state.NewStore(dbPath)
		if err != nil {
			return fmt.Errorf("opening state store: %w", err)
		}
		defer func() { _ = store.Close() }()

		if len(args) == 1 {
			return showTaskDetail(store, args[0])
		}
		return listTasks(store)
	},
}

func init() {
	rootCmd.AddCommand(statusCmd)
}

func listTasks(store *state.Store) error {
	tasks, err := store.ListTasks("")
	if err != nil {
		return fmt.Errorf("listing tasks: %w", err)
	}

	if len(tasks) == 0 {
		fmt.Println("No tasks found. Run 'vairdict run \"<intent>\"' to create one.")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tSTATE\tPHASE\tLOOPS\tLAST SCORE\tDEPS\tINTENT")
	_, _ = fmt.Fprintln(w, "--\t-----\t-----\t-----\t----------\t----\t------")

	for _, t := range tasks {
		loops := totalLoops(t)
		score := lastScore(t)
		intent := truncate(t.Intent, 50)
		deps := "-"
		if len(t.DependsOn) > 0 {
			deps = strings.Join(t.DependsOn, ",")
		}
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%s\t%s\t%s\n",
			t.ID, t.State, t.Phase, loops, score, deps, intent)
	}

	return w.Flush()
}

func showTaskDetail(store *state.Store, id string) error {
	task, err := store.GetTask(id)
	if err != nil {
		return fmt.Errorf("task %q not found", id)
	}

	fmt.Printf("Task: %s\n", task.ID)
	fmt.Printf("Intent: %s\n", task.Intent)
	fmt.Printf("State: %s\n", task.State)
	fmt.Printf("Phase: %s\n", task.Phase)
	if len(task.DependsOn) > 0 {
		fmt.Printf("Depends on: %s\n", strings.Join(task.DependsOn, ", "))
	}
	fmt.Printf("Created: %s\n", task.CreatedAt.Format("2006-01-02 15:04:05"))
	fmt.Printf("Updated: %s\n", task.UpdatedAt.Format("2006-01-02 15:04:05"))

	// Loop counts.
	fmt.Println("\nLoop Counts:")
	if len(task.LoopCount) == 0 {
		fmt.Println("  (none)")
	}
	for phase, count := range task.LoopCount {
		fmt.Printf("  %s: %d\n", phase, count)
	}

	// Assumptions.
	fmt.Println("\nAssumptions:")
	if len(task.Assumptions) == 0 {
		fmt.Println("  (none)")
	}
	for _, a := range task.Assumptions {
		fmt.Printf("  [%s] %s (phase: %s)\n", a.Severity, a.Description, a.Phase)
	}

	// Attempts with verdict history.
	fmt.Println("\nAttempts:")
	if len(task.Attempts) == 0 {
		fmt.Println("  (none)")
	}
	for i, a := range task.Attempts {
		fmt.Printf("  %d. Phase: %s, Loop: %d", i+1, a.Phase, a.Loop)
		if a.Verdict != nil {
			passStr := "FAIL"
			if a.Verdict.Pass {
				passStr = "PASS"
			}
			fmt.Printf(", Score: %.1f%%, %s", a.Verdict.Score, passStr)

			if len(a.Verdict.Gaps) > 0 {
				fmt.Println()
				for _, g := range a.Verdict.Gaps {
					blocking := ""
					if g.Blocking {
						blocking = " [BLOCKING]"
					}
					fmt.Printf("     Gap [%s]%s: %s\n", g.Severity, blocking, g.Description)
				}
			} else {
				fmt.Println()
			}

			if len(a.Verdict.Questions) > 0 {
				for _, q := range a.Verdict.Questions {
					fmt.Printf("     Question [%s]: %s\n", q.Priority, q.Text)
				}
			}
		} else {
			fmt.Println()
		}
	}

	return nil
}

func totalLoops(t *state.Task) int {
	total := 0
	for _, count := range t.LoopCount {
		total += count
	}
	return total
}

func lastScore(t *state.Task) string {
	if len(t.Attempts) == 0 {
		return "-"
	}
	last := t.Attempts[len(t.Attempts)-1]
	if last.Verdict == nil {
		return "-"
	}
	return fmt.Sprintf("%.0f%%", last.Verdict.Score)
}

func truncate(s string, maxLen int) string {
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen-3] + "..."
}
