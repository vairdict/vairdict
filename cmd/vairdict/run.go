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
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/spf13/cobra"
	"github.com/vairdict/vairdict/internal/agents/claudecode"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/conflicts"
	"github.com/vairdict/vairdict/internal/deps"
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
	"github.com/vairdict/vairdict/internal/workspace"
)

const (
	exitError      = 1
	exitEscalation = 2
)

var (
	issueFlags    []int
	outputFlag    string
	colorsFlag    string
	asciiFlag     bool
	envFlag       string
	manifestFlag  string
	dependsOnFlag []string
	priorityFlag  string
)

var runCmd = &cobra.Command{
	Use:   "run [intent...]",
	Short: "Create a task and run it through the development phases",
	Long: `Create one or more tasks with the given intents, then run each through
the plan, code, and quality phases. On success, creates a GitHub PR with
the VAIrdict verdict.

Multiple intents run concurrently (up to parallel.max_tasks from config):
  vairdict run "add login" "fix logout bug"

Use --issue to fetch intents from GitHub issues:
  vairdict run --issue 32 --issue 45`,
	Args: cobra.ArbitraryArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mode, err := ui.ParseMode(outputFlag)
		if err != nil {
			return err
		}
		colors, err := ui.ParseColorScheme(colorsFlag)
		if err != nil {
			return err
		}

		// Manifest path: one or more named tasks with declared deps.
		// Mutually exclusive with positional args / --issue / --depends-on,
		// since the manifest is the complete source of truth.
		if manifestFlag != "" {
			if len(args) > 0 || len(issueFlags) > 0 || len(dependsOnFlag) > 0 {
				return fmt.Errorf("--manifest is mutually exclusive with positional intents, --issue, and --depends-on")
			}
			manifest, err := LoadManifest(manifestFlag)
			if err != nil {
				return err
			}
			return runManifest(manifest, mode, colors, asciiFlag)
		}

		// Collect intents from positional args and --issue flags.
		var intents []string
		var issues []int
		for _, num := range issueFlags {
			if num > 0 {
				intent, err := fetchIssueIntent(num)
				if err != nil {
					return err
				}
				intents = append(intents, intent)
				issues = append(issues, num)
			}
		}
		intents = append(intents, args...)

		if len(intents) == 0 {
			return fmt.Errorf("provide an intent argument or use --issue or --manifest")
		}

		// Validate priority once here so typos fail before any
		// workspace / store side effects.
		if _, err := deps.ParsePriority(priorityFlag); err != nil {
			return err
		}

		// Single intent: run exactly as before (backward compatible).
		if len(intents) == 1 {
			issueNum := 0
			if len(issues) > 0 {
				issueNum = issues[0]
			}
			return runTask(intents[0], issueNum, mode, colors, asciiFlag, dependsOnFlag, priorityFlag)
		}

		if len(dependsOnFlag) > 0 {
			return fmt.Errorf("--depends-on can only be used with a single intent; use --manifest for inter-task dependencies")
		}

		// Multiple intents without deps: concurrent execution. They all
		// share priorityFlag if provided.
		return runTasks(intents, issues, mode, colors, asciiFlag, priorityFlag)
	},
}

