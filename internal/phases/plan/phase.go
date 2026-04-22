// Package plan implements the plan phase orchestration. It spawns a planner
// agent to generate a requirements doc and implementation plan from the task
// intent, then runs the plan through the PlanJudge. On failure it requeues
// with judge feedback until the plan passes or max loops is reached.
package plan

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/standards"
	"github.com/vairdict/vairdict/internal/state"
)

// PhaseResult is the typed output of a plan phase run.
type PhaseResult struct {
	Pass      bool
	Escalate  bool
	Loops     int
	LastScore float64
	Feedback  string
	Plan      string
}

// plannerResponse is the typed response expected from the planner agent.
type plannerResponse struct {
	Requirements string `json:"requirements"`
	Plan         string `json:"plan"`
}

// Planner is the interface for the planner agent that generates plans.
type Planner interface {
	CompleteWithSystem(ctx context.Context, system, prompt string, target any) error
}

// Judge is the interface for the plan judge that evaluates plans.
type Judge interface {
	Judge(ctx context.Context, intent string, plan string) (*state.Verdict, error)
}

// PlanPhase orchestrates the plan phase: planner agent + judge loop.
type PlanPhase struct {
	planner Planner
	judge   Judge
	cfg     config.PlanPhaseConfig
}

// New creates a PlanPhase with the given planner client, judge, and config.
func New(planner Planner, judge Judge, cfg config.PlanPhaseConfig) *PlanPhase {
	return &PlanPhase{
		planner: planner,
		judge:   judge,
		cfg:     cfg,
	}
}

const plannerSystemPromptCore = `You are a software development planner. Your job is to take a task intent and produce a detailed requirements document and implementation plan.

You MUST respond with valid JSON only — no markdown, no explanation outside the JSON.

Respond with this exact JSON structure:
{
  "requirements": "<detailed requirements document>",
  "plan": "<step-by-step implementation plan>"
}

If you receive feedback from a previous attempt, address every piece of feedback in your revised plan.`

// plannerSystemPrompt is the planner prompt with the non-negotiable
// engineering standards appended. Baseline rules reach the planner so it
// plans around them from the start (e.g. names a config loader rather
// than leaving "TODO: load secrets").
var plannerSystemPrompt = plannerSystemPromptCore + "\n\n" + standards.Block

