package state

import (
	"fmt"
	"strings"
	"time"
)

// AgentResult is the shared result type returned by all agents that
// execute work (coders, judges, etc). Consumers use Output; Stderr,
// ExitCode, and Duration are optional and populated by CLI-based agents.
type AgentResult struct {
	Output   string        `json:"output"`
	Stderr   string        `json:"stderr,omitempty"`
	ExitCode int           `json:"exit_code,omitempty"`
	Duration time.Duration `json:"duration,omitempty"`
}

// Phase represents a development phase in the VAIrdict pipeline.
type Phase string

const (
	PhasePlan    Phase = "plan"
	PhaseCode    Phase = "code"
	PhaseQuality Phase = "quality"
)

// AllPhases returns all valid phases in order.
func AllPhases() []Phase {
	return []Phase{PhasePlan, PhaseCode, PhaseQuality}
}

// TaskState represents the current state of a task.
type TaskState string

const (
	StatePending       TaskState = "pending"
	StatePlanning      TaskState = "planning"
	StatePlanReview    TaskState = "plan_review"
	StateCoding        TaskState = "coding"
	StateCodeReview    TaskState = "code_review"
	StateQuality       TaskState = "quality"
	StateQualityReview TaskState = "quality_review"
	StateDone          TaskState = "done"
	StateEscalated     TaskState = "escalated"
	// StateBlocked marks a task that cannot run because a prerequisite
	// dependency failed. Terminal for the current invocation; the human
	// fixes the upstream and re-runs. Never transitioned out of
	// automatically. See internal/deps.
	StateBlocked TaskState = "blocked"
)

// validTransitions defines which state transitions are allowed.
//
// StateQualityReview can rewind to StateCoding or StatePlanning when the
// quality judge's verdict says the root cause is a code- or plan-level
// failure — not something re-judging the same workdir can resolve. See
// Rewind for the per-cycle budget semantics that accompany those
// transitions.
var validTransitions = map[TaskState][]TaskState{
	// Pending can go into the normal pipeline or straight to blocked if
	// a dependency already failed at submission time.
	StatePending:       {StatePlanning, StateBlocked},
	StatePlanning:      {StatePlanReview},
	StatePlanReview:    {StatePlanning, StateCoding, StateEscalated},
	StateCoding:        {StateCodeReview},
	StateCodeReview:    {StateCoding, StateQuality, StateEscalated},
	StateQuality:       {StateQualityReview},
	StateQualityReview: {StateQuality, StateCoding, StatePlanning, StateDone, StateEscalated},
	StateDone:          {},
	StateEscalated:     {},
	StateBlocked:       {},
}

// phaseForState maps each active state to its phase.
var phaseForState = map[TaskState]Phase{
	StatePlanning:      PhasePlan,
	StatePlanReview:    PhasePlan,
	StateCoding:        PhaseCode,
	StateCodeReview:    PhaseCode,
	StateQuality:       PhaseQuality,
	StateQualityReview: PhaseQuality,
}

// Severity represents the severity level of a gap or assumption.
type Severity string

const (
	SeverityP0 Severity = "P0"
	SeverityP1 Severity = "P1"
	SeverityP2 Severity = "P2"
	SeverityP3 Severity = "P3"
)

// Assumption records a decision made under uncertainty during a phase.
type Assumption struct {
	Description string   `json:"description"`
	Severity    Severity `json:"severity"`
	Phase       Phase    `json:"phase"`
}

// Gap represents a deficiency identified by a judge.
type Gap struct {
	Severity    Severity `json:"severity"`
	Description string   `json:"description"`
	Blocking    bool     `json:"blocking"`
	File        string   `json:"file,omitempty"`
	Line        int      `json:"line,omitempty"`
	Suggestion  string   `json:"suggestion,omitempty"`
}

// Question represents a question raised by a judge.
type Question struct {
	Text     string `json:"text"`
	Priority string `json:"priority"`
}

// ReturnTo names the phase a failing verdict should rewind to. The quality
// judge sets it when the root cause of a failure cannot be resolved by
// re-running the current phase — the orchestrator reads it to route the
// task back through the outer loop.
type ReturnTo string

