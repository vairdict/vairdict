package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/github"
	"github.com/vairdict/vairdict/internal/state"
	"github.com/vairdict/vairdict/internal/ui"
	"github.com/vairdict/vairdict/internal/workspace"
)

var resumeCmd = &cobra.Command{
	Use:   "resume [task-id]",
	Short: "Resume an interrupted task from its last persisted state",
	Long: `Resume a task that was interrupted (ctrl-c, crash, laptop close) by
picking up from the phase it was last in. The plan text and branch are
restored from the local state database and git — no re-planning, no lost
code.

With no argument, lists resumable tasks (non-terminal state) sorted by
most-recently-updated first.`,
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

		dbPath, err := state.DefaultDBPath()
		if err != nil {
			return fmt.Errorf("resolving database path: %w", err)
		}
		store, err := state.NewStore(dbPath)
		if err != nil {
			return fmt.Errorf("opening state store: %w", err)
		}
		defer func() { _ = store.Close() }()

		if len(args) == 0 {
			if backgroundFlag {
				return fmt.Errorf("--background requires a task id")
			}
			return listResumable(store)
		}

		if backgroundFlag && !shouldRunForeground() {
			return spawnBackground(args[0], backgroundArgsForResume(args[0], envFlag), os.Stdout)
		}

		return resumeTask(args[0], store, mode, colors, asciiFlag)
	},
}

func init() {
	resumeCmd.Flags().StringVar(&outputFlag, "output", "", "output mode: cli|ci|json (default: auto-detect)")
	resumeCmd.Flags().StringVar(&colorsFlag, "colors", "", "color scheme: default|accessible|no-color (default: auto-detect)")
	resumeCmd.Flags().BoolVar(&asciiFlag, "ascii", false, "use ASCII glyphs instead of unicode emoji")
	resumeCmd.Flags().StringVar(&envFlag, "env", "", "config environment to load (e.g. dev, test, ci)")
	resumeCmd.Flags().BoolVarP(&backgroundFlag, "background", "b", false, "run detached so the resume survives terminal exit. Prints the task id, then returns.")
	rootCmd.AddCommand(resumeCmd)
}

// listResumable prints every non-terminal task in a tabular form so the
// user can pick one to resume. Sorted most-recently-updated first so the
// task that was just interrupted is at the top.
func listResumable(store *state.Store) error {
	tasks, err := store.ListResumable()
	if err != nil {
		return fmt.Errorf("listing resumable tasks: %w", err)
	}
	if len(tasks) == 0 {
		fmt.Println("no resumable tasks. start one with 'vairdict run \"<intent>\"'.")
		return nil
	}

	fmt.Printf("%d resumable task(s):\n\n", len(tasks))
	w := tabwriter.NewWriter(os.Stdout, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(w, "ID\tSTATE\tPHASE\tUPDATED\tINTENT")
	_, _ = fmt.Fprintln(w, "--\t-----\t-----\t-------\t------")
	for _, t := range tasks {
		_, _ = fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n",
			t.ID, t.State, t.Phase,
			t.UpdatedAt.Format("2006-01-02 15:04"),
			truncate(t.Intent, 50))
	}
	_ = w.Flush()
	fmt.Println("\nresume with: vairdict resume <id>")
	return nil
}

