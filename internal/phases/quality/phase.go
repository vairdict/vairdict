// Package quality implements the quality phase orchestration: it runs the
// quality judge against an already-coded workdir and routes the task to
// done, back to the code phase, or to escalation.
package quality

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// PhaseResult is the typed output of a quality phase run.
//
// Exactly one of Pass, RequeueToCode, or Escalate is true on a successful
// (non-error) return. RequeueToCode signals the orchestrator to route the
// task back to the code phase because the failing gaps cannot be resolved
// by re-judging the same workdir.
type PhaseResult struct {
	Pass          bool
	Escalate      bool
	RequeueToCode bool
	Loops         int
	LastScore     float64
	Feedback      string
}

// Judge is the interface for the quality judge. The real implementation lives
// in internal/judges/quality.
type Judge interface {
	Judge(ctx context.Context, intent, plan, workDir string) (*state.Verdict, error)
}

// QualityPhase orchestrates the quality phase: judge loop on already-coded work.
// Unlike plan and code phases, there is no producer agent — the code is
// already written by the time this phase runs.
type QualityPhase struct {
	judge   Judge
	cfg     config.QualityPhaseConfig
	workDir string
}

// New creates a QualityPhase with the given judge, config, and work directory.
func New(judge Judge, cfg config.QualityPhaseConfig, workDir string) *QualityPhase {
	return &QualityPhase{
		judge:   judge,
		cfg:     cfg,
		workDir: workDir,
	}
}

// Run executes the quality phase for the given task. The task must be in the
// quality state (i.e. the code phase has already passed). It loops up to
// MaxLoops times, calling the judge each iteration.
func (p *QualityPhase) Run(ctx context.Context, task *state.Task, plan string) (*PhaseResult, error) {
	if task.State != state.StateQuality {
		return nil, fmt.Errorf("quality phase: unexpected state %s, want quality", task.State)
	}

	var lastFeedback string
	var lastScore float64

	for loop := 0; loop < p.cfg.MaxLoops; loop++ {
		slog.Info("quality phase loop",
			"task_id", task.ID,
			"loop", loop+1,
			"max_loops", p.cfg.MaxLoops,
		)

		if err := task.Transition(state.StateQualityReview); err != nil {
			return nil, fmt.Errorf("transitioning to quality review: %w", err)
		}

		verdict, err := p.judge.Judge(ctx, task.Intent, plan, p.workDir)
		if err != nil {
			return nil, fmt.Errorf("running quality judge: %w", err)
		}

		lastScore = verdict.Score

		slog.Info("quality judge verdict",
			"task_id", task.ID,
			"loop", loop+1,
			"score", verdict.Score,
			"pass", verdict.Pass,
			"gaps", len(verdict.Gaps),
		)

		task.Attempts = append(task.Attempts, state.Attempt{
			Phase:   state.PhaseQuality,
			Loop:    loop + 1,
			Verdict: verdict,
		})

		if verdict.Pass {
			if err := task.Transition(state.StateDone); err != nil {
				return nil, fmt.Errorf("advancing to done: %w", err)
			}
			return &PhaseResult{
				Pass:      true,
				Loops:     loop + 1,
				LastScore: lastScore,
				Feedback:  buildQualityFeedback(verdict),
			}, nil
		}

		lastFeedback = buildQualityFeedback(verdict)

		// Code-level gaps cannot be resolved by re-judging the same workdir.
		// Stop looping inside quality and signal cross-phase routing back
		// to the code phase. The orchestrator (cmd/vairdict/run.go) is
		// responsible for actually moving the task back.
		if needsCodeRework(verdict) {
			slog.Info("quality phase requeue to code",
				"task_id", task.ID,
				"loops", loop+1,
				"last_score", lastScore,
			)
			return &PhaseResult{
				RequeueToCode: true,
				Loops:         loop + 1,
				LastScore:     lastScore,
				Feedback:      lastFeedback,
			}, nil
		}

		if err := task.Requeue(p.cfg.MaxLoops); err != nil {
			if err == state.ErrMaxLoopsReached {
				slog.Warn("quality phase escalation",
					"task_id", task.ID,
					"loops", loop+1,
					"last_score", lastScore,
				)
				return &PhaseResult{
					Escalate:  true,
					Loops:     loop + 1,
					LastScore: lastScore,
					Feedback:  lastFeedback,
				}, nil
			}
			return nil, fmt.Errorf("requeueing quality phase: %w", err)
		}
	}

	return &PhaseResult{
		Escalate:  true,
		Loops:     p.cfg.MaxLoops,
		LastScore: lastScore,
		Feedback:  lastFeedback,
	}, nil
}

// needsCodeRework returns true if any blocking gap indicates a code-level
// problem (intent mismatch or missing/broken feature). These cannot be fixed
// by re-judging the same workdir — only by re-running the code phase.
func needsCodeRework(v *state.Verdict) bool {
	for _, g := range v.Gaps {
		if !g.Blocking {
			continue
		}
		if g.Severity == state.SeverityP0 || g.Severity == state.SeverityP1 {
			return true
		}
	}
	return false
}

func buildQualityFeedback(v *state.Verdict) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Score: %.1f%%\n", v.Score)

	if len(v.Gaps) > 0 {
		b.WriteString("\nQuality gaps:\n")
		for _, g := range v.Gaps {
			blocking := ""
			if g.Blocking {
				blocking = " [BLOCKING]"
			}
			fmt.Fprintf(&b, "- [%s]%s %s\n", g.Severity, blocking, g.Description)
		}
	}

	if len(v.Questions) > 0 {
		b.WriteString("\nOpen questions:\n")
		for _, q := range v.Questions {
			fmt.Fprintf(&b, "- (%s) %s\n", q.Priority, q.Text)
		}
	}

	return b.String()
}