const (
	// ReturnToNone is the zero value: no rewind requested. Used when the
	// verdict passes or the judge wants another in-phase retry.
	ReturnToNone ReturnTo = ""
	// ReturnToCode rewinds to the code phase (tests failing, acceptance
	// criteria unmet, etc).
	ReturnToCode ReturnTo = "code"
	// ReturnToPlan rewinds to the plan phase (plan was too shallow to
	// catch this class of problem). The quality failure is injected as
	// a hard constraint into replanning.
	ReturnToPlan ReturnTo = "plan"
	// ReturnToEscalate stops the loop and asks for human input (intent
	// is fundamentally ambiguous or requires judgement this process
	// cannot make).
	ReturnToEscalate ReturnTo = "escalate"
)

// Verdict is the typed output of a judge evaluation.
type Verdict struct {
	Score     float64    `json:"score"`
	Pass      bool       `json:"pass"`
	Gaps      []Gap      `json:"gaps"`
	Questions []Question `json:"questions"`
	// Summary is an optional human-readable narrative the judge produces
	// alongside the structured verdict (decisions, reviewed items, etc).
	// It is rendered in cli mode under the phase header. May be empty.
	Summary string `json:"summary,omitempty"`
	// ReturnTo is the quality judge's diagnosis of where a failing
	// verdict should be re-run — code, plan, or escalate. Empty on
	// passing verdicts and on non-quality judges. The orchestrator uses
	// it to drive outer-loop rewinds rather than just retrying the same
	// phase.
	ReturnTo ReturnTo `json:"return_to,omitempty"`
}

