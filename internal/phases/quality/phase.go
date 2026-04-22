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
// On a successful (non-error) return exactly one of these is true:
//   - Pass: verdict met the threshold; advance to PR creation.
//   - Escalate: local max-loops exhausted and the verdict can't be routed
//     elsewhere. The orchestrator still consults ReturnTo so an
//     explicit ReturnToEscalate from the judge is honoured.
//   - ReturnTo != "": cross-phase rewind. The orchestrator routes the
//     task back to ReturnToCode or ReturnToPlan (and starts a new outer
//     cycle) or escalates if ReturnToEscalate.
type PhaseResult struct {
	Pass      bool
	Escalate  bool
	ReturnTo  state.ReturnTo
	Loops     int
	LastScore float64
	Feedback  string
	// Diff is the unified diff the judge evaluated. The orchestrator
	// threads it through to PostVerdictWithDiff so gaps with file/line
	// can be rendered as inline PR review comments (#72).
	Diff string
}

// Judge is the interface for the quality judge. The real implementation lives
// in internal/judges/quality. The third argument is the unified diff of the
// code under review — the judge no longer reads the working directory itself.
type Judge interface {
	Judge(ctx context.Context, intent, plan, diff string) (*state.Verdict, error)
}

// QualityPhase orchestrates the quality phase: judge loop on already-coded work.
// Unlike plan and code phases, there is no producer agent — the code is
// already written by the time this phase runs. The diff is computed by the
// orchestrator (cmd/vairdict/run.go) before constructing the phase, so the
// same content is judged on every requeue loop.
type QualityPhase struct {
	judge Judge
	cfg   config.QualityPhaseConfig
	diff  string
}

// New creates a QualityPhase with the given judge, config, and diff. The
// diff should be the full unified diff of the code under review (e.g.
// `git diff origin/main...HEAD`). An empty diff is allowed but will
// produce a low-confidence verdict.
func New(judge Judge, cfg config.QualityPhaseConfig, diff string) *QualityPhase {
	return &QualityPhase{
		judge: judge,
		cfg:   cfg,
		diff:  diff,
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

		verdict, err := p.judge.Judge(ctx, task.Intent, plan, p.diff)
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
				Diff:      p.diff,
			}, nil
		}

		lastFeedback = buildQualityFeedback(verdict)

		// Cross-phase rewind: the quality judge's ReturnTo diagnoses
		// whether the failure is a code-, plan-, or intent-level problem.
		// Re-judging the same workdir can't fix any of those — stop
		// looping inside quality and let the orchestrator route the task
		// to the right phase (or escalate on ReturnToEscalate).
		if verdict.ReturnTo != state.ReturnToNone {
			slog.Info("quality phase rewind requested",
				"task_id", task.ID,
				"loops", loop+1,
				"last_score", lastScore,
				"return_to", string(verdict.ReturnTo),
			)
			res := &PhaseResult{
				ReturnTo:  verdict.ReturnTo,
				Loops:     loop + 1,
				LastScore: lastScore,
				Feedback:  lastFeedback,
				Diff:      p.diff,
			}
			if verdict.ReturnTo == state.ReturnToEscalate {
				res.Escalate = true
			}
			return res, nil
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
					Diff:      p.diff,
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
		Diff:      p.diff,
	}, nil
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
