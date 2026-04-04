package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/agents/claudecode"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/github"
	codejudge "github.com/vairdict/vairdict/internal/judges/code"
	planjudge "github.com/vairdict/vairdict/internal/judges/plan"
	codephase "github.com/vairdict/vairdict/internal/phases/code"
	planphase "github.com/vairdict/vairdict/internal/phases/plan"
	"github.com/vairdict/vairdict/internal/state"
)

const (
	exitError      = 1
	exitEscalation = 2
)

var runCmd = &cobra.Command{
	Use:   "run <intent>",
	Short: "Create a task and run it through the development phases",
	Long: `Create a new task with the given intent, then run it through
the plan and code phases. On success, creates a GitHub PR with
the VAIrdict verdict. Streams progress updates as each phase
loop executes.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		intent := args[0]
		return runTask(intent)
	},
}

func init() {
	rootCmd.AddCommand(runCmd)
}

func runTask(intent string) error {
	// Load config.
	cfg, err := config.LoadConfig("vairdict.yaml")
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	// Open state store.
	dbPath, err := state.DefaultDBPath()
	if err != nil {
		return fmt.Errorf("resolving database path: %w", err)
	}

	store, err := state.NewStore(dbPath)
	if err != nil {
		return fmt.Errorf("opening state store: %w", err)
	}
	defer func() { _ = store.Close() }()

	// Create Claude client.
	client, err := claude.NewClient(cfg)
	if err != nil {
		return fmt.Errorf("creating claude client: %w", err)
	}

	// Create task.
	taskID := uuid.New().String()[:8]
	task := state.NewTask(taskID, intent)

	if err := store.CreateTask(task); err != nil {
		return fmt.Errorf("creating task: %w", err)
	}

	slog.Info("task created", "id", task.ID, "intent", intent)

	// Set up context with signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Resolve working directory.
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	// --- Plan phase ---
	planResult, err := runPlanPhase(ctx, cfg, client, store, task)
	if err != nil {
		return err
	}

	if planResult.Escalate {
		fmt.Fprintf(os.Stderr, "\nEscalation needed: plan phase failed after %d loops (last score: %.0f%%)\n", planResult.Loops, planResult.LastScore)
		os.Exit(exitEscalation)
	}

	fmt.Printf("\nTask %s completed plan phase (score: %.0f%%, loops: %d)\n", task.ID, planResult.LastScore, planResult.Loops)

	// --- Code phase ---
	codeResult, err := runCodePhase(ctx, cfg, store, task, planResult.Plan, workDir)
	if err != nil {
		return err
	}

	if codeResult.Escalate {
		fmt.Fprintf(os.Stderr, "\nEscalation needed: code phase failed after %d loops (last score: %.0f%%)\n", codeResult.Loops, codeResult.LastScore)
		os.Exit(exitEscalation)
	}

	fmt.Printf("\nTask %s completed code phase (score: %.0f%%, loops: %d)\n", task.ID, codeResult.LastScore, codeResult.Loops)

	// --- Create GitHub PR ---
	if err := createPR(ctx, task, workDir); err != nil {
		return err
	}

	fmt.Printf("\nTask %s completed successfully\n", task.ID)
	return nil
}

func runPlanPhase(ctx context.Context, cfg *config.Config, client *claude.Client, store *state.Store, task *state.Task) (*planphase.PhaseResult, error) {
	fmt.Println("\n-> Plan phase starting...")

	judge := planjudge.New(client, cfg.Phases.Plan)
	phase := planphase.New(client, judge, cfg.Phases.Plan)

	// Run the plan phase with streaming output.
	// The plan phase handles its own loop internally, but we wrap it
	// to provide user-facing output.
	result, err := phase.Run(ctx, task)
	if err != nil {
		// Persist task state even on error.
		if updateErr := store.UpdateTask(task); updateErr != nil {
			slog.Error("failed to persist task state", "error", updateErr)
		}
		return nil, fmt.Errorf("running plan phase: %w", err)
	}

	// Persist final task state.
	if err := store.UpdateTask(task); err != nil {
		return nil, fmt.Errorf("persisting task state: %w", err)
	}

	// Print attempt results.
	for _, attempt := range task.Attempts {
		if attempt.Phase != state.PhasePlan {
			continue
		}
		maxLoops := cfg.Phases.Plan.MaxLoops
		passStr := "x"
		if attempt.Verdict != nil && attempt.Verdict.Pass {
			passStr = "ok"
		}
		score := 0.0
		if attempt.Verdict != nil {
			score = attempt.Verdict.Score
		}
		fmt.Printf("   Loop %d/%d: %.0f%% %s\n", attempt.Loop, maxLoops, score, passStr)
	}

	if result.Pass {
		fmt.Println("-> Plan phase complete")
	} else if result.Escalate {
		fmt.Println("-> Plan phase escalated")
	}

	return result, nil
}

func runCodePhase(ctx context.Context, cfg *config.Config, store *state.Store, task *state.Task, plan string, workDir string) (*codephase.PhaseResult, error) {
	fmt.Println("\n-> Code phase starting...")

	coder := claudecode.New()
	judge := codejudge.New(&codejudge.ExecExecutor{}, *cfg)
	phase := codephase.New(coder, judge, cfg.Phases.Code, workDir)

	result, err := phase.Run(ctx, task, plan)
	if err != nil {
		if updateErr := store.UpdateTask(task); updateErr != nil {
			slog.Error("failed to persist task state", "error", updateErr)
		}
		return nil, fmt.Errorf("running code phase: %w", err)
	}

	// Persist final task state.
	if err := store.UpdateTask(task); err != nil {
		return nil, fmt.Errorf("persisting task state: %w", err)
	}

	// Print attempt results.
	for _, attempt := range task.Attempts {
		if attempt.Phase != state.PhaseCode {
			continue
		}
		maxLoops := cfg.Phases.Code.MaxLoops
		passStr := "x"
		if attempt.Verdict != nil && attempt.Verdict.Pass {
			passStr = "ok"
		}
		score := 0.0
		if attempt.Verdict != nil {
			score = attempt.Verdict.Score
		}
		fmt.Printf("   Loop %d/%d: %.0f%% %s\n", attempt.Loop, maxLoops, score, passStr)
	}

	if result.Pass {
		fmt.Println("-> Code phase complete")
	} else if result.Escalate {
		fmt.Println("-> Code phase escalated")
	}

	return result, nil
}

func createPR(ctx context.Context, task *state.Task, workDir string) error {
	fmt.Println("\n-> Creating GitHub PR...")

	ghRunner := &github.ExecRunner{Dir: workDir}
	ghClient := github.New(ghRunner)

	// Create branch.
	branch, err := ghClient.CreateBranch(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("creating branch: %w", err)
	}

	// Build PR content.
	title := github.GeneratePRTitle(task)
	body := github.FormatPRBody(task, 0, "Implemented via VAIrdict run")

	pr, err := ghClient.CreatePR(ctx, github.CreatePROpts{
		Title:      title,
		Body:       body,
		BaseBranch: "main",
		HeadBranch: branch,
	})
	if err != nil {
		return fmt.Errorf("creating PR: %w", err)
	}

	fmt.Printf("-> PR created: %s\n", pr.URL)
	return nil
}
