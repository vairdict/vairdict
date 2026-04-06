package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"os/signal"
	"strings"

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

var issueFlag int

var runCmd = &cobra.Command{
	Use:   "run [intent]",
	Short: "Create a task and run it through the development phases",
	Long: `Create a new task with the given intent, then run it through
the plan and code phases. On success, creates a GitHub PR with
the VAIrdict verdict. Streams progress updates as each phase
loop executes.

Use --issue to fetch the intent from a GitHub issue:
  vairdict run --issue 32`,
	Args: cobra.MaximumNArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var intent string
		if issueFlag > 0 {
			var err error
			intent, err = fetchIssueIntent(issueFlag)
			if err != nil {
				return err
			}
			fmt.Printf("Issue #%d: %s\n", issueFlag, intent)
		} else if len(args) == 1 {
			intent = args[0]
		} else {
			return fmt.Errorf("provide an intent argument or use --issue")
		}
		return runTask(intent)
	},
}

func init() {
	runCmd.Flags().IntVar(&issueFlag, "issue", 0, "GitHub issue number to use as intent")
	rootCmd.AddCommand(runCmd)
}

// fetchIssueIntent reads the title and body of a GitHub issue via gh CLI.
func fetchIssueIntent(number int) (string, error) {
	out, err := execCommand("gh", "issue", "view", fmt.Sprintf("%d", number), "--json", "title,body", "--jq", ".title + \"\\n\\n\" + .body")
	if err != nil {
		return "", fmt.Errorf("fetching issue #%d: %w", number, err)
	}
	return strings.TrimSpace(string(out)), nil
}

func execCommand(name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	return cmd.Output()
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

	// --- Create branch before code phase so commits land on it ---
	ghRunner := &github.ExecRunner{Dir: workDir}
	ghClient := github.New(ghRunner)
	branch, err := ghClient.CreateBranch(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("creating branch: %w", err)
	}
	fmt.Printf("-> Branch created: %s\n", branch)

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

	// --- Commit any changes the coder made ---
	if err := commitChanges(ctx, task, workDir); err != nil {
		return err
	}

	// --- Create GitHub PR ---
	pr, err := createPR(ctx, task, workDir, branch)
	if err != nil {
		return err
	}

	// --- Post verdict comment on PR ---
	if pr.Number > 0 {
		lastVerdict := lastVerdictForPhase(task, state.PhaseCode)
		if lastVerdict != nil {
			if err := postVerdict(ctx, workDir, pr.Number, lastVerdict, state.PhaseCode, task.LoopCount[state.PhaseCode]+1); err != nil {
				// Log but don't fail the whole run for a comment posting failure.
				slog.Warn("failed to post verdict comment", "error", err)
			}
		}
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

func commitChanges(_ context.Context, task *state.Task, workDir string) error {
	// Stage all new and modified files.
	if out, err := execCommandInDir(workDir, "git", "add", "-A"); err != nil {
		return fmt.Errorf("staging changes: %s: %w", out, err)
	}

	// Check if there's anything to commit.
	out, err := execCommandInDir(workDir, "git", "status", "--porcelain")
	if err != nil {
		return fmt.Errorf("checking git status: %w", err)
	}
	if strings.TrimSpace(string(out)) == "" {
		slog.Warn("no changes to commit after code phase")
		return nil
	}

	msg := fmt.Sprintf("feat: %s\n\nImplemented by VAIrdict task %s", task.Intent, task.ID)
	if _, err := execCommandInDir(workDir, "git", "commit", "-m", msg); err != nil {
		return fmt.Errorf("committing changes: %w", err)
	}

	fmt.Println("-> Changes committed")
	return nil
}

func execCommandInDir(dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func createPR(ctx context.Context, task *state.Task, workDir string, branch string) (*github.PR, error) {
	fmt.Println("\n-> Creating GitHub PR...")

	ghRunner := &github.ExecRunner{Dir: workDir}
	ghClient := github.New(ghRunner)

	// Build PR content.
	title := github.GeneratePRTitle(task)
	body := github.FormatPRBody(task, issueFlag, "Implemented via VAIrdict run")

	pr, err := ghClient.CreatePR(ctx, github.CreatePROpts{
		Title:       title,
		Body:        body,
		BaseBranch:  "main",
		HeadBranch:  branch,
		IssueNumber: issueFlag,
	})
	if err != nil {
		return nil, fmt.Errorf("creating PR: %w", err)
	}

	fmt.Printf("-> PR created: %s\n", pr.URL)
	return pr, nil
}

// lastVerdictForPhase returns the verdict from the last attempt of the given phase.
func lastVerdictForPhase(task *state.Task, phase state.Phase) *state.Verdict {
	for i := len(task.Attempts) - 1; i >= 0; i-- {
		if task.Attempts[i].Phase == phase && task.Attempts[i].Verdict != nil {
			return task.Attempts[i].Verdict
		}
	}
	return nil
}

// postVerdict posts a structured verdict comment on a PR.
func postVerdict(ctx context.Context, workDir string, prNumber int, verdict *state.Verdict, phase state.Phase, loop int) error {
	ghRunner := &github.ExecRunner{Dir: workDir}
	ghClient := github.New(ghRunner)
	return ghClient.PostVerdict(ctx, prNumber, verdict, phase, loop)
}