// Attempt records one execution of a phase.
type Attempt struct {
	Phase     Phase     `json:"phase"`
	Loop      int       `json:"loop"`
	Verdict   *Verdict  `json:"verdict,omitempty"`
	PRUrl     string    `json:"pr_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// RewindContext captures why the outer loop rewound and what the next
// attempt must do differently. Without it, successive rewinds have no
// memory of earlier failures and tend to converge on the same output —
// the loop spins instead of terminating. Every rewind builds a new
// RewindContext from the quality verdict and appends it to the task so
// the history survives across cycles (no amnesia). The planner and
// coder treat the full slice as a first-class input: the prompt
// explicitly says "Previous attempt failed because X. You may not
// reproduce approach Y. Your plan must explicitly address Z."
type RewindContext struct {
	// Cycle is the outer-loop cycle number this rewind was diagnosed on
	// (1-indexed). Lets the prompt and logs distinguish repeats.
	Cycle int `json:"cycle"`
	// Target is the phase the outer loop rewound to: PhasePlan or
	// PhaseCode. Consumers filter the task's rewind history by Target
	// when they only want contexts aimed at their phase.
	Target Phase `json:"target"`
	// RootCause is the quality judge's one-line diagnosis of why the
	// attempt failed. Drives the "failed because X" line in the prompt.
	RootCause string `json:"root_cause"`
	// TriedApproach is a short description of what the previous attempt
	// produced — the approach the next attempt may not reproduce.
	TriedApproach string `json:"tried_approach,omitempty"`
	// Failure lists the concrete symptoms the judge observed (failing
	// checks, specific gaps). Rendered verbatim into the prompt so the
	// next attempt has the evidence, not just the diagnosis.
	Failure []string `json:"failure,omitempty"`
	// MustAddress is the set of constraints the next attempt must
	// explicitly satisfy — derived from blocking gaps. Separate from
	// Task.HardConstraints so the context survives for the coder too,
	// not just the planner on a ReturnToPlan.
	MustAddress []string `json:"must_address,omitempty"`
	// CreatedAt is when the rewind was diagnosed. Useful for ordering
	// the audit trail when attempts span a long wall-clock window.
	CreatedAt time.Time `json:"created_at"`
}

// RenderPromptBlock writes a prompt-ready rendering of the rewind
// context using the canonical "Previous attempt failed because X. You
// may not reproduce approach Y. Your plan must explicitly address Z."
// framing from issue #86. Every phase that consumes rewind contexts
// (planner, coder) uses this so the framing is identical across agents.
func (rc RewindContext) RenderPromptBlock(b *strings.Builder) {
	fmt.Fprintf(b, "\n### Cycle %d (rewind to %s)\n", rc.Cycle, rc.Target)
	if rc.RootCause != "" {
		fmt.Fprintf(b, "- Previous attempt failed because: %s\n", rc.RootCause)
	}
	if rc.TriedApproach != "" {
		// Indent with plain spaces rather than a markdown blockquote
		// prefix ('>'), so the block renders consistently across plain
		// text, markdown viewers, and agent prompts.
		b.WriteString("- You may not reproduce approach:\n")
		for _, line := range strings.Split(strings.TrimRight(rc.TriedApproach, "\n"), "\n") {
			fmt.Fprintf(b, "      %s\n", line)
		}
	}
	if len(rc.MustAddress) > 0 {
		b.WriteString("- The next attempt must explicitly address:\n")
		for _, m := range rc.MustAddress {
			fmt.Fprintf(b, "    - %s\n", m)
		}
	}
	if len(rc.Failure) > 0 {
		b.WriteString("- Observed failures:\n")
		for _, f := range rc.Failure {
			fmt.Fprintf(b, "    - %s\n", f)
		}
	}
}

// RewindContextsFor returns the subset of rewind contexts targeted at
// the given phase. Plan and code each consume only entries the outer
// loop rewound to their phase — a ReturnToPlan rewind isn't addressed
// to the coder, and vice versa.
func RewindContextsFor(all []RewindContext, target Phase) []RewindContext {
	if len(all) == 0 {
		return nil
	}
	out := make([]RewindContext, 0, len(all))
	for _, rc := range all {
		if rc.Target == target {
			out = append(out, rc)
		}
	}
	return out
}

// Task is the core entity tracked through the VAIrdict pipeline.
type Task struct {
	ID     string    `json:"id"`
	Intent string    `json:"intent"`
	State  TaskState `json:"state"`
	Phase  Phase     `json:"phase"`
	// LoopCount tracks loops completed for each phase within the current
	// outer cycle. Rewind resets the counter for the target phase (and
	// every later phase) so a fresh cycle gets its own budget — a code
	// retry triggered by a plan rewind must not count against the code
	// phase's original budget.
	LoopCount   map[Phase]int `json:"loop_count"`
	Assumptions []Assumption  `json:"assumptions"`
	Attempts    []Attempt     `json:"attempts"`
	// HardConstraints are requirements injected into the next plan run
	// by the outer loop — typically the quality judge's failure finding
	// when it rewinds to Plan. The planner must satisfy them in the new
	// plan, not just acknowledge them.
	HardConstraints []string `json:"hard_constraints,omitempty"`
	// RewindContexts accumulate structured failure context across every
	// outer-loop rewind. Each entry records the cycle it was diagnosed
	// on, the phase rewound to, and the root cause / approach / failure
	// details the next attempt must respond to. The slice is append-only
	// so multiple rewinds build history rather than overwriting earlier
	// diagnoses. Planner and coder read the entries targeted at their
	// phase to avoid reproducing failed approaches.
	RewindContexts []RewindContext `json:"rewind_contexts,omitempty"`
	// DependsOn lists task IDs this task waits on. The scheduler in
	// internal/deps uses it to build the DAG; vairdict status reads it
	// to render the graph. Empty for tasks without dependencies.
	DependsOn []string `json:"depends_on,omitempty"`
	// Priority is one of "high", "normal", "low". Drives dispatch order
	// in the scheduler when multiple tasks are ready at once. Empty or
	// missing is treated as normal.
	Priority string `json:"priority,omitempty"`
	// PlanOutput is the rendered plan text (requirements + implementation
	// plan) produced by the plan phase. Persisted so `vairdict resume`
	// can continue a later phase without regenerating a different plan
	// than the one the code on the worktree branch was built from.
	PlanOutput string `json:"plan_output,omitempty"`
	// PID is the OS process id of the currently attached runner, or 0
	// when no process is attached. Written when `vairdict run` (or
	// `resume`) begins orchestration and cleared on a clean exit. Used
	// by `vairdict status` to render a RUNNING indicator via a kill(pid, 0)
	// liveness check; stale PIDs (process died without cleanup) show as
	// not running and the task is resumable.
	PID       int       `json:"pid,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// IsResumable reports whether the task is in a non-terminal state and
// can be picked up by `vairdict resume`. Terminal states (done,
// escalated, blocked) return false; StatePending also returns false
// because a pending task has never started and should be launched via
// `vairdict run`, not resumed.
func (t *Task) IsResumable() bool {
	switch t.State {
	case StatePlanning, StatePlanReview,
		StateCoding, StateCodeReview,
		StateQuality, StateQualityReview:
		return true
	}
	return false
}

// ErrInvalidTransition is returned when an invalid state transition is attempted.
var ErrInvalidTransition = fmt.Errorf("invalid state transition")

// ErrMaxLoopsReached is returned when a phase has been requeued too many times.
var ErrMaxLoopsReached = fmt.Errorf("max loops reached, escalation required")

// Transition moves the task to a new state if the transition is valid.
// It updates the Phase field based on the new state.
func (t *Task) Transition(to TaskState) error {
	allowed, ok := validTransitions[t.State]
	if !ok {
		return fmt.Errorf("transitioning from %s to %s: %w", t.State, to, ErrInvalidTransition)
	}

	valid := false
	for _, s := range allowed {
		if s == to {
			valid = true
			break
		}
	}
	if !valid {
		return fmt.Errorf("transitioning from %s to %s: %w", t.State, to, ErrInvalidTransition)
	}

	t.State = to
	t.UpdatedAt = time.Now()

	if phase, exists := phaseForState[to]; exists {
		t.Phase = phase
	}

	return nil
}

// Requeue moves the task back to the active state of its current phase
// (e.g., plan_review -> planning) and increments the loop counter.
// It returns ErrMaxLoopsReached if the loop count would exceed maxLoops.
func (t *Task) Requeue(maxLoops int) error {
	phase, ok := phaseForState[t.State]
	if !ok {
		return fmt.Errorf("requeueing from state %s: %w", t.State, ErrInvalidTransition)
	}

	if t.LoopCount[phase]+1 >= maxLoops {
		if err := t.Transition(StateEscalated); err != nil {
			return fmt.Errorf("escalating task: %w", err)
		}
		return ErrMaxLoopsReached
	}

	// Determine the target active state for requeue.
	var target TaskState
	switch phase {
	case PhasePlan:
		target = StatePlanning
	case PhaseCode:
		target = StateCoding
	case PhaseQuality:
		target = StateQuality
	}

	if err := t.Transition(target); err != nil {
		return fmt.Errorf("requeueing task: %w", err)
	}

	t.LoopCount[phase]++
	return nil
}

// Rewind moves the task from its current review state back to the active
// state of an earlier phase and resets the per-cycle loop budget for the
// target phase and every phase after it. Only PhaseCode and PhasePlan are
// valid rewind targets — escalation is its own terminal state, not a
// rewind. The caller is responsible for setting any HardConstraints that
// should flow into replanning before calling Rewind.
//
// Budget semantics: LoopCount tracks loops within the current outer cycle.
// A rewind starts a new cycle for the phases downstream of the target, so
// their counters are zeroed. The audit trail (task.Attempts) keeps every
// historical attempt; the counter reset only affects future budget checks.
func (t *Task) Rewind(to Phase) error {
	var target TaskState
	switch to {
	case PhasePlan:
		target = StatePlanning
	case PhaseCode:
		target = StateCoding
	default:
		return fmt.Errorf("rewinding to %s: %w", to, ErrInvalidTransition)
	}
	if err := t.Transition(target); err != nil {
		return fmt.Errorf("rewinding: %w", err)
	}

	// Reset the per-cycle budget for the target phase and every phase
	// that follows it. A plan rewind gives the code phase a fresh
	// budget (and quality, after code); a code rewind only resets code
	// and quality. Earlier-phase counters stay intact.
	resetFrom := false
	if t.LoopCount == nil {
		t.LoopCount = map[Phase]int{}
	}
	for _, p := range AllPhases() {
		if p == to {
			resetFrom = true
		}
		if resetFrom {
			delete(t.LoopCount, p)
		}
	}

	return nil
}

// NewTask creates a new task in the pending state.
func NewTask(id, intent string) *Task {
	now := time.Now()
	return &Task{
		ID:        id,
		Intent:    intent,
		State:     StatePending,
		LoopCount: make(map[Phase]int),
		CreatedAt: now,
		UpdatedAt: now,
	}
}
