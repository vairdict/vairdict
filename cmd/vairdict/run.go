package main

import (
	"context"
	"fmt"
	"io"
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
	"github.com/vairdict/vairdict/internal/escalation"
	"github.com/vairdict/vairdict/internal/github"
	codejudge "github.com/vairdict/vairdict/internal/judges/code"
	planjudge "github.com/vairdict/vairdict/internal/judges/plan"
	qualityjudge "github.com/vairdict/vairdict/internal/judges/quality"
	codephase "github.com/vairdict/vairdict/internal/phases/code"
	planphase "github.com/vairdict/vairdict/internal/phases/plan"
	qualityphase "github.com/vairdict/vairdict/internal/phases/quality"
	"github.com/vairdict/vairdict/internal/state"
	"github.com/vairdict/vairdict/internal/ui"
)

const (
	exitError      = 1
	exitEscalation = 2
)

var (
	issueFlag  int
	outputFlag string
	colorsFlag string
	asciiFlag  bool
)

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
		mode, err := ui.ParseMode(outputFlag)
		if err != nil {
			return err
		}
		colors, err := ui.ParseColorScheme(colorsFlag)
		if err != nil {
			return err
		}

		var intent string
		if issueFlag > 0 {
			intent, err = fetchIssueIntent(issueFlag)
			if err != nil {
				return err
			}
		} else if len(args) == 1 {
			intent = args[0]
		} else {
			return fmt.Errorf("provide an intent argument or use --issue")
		}
		return runTask(intent, mode, colors, asciiFlag)
	},
}