// Run executes the plan phase for the given task. It loops up to MaxLoops
// times, running the planner and judge on each iteration. It updates task
// state, logs assumptions from P2 gaps, and stores all attempts.
func (p *PlanPhase) Run(ctx context.Context, task *state.Task) (*PhaseResult, error) {
	// Transition from pending to planning.
	if task.State == state.StatePending {
		if err := task.Transition(state.StatePlanning); err != nil {
			return nil, fmt.Errorf("starting plan phase: %w", err)
		}
	}

	var lastFeedback string
	var lastScore float64

	for loop := 0; loop < p.cfg.MaxLoops; loop++ {
		slog.Info("plan phase loop",
			"task_id", task.ID,
			"loop", loop+1,
			"max_loops", p.cfg.MaxLoops,
		)

		// Ensure task is in planning state.
		if task.State != state.StatePlanning {
			return nil, fmt.Errorf("running plan phase: unexpected state %s", task.State)
		}

		// Build the planner prompt.
		prompt := buildPlannerPrompt(task.Intent, lastFeedback, task.Assumptions, task.HardConstraints)

		// Call the planner agent.
		var resp plannerResponse
		if err := p.planner.CompleteWithSystem(ctx, plannerSystemPrompt, prompt, &resp); err != nil {
			return nil, fmt.Errorf("calling planner agent: %w", err)
		}

		// Combine requirements and plan for the judge.
		fullPlan := fmt.Sprintf("## Requirements\n%s\n\n## Implementation Plan\n%s", resp.Requirements, resp.Plan)

		// Transition to plan_review for judging.
		if err := task.Transition(state.StatePlanReview); err != nil {
			return nil, fmt.Errorf("transitioning to plan review: %w", err)
		}

		// Run the judge.
		verdict, err := p.judge.Judge(ctx, task.Intent, fullPlan)
		if err != nil {
			return nil, fmt.Errorf("running plan judge: %w", err)
		}

		lastScore = verdict.Score

		slog.Info("plan judge verdict",
			"task_id", task.ID,
			"loop", loop+1,
			"score", verdict.Score,
			"pass", verdict.Pass,
			"gaps", len(verdict.Gaps),
		)

		// Store the attempt.
		task.Attempts = append(task.Attempts, state.Attempt{
			Phase:   state.PhasePlan,
			Loop:    loop + 1,
			Verdict: verdict,
		})

		// Process gaps: log P2 as assumptions, P3 for later.
		p.processGaps(task, verdict.Gaps)

		if verdict.Pass {
			// Transition to coding.
			if err := task.Transition(state.StateCoding); err != nil {
				return nil, fmt.Errorf("advancing to coding: %w", err)
			}

			return &PhaseResult{
				Pass:      true,
				Loops:     loop + 1,
				LastScore: lastScore,
				Feedback:  buildFeedbackSummary(verdict),
				Plan:      fullPlan,
			}, nil
		}

		// Build feedback for next loop.
		lastFeedback = buildFeedbackSummary(verdict)

		// Requeue for another loop (unless max reached).
		if err := task.Requeue(p.cfg.MaxLoops); err != nil {
			if err == state.ErrMaxLoopsReached {
				slog.Warn("plan phase escalation",
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
			return nil, fmt.Errorf("requeueing plan phase: %w", err)
		}
	}

	// Should not be reached since Requeue handles max loops,
	// but be defensive.
	return &PhaseResult{
		Escalate:  true,
		Loops:     p.cfg.MaxLoops,
		LastScore: lastScore,
		Feedback:  lastFeedback,
	}, nil
}

// processGaps logs P2 gaps as assumptions on the task and P3 gaps for later.
func (p *PlanPhase) processGaps(task *state.Task, gaps []state.Gap) {
	for _, gap := range gaps {
		switch gap.Severity {
		case state.SeverityP2:
			slog.Info("logging P2 gap as assumption",
				"task_id", task.ID,
				"description", gap.Description,
			)
			task.Assumptions = append(task.Assumptions, state.Assumption{
				Description: gap.Description,
				Severity:    state.SeverityP2,
				Phase:       state.PhasePlan,
			})
		case state.SeverityP3:
			slog.Info("deferring P3 gap to future issue",
				"task_id", task.ID,
				"description", gap.Description,
			)
		}
	}
}

// buildPlannerPrompt constructs the prompt for the planner agent.
func buildPlannerPrompt(intent string, feedback string, assumptions []state.Assumption, hardConstraints []string) string {
	var b strings.Builder

	b.WriteString("## Task Intent\n")
	b.WriteString(intent)
	b.WriteString("\n")

	if len(hardConstraints) > 0 {
		b.WriteString("\n## Hard Constraints (non-negotiable)\n")
		b.WriteString("These were identified by the quality judge on a previous outer cycle. ")
		b.WriteString("Your new plan MUST address each one — acknowledging is not enough, the plan must call out concrete steps that resolve them:\n")
		for _, c := range hardConstraints {
			fmt.Fprintf(&b, "- %s\n", c)
		}
	}

	if feedback != "" {
		b.WriteString("\n## Previous Attempt Feedback\n")
		b.WriteString("Your previous plan was rejected. Address the following feedback:\n")
		b.WriteString(feedback)
		b.WriteString("\n")
	}

	if len(assumptions) > 0 {
		b.WriteString("\n## Assumptions from Previous Loops\n")
		for _, a := range assumptions {
			fmt.Fprintf(&b, "- [%s] %s\n", a.Severity, a.Description)
		}
	}

	return b.String()
}

// buildFeedbackSummary creates a human-readable summary from a verdict.
func buildFeedbackSummary(verdict *state.Verdict) string {
	var b strings.Builder

	fmt.Fprintf(&b, "Score: %.1f\n", verdict.Score)

	if len(verdict.Gaps) > 0 {
		b.WriteString("\nGaps:\n")
		for _, gap := range verdict.Gaps {
			blocking := ""
			if gap.Blocking {
				blocking = " [BLOCKING]"
			}
			fmt.Fprintf(&b, "- [%s]%s %s\n", gap.Severity, blocking, gap.Description)
		}
	}

	if len(verdict.Questions) > 0 {
		b.WriteString("\nQuestions:\n")
		for _, q := range verdict.Questions {
			fmt.Fprintf(&b, "- [%s] %s\n", q.Priority, q.Text)
		}
	}

	return b.String()
}