func init() {
	runCmd.Flags().IntSliceVar(&issueFlags, "issue", nil, "GitHub issue number(s) to use as intent (repeatable)")
	runCmd.Flags().StringVar(&outputFlag, "output", "", "output mode: cli|ci|json (default: auto-detect)")
	runCmd.Flags().StringVar(&colorsFlag, "colors", "", "color scheme: default|accessible|no-color (default: auto-detect)")
	runCmd.Flags().BoolVar(&asciiFlag, "ascii", false, "use ASCII glyphs instead of unicode emoji")
	runCmd.Flags().StringVar(&envFlag, "env", "", "config environment to load (e.g. dev, test, ci) — loads vairdict.<env>.yaml on top of vairdict.yaml. Defaults to ci when CI=true and vairdict.ci.yaml exists.")
	runCmd.Flags().StringVar(&manifestFlag, "manifest", "", "path to a YAML manifest declaring multiple tasks with dependencies (see docs for format)")
	runCmd.Flags().StringSliceVar(&dependsOnFlag, "depends-on", nil, "task ID(s) this run depends on. The new task will wait (or start blocked) until each listed task is StateDone in the store.")
	runCmd.Flags().StringVar(&priorityFlag, "priority", "", "task priority: high|normal|low (default: normal). Higher-priority tasks are dispatched first when multiple are ready.")
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

// fileExistsFunc reports whether path exists and is not a directory.
// Hoisted out of runTask so config.ResolveOverlayPath can stay
// filesystem-agnostic for unit tests.
func fileExistsFunc(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

// --- Orchestration interfaces ---
//
// These describe what the run orchestrator needs from each subsystem.
// Production uses defaultRunDeps; tests inject fakes. Interfaces are
// co-located here (not in the subsystem packages) because they describe
// the orchestrator's requirements, not the implementations' capabilities.

type planRunner interface {
	Run(ctx context.Context, task *state.Task) (*planphase.PhaseResult, error)
}

type codeRunner interface {
	Run(ctx context.Context, task *state.Task, plan string) (*codephase.PhaseResult, error)
}

type qualityRunner interface {
	Run(ctx context.Context, task *state.Task, plan string, codeFacts string) (*qualityphase.PhaseResult, error)
}

// ghOrchestrator is the subset of github.Client the orchestrator needs.
type ghOrchestrator interface {
	CreateBranch(ctx context.Context, taskID, intent string) (string, error)
	CreatePR(ctx context.Context, opts github.CreatePROpts) (*github.PR, error)
	PostVerdictWithDiff(ctx context.Context, prNumber int, v *state.Verdict, phase state.Phase, loop int, diff string) error
	MergePR(ctx context.Context, prNumber int) error
}

// conflictChecker detects and resolves merge conflicts before PR creation.
type conflictChecker interface {
	DetectAndResolve(ctx context.Context, workDir, baseBranch string) (*conflicts.Result, error)
}

// runDeps bundles all dependencies the orchestration loop needs.
type runDeps struct {
	plan         planRunner
	code         codeRunner
	quality      qualityRunner
	gh           ghOrchestrator
	conflicts    conflictChecker
	workDir      string
	commit       func(ctx context.Context, task *state.Task) error
	onEscalation func(ctx context.Context, task *state.Task, result escalation.Result) error
	issueNumber  int
	autoMerge    bool
}

// --- Default (production) phase runner implementations ---

type defaultPlanRunner struct {
	cfg    *config.Config
	client completer
	store  *state.Store
	r      ui.Renderer
}

func (d *defaultPlanRunner) Run(ctx context.Context, task *state.Task) (*planphase.PhaseResult, error) {
	return runPlanPhase(ctx, d.cfg, d.client, d.store, task, d.r)
}

type defaultCodeRunner struct {
	cfg     *config.Config
	store   *state.Store
	workDir string
	r       ui.Renderer
}

func (d *defaultCodeRunner) Run(ctx context.Context, task *state.Task, plan string) (*codephase.PhaseResult, error) {
	return runCodePhase(ctx, d.cfg, d.store, task, plan, d.workDir, d.r)
}

type defaultQualityRunner struct {
	cfg     *config.Config
	client  completer
	store   *state.Store
	workDir string
	r       ui.Renderer
}

func (d *defaultQualityRunner) Run(ctx context.Context, task *state.Task, plan string, codeFacts string) (*qualityphase.PhaseResult, error) {
	return runQualityPhase(ctx, d.cfg, d.client, d.store, task, plan, codeFacts, d.workDir, d.r)
}

func defaultRunDeps(cfg *config.Config, client completer, store *state.Store, workDir string, r ui.Renderer, ghClient *github.Client, issueNumber int) runDeps {
	return runDeps{
		plan:      &defaultPlanRunner{cfg: cfg, client: client, store: store, r: r},
		code:      &defaultCodeRunner{cfg: cfg, store: store, workDir: workDir, r: r},
		quality:   &defaultQualityRunner{cfg: cfg, client: client, store: store, workDir: workDir, r: r},
		gh:        ghClient,
		conflicts: conflicts.New(&workspace.ExecRunner{}),
		workDir:   workDir,
		commit: func(ctx context.Context, task *state.Task) error {
			return commitChanges(ctx, task, workDir, r)
		},
		onEscalation: func(ctx context.Context, task *state.Task, result escalation.Result) error {
			return escalateAndExit(ctx, task, result, cfg.Escalation, ghClient)
		},
		issueNumber: issueNumber,
		autoMerge:   cfg.AutoVairdict,
	}
}

func runTask(intent string, issueNumber int, mode ui.Mode, colors ui.ColorScheme, ascii bool, dependsOn []string, priority string) error {
	// Resolve overlay path from --env / CI auto-detect.
	overlayPath, err := config.ResolveOverlayPath(envFlag, config.IsCI(), ".", fileExistsFunc)
	if err != nil {
		return fmt.Errorf("resolving env: %w", err)
	}

	// Load config (with overlay merged on top, if any).
	cfg, err := config.LoadConfigWithOverlay("vairdict.yaml", overlayPath)
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

	// Resolve the completer backend — HTTP claude.Client or local
	// claude CLI wrapper — based on agents.judge and the environment.
	client, backend, err := resolveCompleter(cfg)
	if err != nil {
		return err
	}

	// Create task.
	taskID := uuid.New().String()[:8]
	task := state.NewTask(taskID, intent)
	task.DependsOn = dependsOn
	task.Priority = priority

	// If the task declares deps, gate its admission on the current state
	// of those deps in the store. We don't cross-process synchronise —
	// any dep that hasn't reached StateDone makes this task blocked so
	// the human can re-run after the upstream settles.
	if len(dependsOn) > 0 {
		if blocked, err := maybeBlockOnDeps(store, task, dependsOn); err != nil {
			return err
		} else if blocked {
			if err := store.CreateTask(task); err != nil {
				return fmt.Errorf("creating task: %w", err)
			}
			_, _ = fmt.Fprintf(os.Stdout, "[%s] task entered blocked state: one or more dependencies are not done. Re-run once %v have settled.\n",
				task.ID, dependsOn)
			return nil
		}
	}

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
	r.Note("completer", string(backend))
	if issueNumber > 0 {
		r.Note("issue", fmt.Sprintf("#%d", issueNumber))
	}

	slog.Info("task created", "id", task.ID, "intent", intent)

	// Set up context with signal handling.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Resolve working directory — the repo root where vairdict was invoked.
	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	// Create an isolated workspace (git worktree) for this task so
	// concurrent tasks don't interfere with each other's file changes.
	wsMgr := workspace.New(repoRoot, "", &workspace.ExecRunner{})
	ws, err := wsMgr.Create(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("creating workspace: %w", err)
	}
	defer func() { _ = ws.Cleanup(ctx) }()

	workDir := ws.Path
	r.Note("workspace", workDir)

	ghRunner := &github.ExecRunner{Dir: repoRoot}
	ghClient := github.New(ghRunner)

	deps := defaultRunDeps(cfg, client, store, workDir, r, ghClient, issueNumber)
	return runOrchestration(ctx, deps, task, r)
}

// taskResult records the outcome of a single concurrent task.
type taskResult struct {
	TaskID string
	Intent string
	Err    error
}

// runTasks executes multiple intents concurrently with a semaphore
// controlling the degree of parallelism. Shared resources (config, store,
// completer) are created once; per-task resources (workspace, log file,
// renderer, deps) are created inside each goroutine.
func runTasks(intents []string, issues []int, mode ui.Mode, colors ui.ColorScheme, ascii bool, priority string) error {
	// Resolve overlay path from --env / CI auto-detect.
	overlayPath, err := config.ResolveOverlayPath(envFlag, config.IsCI(), ".", fileExistsFunc)
	if err != nil {
		return fmt.Errorf("resolving env: %w", err)
	}

	cfg, err := config.LoadConfigWithOverlay("vairdict.yaml", overlayPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
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

	client, _, err := resolveCompleter(cfg)
	if err != nil {
		return err
	}

	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Print header for concurrent run.
	_, _ = fmt.Fprintf(os.Stdout, "Running %d tasks (max %d concurrent)\n\n", len(intents), cfg.Parallel.MaxTasks)

	sem := make(chan struct{}, cfg.Parallel.MaxTasks)
	results := make([]taskResult, len(intents))
	var wg sync.WaitGroup

	for i, intent := range intents {
		issueNum := 0
		if i < len(issues) {
			issueNum = issues[i]
		}

		wg.Add(1)
		go func(idx int, intent string, issueNum int) {
			defer wg.Done()

			sem <- struct{}{}
			defer func() { <-sem }()

			results[idx] = runSingleTask(ctx, cfg, client, store, repoRoot, intent, issueNum, priority)
			r := results[idx]
			status := "pass"
			if r.Err != nil {
				status = "FAIL"
			}
			_, _ = fmt.Fprintf(os.Stdout, "[%s] %s → %s\n", r.TaskID, truncate(intent, 60), status)
		}(i, intent, issueNum)
	}

	wg.Wait()

	// Print summary table.
	_, _ = fmt.Fprintln(os.Stdout, "\n--- Summary ---")
	var errs []string
	for _, r := range results {
		status := "pass"
		detail := ""
		if r.Err != nil {
			status = "FAIL"
			detail = ": " + r.Err.Error()
		}
		_, _ = fmt.Fprintf(os.Stdout, "  [%s] %-6s %s%s\n", r.TaskID, status, truncate(r.Intent, 50), detail)
		if r.Err != nil {
			errs = append(errs, fmt.Sprintf("task %s: %v", r.TaskID, r.Err))
		}
	}

	if len(errs) > 0 {
		return fmt.Errorf("%d of %d tasks failed:\n  %s", len(errs), len(results), strings.Join(errs, "\n  "))
	}
	return nil
}

// runSingleTask runs one task in the context of a concurrent runTasks call.
// It creates per-task isolated resources (workspace, log file, renderer).
func runSingleTask(
	ctx context.Context,
	cfg *config.Config,
	client completer,
	store *state.Store,
	repoRoot string,
	intent string,
	issueNumber int,
	priority string,
) taskResult {
	taskID := uuid.New().String()[:8]
	task := state.NewTask(taskID, intent)
	task.Priority = priority
	res := taskResult{TaskID: taskID, Intent: intent}

	if err := store.CreateTask(task); err != nil {
		res.Err = fmt.Errorf("creating task: %w", err)
		return res
	}

	logFile, logErr := ui.OpenLogFile(task.ID)
	logger := slog.Default()
	if logErr == nil {
		logger = slog.New(logFile.Handler()).With("task_id", taskID)
		defer func() { _ = logFile.Close() }()
	}
	logger.Info("task created", "id", task.ID, "intent", intent)

	// Each concurrent task writes to its own log file, not stdout.
	logWriter := io.Discard
	if logFile != nil {
		logWriter = logFile.File()
	}

	r := ui.New(ui.Options{
		Mode:       ui.ModeCI,
		Colors:     ui.ColorsNone,
		ASCII:      true,
		IsTTY:      false,
		NoColorEnv: true,
		Out:        logWriter,
	})
	defer func() { _ = r.Close() }()

	r.RunStart(task.ID, intent, "")

	wsMgr := workspace.New(repoRoot, "", &workspace.ExecRunner{})
	ws, err := wsMgr.Create(ctx, task.ID)
	if err != nil {
		res.Err = fmt.Errorf("creating workspace: %w", err)
		return res
	}
	defer func() { _ = ws.Cleanup(ctx) }()

	workDir := ws.Path
	ghRunner := &github.ExecRunner{Dir: repoRoot}
	ghClient := github.New(ghRunner)

	deps := defaultRunDeps(cfg, client, store, workDir, r, ghClient, issueNumber)
	// In concurrent mode, escalation returns an error instead of os.Exit.
	deps.onEscalation = func(ctx context.Context, task *state.Task, result escalation.Result) error {
		if err := dispatchEscalation(ctx, task, result, cfg.Escalation, logWriter, ghClient); err != nil {
			return fmt.Errorf("escalating task: %w", err)
		}
		return fmt.Errorf("task escalated in %s phase after %d loops (score: %.0f)", result.Phase, result.Loops, result.LastScore)
	}

	res.Err = runOrchestration(ctx, deps, task, r)
	return res
}

// maxOuterCycles bounds the number of plan→code→quality outer loops a
// task may go through. Each cycle gives every phase its own fresh
// per-phase budget (see state.Task.Rewind); this constant only exists to
// stop pathological verdicts from looping forever. Three cycles is enough
// to exercise every rewind path (quality→code, quality→plan→code→quality)
// with one spare attempt.
const maxOuterCycles = 3

// runOrchestration is the testable core of runTask. It receives all
// dependencies via deps so tests can substitute fakes for every
// external interaction (phases, GitHub, git commit, escalation).
//
// The outer loop runs plan → code → quality up to maxOuterCycles times.
// Each cycle re-uses the previous plan unless the quality judge rewound
// to Plan, in which case HardConstraints capture the quality failure and
// the plan phase runs again to produce a new plan that addresses them.
func runOrchestration(ctx context.Context, deps runDeps, task *state.Task, r ui.Renderer) error {
	var (
		planResult    *planphase.PhaseResult
		branch        string
		branchCreated bool
		qualityResult *qualityphase.PhaseResult
	)

	for cycle := 0; cycle < maxOuterCycles; cycle++ {
		// --- Plan phase (first cycle, or after rewind to plan) ---
		if planResult == nil {
			pr, err := deps.plan.Run(ctx, task)
			if err != nil {
				r.Error(err)
				return err
			}
			planResult = pr

			if planResult.Escalate {
				gaps := lastGapsForPhase(task, state.PhasePlan)
				r.Escalation(task.ID, state.PhasePlan, planResult.Loops, planResult.LastScore, gaps)
				return deps.onEscalation(ctx, task, escalation.Result{
					Phase:     state.PhasePlan,
					Loops:     planResult.Loops,
					LastScore: planResult.LastScore,
					Gaps:      gaps,
				})
			}
		}

		// --- Create branch once, before the first code phase ---
		if !branchCreated {
			b, err := deps.gh.CreateBranch(ctx, task.ID, task.Intent)
			if err != nil {
				r.Error(err)
				return fmt.Errorf("creating branch: %w", err)
			}
			branch = b
			r.Note("branch", branch)
			branchCreated = true
		}

		// --- Code phase ---
		codeResult, err := deps.code.Run(ctx, task, planResult.Plan)
		if err != nil {
			r.Error(err)
			return err
		}

		if codeResult.Escalate {
			gaps := lastGapsForPhase(task, state.PhaseCode)
			r.Escalation(task.ID, state.PhaseCode, codeResult.Loops, codeResult.LastScore, gaps)
			return deps.onEscalation(ctx, task, escalation.Result{
				Phase:     state.PhaseCode,
				Loops:     codeResult.Loops,
				LastScore: codeResult.LastScore,
				Gaps:      gaps,
			})
		}

		// --- Commit any changes the coder made ---
		if err := deps.commit(ctx, task); err != nil {
			r.Error(err)
			return err
		}

		// --- Quality phase (gates the PR) ---
		qResult, err := deps.quality.Run(ctx, task, planResult.Plan, codeResult.Feedback)
		if err != nil {
			r.Error(err)
			return err
		}
		qualityResult = qResult

		if qualityResult.Pass {
			break
		}

		// Quality failed. Route based on the judge's root-cause
		// diagnosis. ReturnToEscalate and the judge's own Escalate flag
		// (local loop budget exhausted) both terminate the outer loop.
		if qualityResult.Escalate || qualityResult.ReturnTo == state.ReturnToEscalate {
			gaps := lastGapsForPhase(task, state.PhaseQuality)
			r.Escalation(task.ID, state.PhaseQuality, qualityResult.Loops, qualityResult.LastScore, gaps)
			return deps.onEscalation(ctx, task, escalation.Result{
				Phase:     state.PhaseQuality,
				Loops:     qualityResult.Loops,
				LastScore: qualityResult.LastScore,
				Gaps:      gaps,
			})
		}

		qualityVerdict := lastVerdictForPhase(task, state.PhaseQuality)
		switch qualityResult.ReturnTo {
		case state.ReturnToCode:
			// The coder's "approach" is the diff it produced, not the
			// plan it worked from — the plan survives into the next
			// code run untouched, so telling the coder not to reproduce
			// the plan would be wrong. The diff captures what actually
			// failed and must change.
			task.RewindContexts = append(task.RewindContexts,
				buildRewindContext(cycle+1, state.PhaseCode, qualityVerdict, qualityResult.Diff))
			if err := task.Rewind(state.PhaseCode); err != nil {
				r.Error(err)
				return fmt.Errorf("rewinding to code: %w", err)
			}
			slog.Info("outer loop rewinding to code",
				"task_id", task.ID, "cycle", cycle+1,
				"rewind_contexts", len(task.RewindContexts),
			)
			continue
		case state.ReturnToPlan:
			task.HardConstraints = append(task.HardConstraints,
				buildQualityHardConstraints(qualityVerdict)...)
			task.RewindContexts = append(task.RewindContexts,
				buildRewindContext(cycle+1, state.PhasePlan, qualityVerdict, planResult.Plan))
			if err := task.Rewind(state.PhasePlan); err != nil {
				r.Error(err)
				return fmt.Errorf("rewinding to plan: %w", err)
			}
			planResult = nil
			slog.Info("outer loop rewinding to plan",
				"task_id", task.ID, "cycle", cycle+1,
				"hard_constraints", len(task.HardConstraints),
				"rewind_contexts", len(task.RewindContexts),
			)
			continue
		}

		// ReturnTo is empty on a non-blocking quality failure — the
		// quality phase exhausted its own budget without finding a
		// rewind target, so escalate.
		gaps := lastGapsForPhase(task, state.PhaseQuality)
		r.Escalation(task.ID, state.PhaseQuality, qualityResult.Loops, qualityResult.LastScore, gaps)
		return deps.onEscalation(ctx, task, escalation.Result{
			Phase:     state.PhaseQuality,
			Loops:     qualityResult.Loops,
			LastScore: qualityResult.LastScore,
			Gaps:      gaps,
		})
	}

	if qualityResult == nil || !qualityResult.Pass {
		// Exceeded maxOuterCycles without a passing verdict — escalate.
		gaps := lastGapsForPhase(task, state.PhaseQuality)
		loops, lastScore := 0, 0.0
		if qualityResult != nil {
			loops = qualityResult.Loops
			lastScore = qualityResult.LastScore
		}
		r.Escalation(task.ID, state.PhaseQuality, loops, lastScore, gaps)
		return deps.onEscalation(ctx, task, escalation.Result{
			Phase:     state.PhaseQuality,
			Loops:     loops,
			LastScore: lastScore,
			Gaps:      gaps,
		})
	}

	// --- Detect and resolve merge conflicts before PR creation ---
	if deps.conflicts != nil {
		conflictResult, err := deps.conflicts.DetectAndResolve(ctx, deps.workDir, "main")
		if err != nil {
			r.Error(err)
			return fmt.Errorf("checking for merge conflicts: %w", err)
		}
		if conflictResult.Rebased {
			r.Note("rebase", "auto-rebased onto latest main")
		}
		if conflictResult.HasConflicts {
			conflictGaps := make([]state.Gap, 0, len(conflictResult.ConflictFiles))
			for _, f := range conflictResult.ConflictFiles {
				conflictGaps = append(conflictGaps, state.Gap{
					Severity:    state.SeverityP0,
					Description: fmt.Sprintf("merge conflict in %s", f),
					Blocking:    true,
					File:        f,
				})
			}
			r.Escalation(task.ID, state.PhaseCode, 0, 0, conflictGaps)
			return deps.onEscalation(ctx, task, escalation.Result{
				Phase: state.PhaseCode,
				Gaps:  conflictGaps,
			})
		}
	}

	// --- Create GitHub PR (only after quality passes) ---
	title := github.GeneratePRTitle(task)
	body := github.FormatPRBody(task, deps.issueNumber, "Implemented via VAIrdict run")
	pr, err := deps.gh.CreatePR(ctx, github.CreatePROpts{
		Title:       title,
		Body:        body,
		BaseBranch:  "main",
		HeadBranch:  branch,
		IssueNumber: deps.issueNumber,
	})
	if err != nil {
		r.Error(err)
		return fmt.Errorf("creating PR: %w", err)
	}
	r.PRCreated(pr.URL)

	// --- Post quality verdict comment on PR ---
	if pr.Number > 0 {
		lastVerdict := lastVerdictForPhase(task, state.PhaseQuality)
		if lastVerdict != nil {
			if err := deps.gh.PostVerdictWithDiff(ctx, pr.Number, lastVerdict, state.PhaseQuality, qualityResult.Loops, qualityResult.Diff); err != nil {
				// Log but don't fail the whole run for a comment posting failure.
				slog.Warn("failed to post verdict comment", "error", err)
			} else {
				r.VerdictPosted(lastVerdict.Score, lastVerdict.Pass)
			}
		}

		// --- Auto-merge if enabled and verdict passed ---
		if deps.autoMerge && lastVerdict != nil && lastVerdict.Pass {
			if err := deps.gh.MergePR(ctx, pr.Number); err != nil {
				slog.Warn("auto-merge failed", "error", err)
			} else {
				r.Note("auto-merge", fmt.Sprintf("PR #%d merged", pr.Number))
			}
		}
	}

	r.RunComplete(task.ID)
	return nil
}

// buildRewindContext packages the quality judge's verdict into a
// structured RewindContext for the planner or coder. priorApproach is
// whatever the next attempt must not reproduce: the prior plan for a
// ReturnToPlan rewind, the prior code diff for a ReturnToCode rewind.
// Every blocking gap becomes a MustAddress entry; every gap (blocking
// or not) becomes a Failure entry so the next attempt sees the
// symptoms, not just the diagnosis.
func buildRewindContext(cycle int, target state.Phase, v *state.Verdict, priorApproach string) state.RewindContext {
	rc := state.RewindContext{
		Cycle:         cycle,
		Target:        target,
		TriedApproach: priorApproach,
		CreatedAt:     time.Now(),
	}
	if v == nil {
		return rc
	}
	if strings.TrimSpace(v.Summary) != "" {
		rc.RootCause = v.Summary
	} else if len(v.Gaps) > 0 {
		// Fall back to the first blocking gap, then any gap, as the
		// one-line diagnosis when the judge didn't write a summary.
		for _, g := range v.Gaps {
			if g.Blocking {
				rc.RootCause = g.Description
				break
			}
		}
		if rc.RootCause == "" {
			rc.RootCause = v.Gaps[0].Description
		}
	}
	for _, g := range v.Gaps {
		line := fmt.Sprintf("[%s] %s", g.Severity, g.Description)
		if g.File != "" {
			if g.Line > 0 {
				line = fmt.Sprintf("%s (%s:%d)", line, g.File, g.Line)
			} else {
				line = fmt.Sprintf("%s (%s)", line, g.File)
			}
		}
		rc.Failure = append(rc.Failure, line)
		if g.Blocking {
			rc.MustAddress = append(rc.MustAddress, g.Description)
		}
	}
	return rc
}

// buildQualityHardConstraints extracts the blocking gaps from the last
// quality verdict and turns each into a single-line constraint for the
// planner. The planner uses them as non-negotiable requirements on the
// next plan — it must call out concrete steps to resolve each one.
func buildQualityHardConstraints(v *state.Verdict) []string {
	if v == nil {
		return nil
	}
	constraints := make([]string, 0, len(v.Gaps))
	for _, g := range v.Gaps {
		if !g.Blocking {
			continue
		}
		c := fmt.Sprintf("[quality judge, %s] %s", g.Severity, g.Description)
		if g.File != "" {
			if g.Line > 0 {
				c = fmt.Sprintf("%s (ref: %s:%d)", c, g.File, g.Line)
			} else {
				c = fmt.Sprintf("%s (ref: %s)", c, g.File)
			}
		}
		constraints = append(constraints, c)
	}
	return constraints
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

func runPlanPhase(ctx context.Context, cfg *config.Config, client completer, store *state.Store, task *state.Task, r ui.Renderer) (*planphase.PhaseResult, error) {
	r.PhaseStart(state.PhasePlan)

	judge := planjudge.New(client, cfg.Phases.Plan)
	phase := planphase.New(client, judge, cfg.Phases.Plan)

	spin := ui.NewSpinner(os.Stdout, "", ui.PaletteForCLI(r), ui.IsASCII(r))
	phase.OnProgress = phaseProgressHandler(spin, r, state.PhasePlan)

	result, err := phase.Run(ctx, task)
	spin.Stop()
	if err != nil {
		if updateErr := store.UpdateTask(task); updateErr != nil {
			slog.Error("failed to persist task state", "error", updateErr)
		}
		return nil, fmt.Errorf("running plan phase: %w", err)
	}

	if err := store.UpdateTask(task); err != nil {
		return nil, fmt.Errorf("persisting task state: %w", err)
	}

	emitPhaseDone(r, task, state.PhasePlan, result.Pass, result.Escalate, state.ReturnToNone, result.LastScore, result.Loops)
	return result, nil
}

func runCodePhase(ctx context.Context, cfg *config.Config, store *state.Store, task *state.Task, plan string, workDir string, r ui.Renderer) (*codephase.PhaseResult, error) {
	r.PhaseStart(state.PhaseCode)

	coder := claudecode.New()
	judge := codejudge.New(&codejudge.ExecExecutor{}, *cfg)
	phase := codephase.New(coder, judge, cfg.Phases.Code, workDir)

	spin := ui.NewSpinner(os.Stdout, "", ui.PaletteForCLI(r), ui.IsASCII(r))
	phase.OnProgress = phaseProgressHandler(spin, r, state.PhaseCode)

	result, err := phase.Run(ctx, task, plan)
	spin.Stop()
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

	emitPhaseDone(r, task, state.PhaseCode, result.Pass, result.Escalate, state.ReturnToNone, result.LastScore, result.Loops)
	return result, nil
}

// codeDiffBase returns the git base ref to diff against — origin/main when
// it exists, otherwise local main. Hoisted out of codeDiffSummary so the
// quality-phase diff helper uses the same base.
func codeDiffBase(workDir string) string {
	if _, err := execCommandInDir(workDir, "git", "rev-parse", "--verify", "origin/main"); err == nil {
		return "origin/main"
	}
	return "main"
}

// codeDiffFull returns the full unified diff of HEAD against the diff base
// (origin/main or main). Used to feed the quality judge concrete code
// content rather than a directory path. Empty on error.
func codeDiffFull(workDir string) string {
	out, err := execCommandInDir(workDir, "git", "diff", codeDiffBase(workDir)+"...HEAD")
	if err != nil {
		return ""
	}
	return string(out)
}

// codeDiffSummary builds a `## Files touched` section from `git diff --stat`
// against origin/main (or main if origin is not present). Empty on error so
// the caller gracefully falls back to no summary.
func codeDiffSummary(workDir string) string {
	out, err := execCommandInDir(workDir, "git", "diff", "--stat", codeDiffBase(workDir)+"...HEAD")
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
// renderer as PhaseLoop events. Used by tests and as a fallback when
// real-time progress is not available (e.g. CI mode).
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

// phaseProgressHandler returns a callback for phase.OnProgress that updates
// the spinner label on step changes and emits PhaseLoop lines in real time
// when a loop completes. On failed loops it also renders blocking gaps so
// the user can see *why* a loop was rejected without digging into logs.
func phaseProgressHandler(spin *ui.Spinner, r ui.Renderer, phase state.Phase) func(loop, max int, step string, score float64, pass bool, gaps []state.Gap) {
	return func(loop, max int, step string, score float64, pass bool, gaps []state.Gap) {
		if step == "done" {
			// Loop finished — stop spinner, print result, restart spinner.
			spin.Stop()
			r.PhaseLoop(phase, loop, max, score, pass)
			if !pass && len(gaps) > 0 {
				// Show blocking gaps inline so the user sees why this loop failed.
				r.PhaseLoopBlockingGaps(gaps)
			}
			if !pass {
				spin.Reset()
				spin.SetLabel(fmt.Sprintf("loop %d/%d: retrying...", loop+1, max))
				spin.Start()
			}
		} else {
			// Step in progress — update spinner label.
			spin.SetLabel(fmt.Sprintf("loop %d/%d: %s", loop, max, step))
			if !spin.IsRunning() {
				spin.Start()
			}
		}
	}
}

// emitPhaseDone emits the closing block for a phase, pulling the summary
// and gaps from the last verdict. Outcome is derived from the phase
// result flags; returnTo (quality phase only) selects between rewind
// outcomes and wins over `escalate` so a ReturnToPlan shows as a
// plan-rewind rather than a generic fail.
func emitPhaseDone(r ui.Renderer, task *state.Task, phase state.Phase, pass, escalate bool, returnTo state.ReturnTo, score float64, loops int) {
	outcome := ui.OutcomeFail
	switch {
	case pass:
		outcome = ui.OutcomePass
	case returnTo == state.ReturnToCode:
		outcome = ui.OutcomeRequeueToCode
	case returnTo == state.ReturnToPlan:
		outcome = ui.OutcomeRequeueToPlan
	case escalate, returnTo == state.ReturnToEscalate:
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
	client completer,
	store *state.Store,
	task *state.Task,
	plan string,
	codeFacts string,
	workDir string,
	r ui.Renderer,
) (*qualityphase.PhaseResult, error) {
	r.PhaseStart(state.PhaseQuality)

	judge := qualityjudge.New(client, &qualityjudge.ExecRunner{}, *cfg).WithCodeFacts(codeFacts)
	// Compute the unified diff once, here, so the judge gets concrete
	// code content rather than just a working-directory path. The diff
	// is stable across requeue loops because the quality phase never
	// rewrites code.
	diff := codeDiffFull(workDir)
	phase := qualityphase.New(judge, cfg.Phases.Quality, diff)

	spin := ui.NewSpinner(os.Stdout, "", ui.PaletteForCLI(r), ui.IsASCII(r))
	phase.OnProgress = phaseProgressHandler(spin, r, state.PhaseQuality)

	result, err := phase.Run(ctx, task, plan)
	spin.Stop()
	if err != nil {
		if updateErr := store.UpdateTask(task); updateErr != nil {
			slog.Error("failed to persist task state", "error", updateErr)
		}
		return nil, fmt.Errorf("running quality phase: %w", err)
	}

	if err := store.UpdateTask(task); err != nil {
		return nil, fmt.Errorf("persisting task state: %w", err)
	}

	emitPhaseDone(r, task, state.PhaseQuality, result.Pass, result.Escalate, result.ReturnTo, result.LastScore, result.Loops)

	return result, nil
}