func init() {
	runCmd.Flags().IntVar(&issueFlag, "issue", 0, "GitHub issue number to use as intent")
	runCmd.Flags().StringVar(&outputFlag, "output", "", "output mode: cli|ci|json (default: auto-detect)")
	runCmd.Flags().StringVar(&colorsFlag, "colors", "", "color scheme: default|accessible|no-color (default: auto-detect)")
	runCmd.Flags().BoolVar(&asciiFlag, "ascii", false, "use ASCII glyphs instead of unicode emoji")
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

func runTask(intent string, mode ui.Mode, colors ui.ColorScheme, ascii bool) error {
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

	// Open per-task log file and route slog into it. Falls back to the
	// existing handler if $HOME is unwritable so we never block a run on
	// log-file creation.
	logFile, logErr := ui.OpenLogFile(task.ID)
	logPath := ""
	if logErr == nil {
		slog.SetDefault(slog.New(logFile.Handler()))
		logPath = logFile.Path()
		defer func() { _ = logFile.Close() }()
	} else {
		slog.Warn("falling back to default log handler", "error", logErr)
	}

	// Build renderer. Auto-detects mode/colors based on TTY when flags
	// are empty; respects NO_COLOR per no-color.org.
	r := ui.New(ui.Options{
		Mode:       mode,
		Colors:     colors,
		ASCII:      ascii,
		IsTTY:      ui.IsTerminal(os.Stdout),
		NoColorEnv: ui.NoColorEnv(),
		Out:        os.Stdout,
	})
	defer func() { _ = r.Close() }()

	r.RunStart(task.ID, intent, logPath)
	if issueFlag > 0 {
		r.Note("issue", fmt.Sprintf("#%d", issueFlag))
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

	ghRunner := &github.ExecRunner{Dir: workDir}
	ghClient := github.New(ghRunner)

	// --- Plan phase ---
	planResult, err := runPlanPhase(ctx, cfg, client, store, task, r)
	if err != nil {
		r.Error(err)
		return err
	}

	if planResult.Escalate {
		gaps := lastGapsForPhase(task, state.PhasePlan)
		r.Escalation(task.ID, state.PhasePlan, planResult.Loops, planResult.LastScore, gaps)
		return escalateAndExit(ctx, task, escalation.Result{
			Phase:     state.PhasePlan,
			Loops:     planResult.Loops,
			LastScore: planResult.LastScore,
			Gaps:      gaps,
		}, cfg.Escalation, ghClient)
	}

	// --- Create branch before code phase so commits land on it ---
	branch, err := ghClient.CreateBranch(ctx, task.ID)
	if err != nil {
		r.Error(err)
		return fmt.Errorf("creating branch: %w", err)
	}
	r.Note("branch", branch)

	// --- Code phase ---
	codeResult, err := runCodePhase(ctx, cfg, store, task, planResult.Plan, workDir, r)
	if err != nil {
		r.Error(err)
		return err
	}

	if codeResult.Escalate {
		gaps := lastGapsForPhase(task, state.PhaseCode)
		r.Escalation(task.ID, state.PhaseCode, codeResult.Loops, codeResult.LastScore, gaps)
		return escalateAndExit(ctx, task, escalation.Result{
			Phase:     state.PhaseCode,
			Loops:     codeResult.Loops,
			LastScore: codeResult.LastScore,
			Gaps:      gaps,
		}, cfg.Escalation, ghClient)
	}

	// --- Commit any changes the coder made ---
	if err := commitChanges(ctx, task, workDir, r); err != nil {
		r.Error(err)
		return err
	}

	// --- Quality phase (gates the PR) ---
	qualityResult, err := runQualityPhase(ctx, cfg, client, store, task, planResult.Plan, workDir, r)
	if err != nil {
		r.Error(err)
		return err
	}

	if qualityResult.Escalate || qualityResult.RequeueToCode {
		// RequeueToCode is currently treated as escalation: cross-phase
		// routing back into the code phase is intentionally deferred to
		// a follow-up issue (see PROGRESS.md). The escalation summary
		// includes the blocking gaps so the human knows code rework is
		// needed.
		gaps := lastGapsForPhase(task, state.PhaseQuality)
		r.Escalation(task.ID, state.PhaseQuality, qualityResult.Loops, qualityResult.LastScore, gaps)
		return escalateAndExit(ctx, task, escalation.Result{
			Phase:     state.PhaseQuality,
			Loops:     qualityResult.Loops,
			LastScore: qualityResult.LastScore,
			Gaps:      gaps,
		}, cfg.Escalation, ghClient)
	}

	// --- Create GitHub PR (only after quality passes) ---
	pr, err := createPR(ctx, task, workDir, branch, r)
	if err != nil {
		r.Error(err)
		return err
	}

	// --- Post quality verdict comment on PR ---
	if pr.Number > 0 {
		lastVerdict := lastVerdictForPhase(task, state.PhaseQuality)
		if lastVerdict != nil {
			if err := ghClient.PostVerdict(ctx, pr.Number, lastVerdict, state.PhaseQuality, qualityResult.Loops); err != nil {
				// Log but don't fail the whole run for a comment posting failure.
				slog.Warn("failed to post verdict comment", "error", err)
			} else {
				r.VerdictPosted(lastVerdict.Score, lastVerdict.Pass)
			}
		}
	}

	r.RunComplete(task.ID)
	return nil
}

// dispatchEscalation routes a phase failure through the escalation module
// without touching the process exit code. Pure-ish wrapper that exists
// purely so escalateAndExit's behavior can be unit-tested in isolation
// from os.Exit.
func dispatchEscalation(
	ctx context.Context,
	task *state.Task,
	result escalation.Result,
	cfg config.EscalationConfig,
	out io.Writer,
	gh escalation.PRCommenter,
) error {
	if err := escalation.Escalate(ctx, task, result, cfg, out, gh); err != nil {
		return fmt.Errorf("escalating task: %w", err)
	}
	return nil
}

// escalateAndExit dispatches escalation and then exits the process with the
// escalation exit code. Replaces the previous inline os.Exit(exitEscalation)
// calls so that escalation channel routing (stdout / github) is honored
// consistently across phases. The os.Exit makes this function itself
// untestable; all real logic lives in dispatchEscalation.
func escalateAndExit(
	ctx context.Context,
	task *state.Task,
	result escalation.Result,
	cfg config.EscalationConfig,
	gh escalation.PRCommenter,
) error {
	if err := dispatchEscalation(ctx, task, result, cfg, os.Stderr, gh); err != nil {
		return err
	}
	os.Exit(exitEscalation)
	return nil
}

// lastGapsForPhase returns the gaps from the last verdict of the given phase,
// used to enrich escalation summaries.
func lastGapsForPhase(task *state.Task, phase state.Phase) []state.Gap {
	if v := lastVerdictForPhase(task, phase); v != nil {
		return v.Gaps
	}
	return nil
}

func runPlanPhase(ctx context.Context, cfg *config.Config, client *claude.Client, store *state.Store, task *state.Task, r ui.Renderer) (*planphase.PhaseResult, error) {
	r.PhaseStart(state.PhasePlan)

	judge := planjudge.New(client, cfg.Phases.Plan)
	phase := planphase.New(client, judge, cfg.Phases.Plan)

	result, err := phase.Run(ctx, task)
	if err != nil {
		if updateErr := store.UpdateTask(task); updateErr != nil {
			slog.Error("failed to persist task state", "error", updateErr)
		}
		return nil, fmt.Errorf("running plan phase: %w", err)
	}

	if err := store.UpdateTask(task); err != nil {
		return nil, fmt.Errorf("persisting task state: %w", err)
	}

	emitPhaseAttempts(r, task, state.PhasePlan, cfg.Phases.Plan.MaxLoops)
	emitPhaseDone(r, task, state.PhasePlan, result.Pass, result.Escalate, false, result.LastScore, result.Loops)
	return result, nil
}

func runCodePhase(ctx context.Context, cfg *config.Config, store *state.Store, task *state.Task, plan string, workDir string, r ui.Renderer) (*codephase.PhaseResult, error) {
	r.PhaseStart(state.PhaseCode)

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

	if err := store.UpdateTask(task); err != nil {
		return nil, fmt.Errorf("persisting task state: %w", err)
	}

	// The code phase judge emits a pass/fail verdict but no narrative
	// summary. Synthesize one from `git diff --stat` against main so the
	// user sees what the coder actually touched. We inject it into the
	// last code verdict so emitPhaseDone picks it up uniformly.
	if summary := codeDiffSummary(workDir); summary != "" {
		if v := lastVerdictForPhase(task, state.PhaseCode); v != nil {
			v.Summary = summary
		}
	}

	emitPhaseAttempts(r, task, state.PhaseCode, cfg.Phases.Code.MaxLoops)
	emitPhaseDone(r, task, state.PhaseCode, result.Pass, result.Escalate, false, result.LastScore, result.Loops)
	return result, nil
}

// codeDiffSummary builds a `## Files touched` section from `git diff --stat`
// against origin/main (or main if origin is not present). Empty on error so
// the caller gracefully falls back to no summary.
func codeDiffSummary(workDir string) string {
	base := "main"
	if _, err := execCommandInDir(workDir, "git", "rev-parse", "--verify", "origin/main"); err == nil {
		base = "origin/main"
	}
	out, err := execCommandInDir(workDir, "git", "diff", "--stat", base+"...HEAD")
	if err != nil {
		return ""
	}
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Files touched\n")
	// Drop the final "N files changed, ..." summary line from git's output
	// so we emit one bullet per file.
	fileLines := lines
	if len(fileLines) > 1 {
		last := fileLines[len(fileLines)-1]
		if strings.Contains(last, "changed") {
			fileLines = fileLines[:len(fileLines)-1]
		}
	}
	for _, line := range fileLines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		fmt.Fprintf(&b, "- %s\n", line)
	}
	return b.String()
}

// emitPhaseAttempts replays every attempt for the given phase to the
// renderer as PhaseLoop events. Called once after the phase finishes so
// the renderer sees the same trace whether the phase passed, failed, or
// escalated.
func emitPhaseAttempts(r ui.Renderer, task *state.Task, phase state.Phase, maxLoops int) {
	for _, attempt := range task.Attempts {
		if attempt.Phase != phase {
			continue
		}
		var score float64
		pass := false
		if attempt.Verdict != nil {
			score = attempt.Verdict.Score
			pass = attempt.Verdict.Pass
		}
		r.PhaseLoop(phase, attempt.Loop, maxLoops, score, pass)
	}
}

// emitPhaseDone emits the closing block for a phase, pulling the summary
// and gaps from the last verdict. Outcome is derived from the phase
// result flags.
func emitPhaseDone(r ui.Renderer, task *state.Task, phase state.Phase, pass, escalate, requeueToCode bool, score float64, loops int) {
	outcome := ui.OutcomeFail
	switch {
	case pass:
		outcome = ui.OutcomePass
	case requeueToCode:
		outcome = ui.OutcomeRequeueToCode
	case escalate:
		outcome = ui.OutcomeEscalate
	}
	v := lastVerdictForPhase(task, phase)
	var summary string
	var gaps []state.Gap
	if v != nil {
		summary = v.Summary
		gaps = v.Gaps
	}
	r.PhaseDone(phase, outcome, score, loops, summary, gaps)
}

func commitChanges(_ context.Context, task *state.Task, workDir string, r ui.Renderer) error {
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

	r.Note("commit", "changes committed")
	return nil
}

func execCommandInDir(dir string, name string, args ...string) ([]byte, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	return cmd.CombinedOutput()
}

func createPR(ctx context.Context, task *state.Task, workDir string, branch string, r ui.Renderer) (*github.PR, error) {
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

	r.PRCreated(pr.URL)
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

// runQualityPhase runs the quality phase orchestration and emits progress
// to the renderer in the same shape as runPlanPhase / runCodePhase.
func runQualityPhase(
	ctx context.Context,
	cfg *config.Config,
	client *claude.Client,
	store *state.Store,
	task *state.Task,
	plan string,
	workDir string,
	r ui.Renderer,
) (*qualityphase.PhaseResult, error) {
	r.PhaseStart(state.PhaseQuality)

	judge := qualityjudge.New(client, &qualityjudge.ExecRunner{}, *cfg)
	phase := qualityphase.New(judge, cfg.Phases.Quality, workDir)

	result, err := phase.Run(ctx, task, plan)
	if err != nil {
		if updateErr := store.UpdateTask(task); updateErr != nil {
			slog.Error("failed to persist task state", "error", updateErr)
		}
		return nil, fmt.Errorf("running quality phase: %w", err)
	}

	if err := store.UpdateTask(task); err != nil {
		return nil, fmt.Errorf("persisting task state: %w", err)
	}

	emitPhaseAttempts(r, task, state.PhaseQuality, cfg.Phases.Quality.MaxLoops)
	emitPhaseDone(r, task, state.PhaseQuality, result.Pass, result.Escalate, result.RequeueToCode, result.LastScore, result.Loops)

	return result, nil
}
