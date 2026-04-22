package code

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/standards"
	"github.com/vairdict/vairdict/internal/state"
)

// PhaseResult is the typed output of a code phase run.
type PhaseResult struct {
	Pass      bool
	Escalate  bool
	Loops     int
	LastScore float64
	Feedback  string
}

// Coder is the interface for the coding agent (Claude Code CLI).
type Coder interface {
	Run(ctx context.Context, prompt string, workDir string) (state.AgentResult, error)
}

// Judge is the interface for the code judge (spm exec ship).
type Judge interface {
	Judge(ctx context.Context, workDir string) (*state.Verdict, error)
}

// CodePhase orchestrates the code phase: coder agent + judge loop.
type CodePhase struct {
	coder      Coder
	judge      Judge
	cfg        config.CodePhaseConfig
	workDir    string
	OnProgress func(loop, max int, step string, score float64, pass bool)
}

// New creates a CodePhase with the given coder, judge, config, and work directory.
func New(coder Coder, judge Judge, cfg config.CodePhaseConfig, workDir string) *CodePhase {
	return &CodePhase{
		coder:   coder,
		judge:   judge,
		cfg:     cfg,
		workDir: workDir,
	}
}

func (p *CodePhase) notify(loop, max int, step string, score float64, pass bool) {
	if p.OnProgress != nil {
		p.OnProgress(loop, max, step, score, pass)
	}
}

// Run executes the code phase for the given task. It loops up to MaxLoops
// times, running the coder and judge on each iteration.
func (p *CodePhase) Run(ctx context.Context, task *state.Task, plan string) (*PhaseResult, error) {
	// Ensure task is in coding state.
	if task.State != state.StateCoding {
		return nil, fmt.Errorf("code phase: unexpected state %s, want coding", task.State)
	}

	var lastFeedback string
	var lastScore float64

	for loop := 0; loop < p.cfg.MaxLoops; loop++ {
		slog.Info("code phase loop",
			"task_id", task.ID,
			"loop", loop+1,
			"max_loops", p.cfg.MaxLoops,
		)

		// Build the coder prompt.
		prompt := buildCoderPrompt(task.Intent, plan, lastFeedback, task.Assumptions)

		// Run the coder agent.
		p.notify(loop+1, p.cfg.MaxLoops, "coding", 0, false)
		_, err := p.coder.Run(ctx, prompt, p.workDir)
		if err != nil {
			return nil, fmt.Errorf("running coder agent: %w", err)
		}

		// Transition to code_review for judging.
		if err := task.Transition(state.StateCodeReview); err != nil {
			return nil, fmt.Errorf("transitioning to code review: %w", err)
		}

		// Run the judge.
		p.notify(loop+1, p.cfg.MaxLoops, "judging code", 0, false)
		verdict, err := p.judge.Judge(ctx, p.workDir)
		if err != nil {
			return nil, fmt.Errorf("running code judge: %w", err)
		}

		p.notify(loop+1, p.cfg.MaxLoops, "done", verdict.Score, verdict.Pass)

		lastScore = verdict.Score

		slog.Info("code judge verdict",
			"task_id", task.ID,
			"loop", loop+1,
			"score", verdict.Score,
			"pass", verdict.Pass,
			"gaps", len(verdict.Gaps),
		)

		// Store the attempt.
		task.Attempts = append(task.Attempts, state.Attempt{
			Phase:   state.PhaseCode,
			Loop:    loop + 1,
			Verdict: verdict,
		})

		if verdict.Pass {
			// Transition to quality.
			if err := task.Transition(state.StateQuality); err != nil {
				return nil, fmt.Errorf("advancing to quality: %w", err)
			}

			return &PhaseResult{
				Pass:      true,
				Loops:     loop + 1,
				LastScore: lastScore,
				Feedback:  buildCodeFeedback(verdict),
			}, nil
		}

		// Build feedback for next loop.
		lastFeedback = buildCodeFeedback(verdict)

		// Requeue for another loop.
		if err := task.Requeue(p.cfg.MaxLoops); err != nil {
			if err == state.ErrMaxLoopsReached {
				slog.Warn("code phase escalation",
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
			return nil, fmt.Errorf("requeueing code phase: %w", err)
		}
	}

	return &PhaseResult{
		Escalate:  true,
		Loops:     p.cfg.MaxLoops,
		LastScore: lastScore,
		Feedback:  lastFeedback,
	}, nil
}

func buildCoderPrompt(intent string, plan string, feedback string, assumptions []state.Assumption) string {
	var b strings.Builder

	b.WriteString("## Task Intent\n")
	b.WriteString(intent)
	b.WriteString("\n\n")

	b.WriteString("## Approved Plan\n")
	b.WriteString(plan)
	b.WriteString("\n")

	b.WriteString("\n")
	b.WriteString(standards.Block)

	b.WriteString("\n## Guidelines\n")
	b.WriteString("- Avoid duplicating logic. Before writing a new helper, check if one already exists.\n")
	b.WriteString("- Do not copy-paste blocks that differ only in variable names — extract a shared function.\n")
	b.WriteString("- Reuse existing utilities and patterns found in the codebase.\n")

	if feedback != "" {
		b.WriteString("\n## Previous Attempt Feedback\n")
		b.WriteString("Your previous code did not pass the quality checks. Fix the following:\n")
		b.WriteString(feedback)
		b.WriteString("\n")
	}

	if len(assumptions) > 0 {
		b.WriteString("\n## Assumptions\n")
		for _, a := range assumptions {
			fmt.Fprintf(&b, "- [%s] %s\n", a.Severity, a.Description)
		}
	}

	return b.String()
}

func buildCodeFeedback(verdict *state.Verdict) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Score: %.1f%%\n", verdict.Score)

	if len(verdict.Gaps) > 0 {
		b.WriteString("\nFailing checks:\n")
		for _, gap := range verdict.Gaps {
			blocking := ""
			if gap.Blocking {
				blocking = " [BLOCKING]"
			}
			fmt.Fprintf(&b, "- [%s]%s %s\n", gap.Severity, blocking, gap.Description)
		}
	}

	return b.String()
}