// resumeTask loads a task, validates it's resumable, and hands it to the
// orchestration loop with a pre-populated resumeState so the plan phase
// and already-completed code phases are skipped.
func resumeTask(taskID string, store *state.Store, mode ui.Mode, colors ui.ColorScheme, ascii bool) error {
	task, err := store.GetTask(taskID)
	if err != nil {
		return fmt.Errorf("task %q not found", taskID)
	}

	// Terminal states — print status and exit. Matches the AC: resume
	// on a done/escalated task is a no-op, not an error.
	switch task.State {
	case state.StateDone:
		fmt.Printf("task %s is already done — nothing to resume.\n", task.ID)
		return nil
	case state.StateEscalated:
		fmt.Printf("task %s escalated to human review — nothing to resume.\n", task.ID)
		return nil
	case state.StateBlocked:
		fmt.Printf("task %s is blocked on dependencies — nothing to resume.\n", task.ID)
		return nil
	case state.StatePending:
		return fmt.Errorf("task %s has not started yet; run it with 'vairdict run'", task.ID)
	}

	if !task.IsResumable() {
		return fmt.Errorf("task %s is in state %s and cannot be resumed", task.ID, task.State)
	}

	// A resume to code/quality needs the plan text that produced the
	// existing branch. Without it, the coder would work from a
	// regenerated (different) plan and desync from the committed code.
	if task.Phase != state.PhasePlan && task.PlanOutput == "" {
		return fmt.Errorf("task %s has no persisted plan output — this task predates resume support and cannot be resumed", task.ID)
	}

	// Normalize review states: the phase runners expect to start from
	// the active state of their phase (planning/coding/quality), not
	// the review state. Requeueing-through-Transition is idempotent if
	// the task is already in the active state.
	switch task.State {
	case state.StatePlanReview:
		if err := task.Transition(state.StatePlanning); err != nil {
			return fmt.Errorf("normalizing plan_review for resume: %w", err)
		}
	case state.StateCodeReview:
		if err := task.Transition(state.StateCoding); err != nil {
			return fmt.Errorf("normalizing code_review for resume: %w", err)
		}
	case state.StateQualityReview:
		if err := task.Transition(state.StateQuality); err != nil {
			return fmt.Errorf("normalizing quality_review for resume: %w", err)
		}
	}
	if err := store.UpdateTask(task); err != nil {
		return fmt.Errorf("persisting normalized state: %w", err)
	}

	// Load config.
	overlayPath, err := config.ResolveOverlayPath(envFlag, config.IsCI(), ".", fileExistsFunc)
	if err != nil {
		return fmt.Errorf("resolving env: %w", err)
	}
	cfg, err := config.LoadConfigWithOverlay("vairdict.yaml", overlayPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	client, backend, err := resolveCompleter(cfg)
	if err != nil {
		return err
	}

	// Route slog into the task's log file so resume output goes to the
	// same place as the original run.
	logFile, logErr := ui.OpenLogFile(task.ID)
	logPath := ""
	if logErr == nil {
		slog.SetDefault(slog.New(logFile.Handler()))
		logPath = logFile.Path()
		defer func() { _ = logFile.Close() }()
	} else {
		slog.Warn("falling back to default log handler", "error", logErr)
	}

	r := ui.New(ui.Options{
		Mode:       mode,
		Colors:     colors,
		ASCII:      ascii,
		IsTTY:      ui.IsTerminal(os.Stdout),
		NoColorEnv: ui.NoColorEnv(),
		Out:        os.Stdout,
	})
	defer func() { _ = r.Close() }()

	r.RunStart(task.ID, task.Intent, logPath)
	r.Note("completer", string(backend))
	r.Note("resume", fmt.Sprintf("from [%s/%s]", task.Phase, task.State))

	slog.Info("task resumed", "id", task.ID, "phase", task.Phase, "state", task.State)

	ctx, done := withInterruptHandler(context.Background(), task.ID)
	defer done()

	repoRoot, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}

	// Attach to the existing worktree (or recreate it from the branch
	// if it was cleaned up). The branch is deterministic from task ID.
	wsMgr := workspace.New(repoRoot, "", &workspace.ExecRunner{})
	ws, err := wsMgr.Attach(ctx, task.ID)
	if err != nil {
		return fmt.Errorf("attaching workspace for resume: %w", err)
	}
	defer func() { _ = ws.Cleanup(ctx) }()

	workDir := ws.Path
	r.Note("workspace", workDir)

	ghRunner := &github.ExecRunner{Dir: repoRoot}
	ghClient := github.New(ghRunner)
	deps := defaultRunDeps(cfg, client, store, workDir, r, ghClient, 0)

	// Claim the run with this PID so `vairdict status` shows it as
	// running while the resumed orchestration is in flight.
	task.PID = os.Getpid()
	task.UpdatedAt = time.Now()
	if err := store.UpdateTask(task); err != nil {
		slog.Warn("failed to claim pid", "error", err)
	}
	defer clearPID(store, task)

	return runOrchestrationWithResume(ctx, deps, task, r, resumeState{
		plan:      task.PlanOutput,
		branch:    ws.Branch,
		fromPhase: task.Phase,
	})
}

// clearPID unsets the task's PID on exit so stale pid entries don't
// make `status` misreport a dead task as running. Called via defer from
// resumeTask; errors are logged but do not fail the run.
func clearPID(store *state.Store, task *state.Task) {
	task.PID = 0
	task.UpdatedAt = time.Now()
	if err := store.UpdateTask(task); err != nil {
		slog.Debug("failed to clear pid", "task", task.ID, "error", err)
	}
}

// withInterruptHandler wraps a parent context with SIGINT/SIGTERM
// handling tailored to resume-able tasks. First signal cancels the
// context and prints a resume hint on stderr. A second signal within
// the same session force-exits immediately — protects against a hung
// subprocess that ignores the cancelled context.
//
// The returned done() should be deferred by the caller to restore
// default signal handling once orchestration completes normally.
func withInterruptHandler(parent context.Context, taskID string) (context.Context, func()) {
	ctx, cancel := context.WithCancel(parent)
	sigCh := make(chan os.Signal, 2)
	signal.Notify(sigCh, os.Interrupt)

	go func() {
		sig, ok := <-sigCh
		if !ok {
			return
		}
		fmt.Fprintf(os.Stderr, "\nreceived %s — shutting down gracefully. press ctrl-c again to force exit.\n", sig)
		fmt.Fprintf(os.Stderr, "resume with: vairdict resume %s\n", taskID)
		cancel()
		// Second signal force-exits.
		if _, ok := <-sigCh; ok {
			fmt.Fprintln(os.Stderr, "forced exit.")
			os.Exit(130)
		}
	}()

	done := func() {
		signal.Stop(sigCh)
		close(sigCh)
		cancel()
	}
	return ctx, done
}
