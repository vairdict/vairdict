package state

import (
	"fmt"
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
)

// validTransitions defines which state transitions are allowed.
var validTransitions = map[TaskState][]TaskState{
	StatePending:       {StatePlanning},
	StatePlanning:      {StatePlanReview},
	StatePlanReview:    {StatePlanning, StateCoding, StateEscalated},
	StateCoding:        {StateCodeReview},
	StateCodeReview:    {StateCoding, StateQuality, StateEscalated},
	StateQuality:       {StateQualityReview},
	StateQualityReview: {StateQuality, StateDone, StateEscalated},
	StateDone:          {},
	StateEscalated:     {},
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
}

// Question represents a question raised by a judge.
type Question struct {
	Text     string `json:"text"`
	Priority string `json:"priority"`
}

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
}

// Attempt records one execution of a phase.
type Attempt struct {
	Phase     Phase     `json:"phase"`
	Loop      int       `json:"loop"`
	Verdict   *Verdict  `json:"verdict,omitempty"`
	PRUrl     string    `json:"pr_url,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// Task is the core entity tracked through the VAIrdict pipeline.
type Task struct {
	ID          string        `json:"id"`
	Intent      string        `json:"intent"`
	State       TaskState     `json:"state"`
	Phase       Phase         `json:"phase"`
	LoopCount   map[Phase]int `json:"loop_count"`
	Assumptions []Assumption  `json:"assumptions"`
	Attempts    []Attempt     `json:"attempts"`
	CreatedAt   time.Time     `json:"created_at"`
	UpdatedAt   time.Time     `json:"updated_at"`
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
