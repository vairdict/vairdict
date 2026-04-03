package state

import (
	"errors"
	"testing"
)

func TestNewTask(t *testing.T) {
	task := NewTask("task-1", "implement feature X")

	if task.ID != "task-1" {
		t.Errorf("expected ID task-1, got %s", task.ID)
	}
	if task.Intent != "implement feature X" {
		t.Errorf("expected intent 'implement feature X', got %s", task.Intent)
	}
	if task.State != StatePending {
		t.Errorf("expected state pending, got %s", task.State)
	}
	if task.LoopCount == nil {
		t.Fatal("expected LoopCount to be initialized")
	}
	if task.CreatedAt.IsZero() {
		t.Error("expected CreatedAt to be set")
	}
	if task.UpdatedAt.IsZero() {
		t.Error("expected UpdatedAt to be set")
	}
}

func TestValidTransitions(t *testing.T) {
	// The happy path: pending -> planning -> plan_review -> coding -> code_review -> quality -> quality_review -> done
	transitions := []TaskState{
		StatePlanning, StatePlanReview, StateCoding,
		StateCodeReview, StateQuality, StateQualityReview, StateDone,
	}

	task := NewTask("t-1", "happy path")
	for _, next := range transitions {
		prev := task.State
		if err := task.Transition(next); err != nil {
			t.Errorf("transition %s -> %s should be valid, got: %v", prev, next, err)
		}
		if task.State != next {
			t.Errorf("expected state %s, got %s", next, task.State)
		}
	}
}

func TestInvalidTransitions(t *testing.T) {
	cases := []struct {
		name string
		from TaskState
		to   TaskState
	}{
		{"pending to coding", StatePending, StateCoding},
		{"pending to done", StatePending, StateDone},
		{"planning to coding", StatePlanning, StateCoding},
		{"coding to planning", StateCoding, StatePlanning},
		{"done to pending", StateDone, StatePending},
		{"escalated to pending", StateEscalated, StatePending},
		{"plan_review to quality", StatePlanReview, StateQuality},
		{"code_review to done", StateCodeReview, StateDone},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			task := NewTask("t-inv", "test")
			task.State = tc.from

			err := task.Transition(tc.to)
			if err == nil {
				t.Errorf("expected error for transition %s -> %s", tc.from, tc.to)
			}
			if !errors.Is(err, ErrInvalidTransition) {
				t.Errorf("expected ErrInvalidTransition, got: %v", err)
			}
		})
	}
}

func TestTransitionUpdatesPhase(t *testing.T) {
	task := NewTask("t-phase", "test phases")

	_ = task.Transition(StatePlanning)
	if task.Phase != PhasePlan {
		t.Errorf("expected phase plan, got %s", task.Phase)
	}

	_ = task.Transition(StatePlanReview)
	if task.Phase != PhasePlan {
		t.Errorf("expected phase plan during review, got %s", task.Phase)
	}

	_ = task.Transition(StateCoding)
	if task.Phase != PhaseCode {
		t.Errorf("expected phase code, got %s", task.Phase)
	}

	_ = task.Transition(StateCodeReview)
	if task.Phase != PhaseCode {
		t.Errorf("expected phase code during review, got %s", task.Phase)
	}

	_ = task.Transition(StateQuality)
	if task.Phase != PhaseQuality {
		t.Errorf("expected phase quality, got %s", task.Phase)
	}

	_ = task.Transition(StateQualityReview)
	if task.Phase != PhaseQuality {
		t.Errorf("expected phase quality during review, got %s", task.Phase)
	}
}

func TestRequeueIncrementsLoopCount(t *testing.T) {
	task := NewTask("t-loop", "test loops")
	_ = task.Transition(StatePlanning)
	_ = task.Transition(StatePlanReview)

	if err := task.Requeue(3); err != nil {
		t.Fatalf("requeue should succeed: %v", err)
	}

	if task.State != StatePlanning {
		t.Errorf("expected state planning after requeue, got %s", task.State)
	}
	if task.LoopCount[PhasePlan] != 1 {
		t.Errorf("expected loop count 1, got %d", task.LoopCount[PhasePlan])
	}

	// Second requeue.
	_ = task.Transition(StatePlanReview)
	if err := task.Requeue(3); err != nil {
		t.Fatalf("second requeue should succeed: %v", err)
	}
	if task.LoopCount[PhasePlan] != 2 {
		t.Errorf("expected loop count 2, got %d", task.LoopCount[PhasePlan])
	}
}

func TestRequeueEscalatesAtMaxLoops(t *testing.T) {
	task := NewTask("t-esc", "test escalation")
	_ = task.Transition(StatePlanning)
	_ = task.Transition(StatePlanReview)

	// Loop count is 0 initially. With maxLoops=1, should escalate immediately.
	// Because 0+1 >= 1 is true.
	err := task.Requeue(1)
	if !errors.Is(err, ErrMaxLoopsReached) {
		t.Errorf("expected ErrMaxLoopsReached, got: %v", err)
	}
	if task.State != StateEscalated {
		t.Errorf("expected state escalated, got %s", task.State)
	}
}

func TestRequeueEscalatesAtMaxLoopsAfterRetries(t *testing.T) {
	task := NewTask("t-esc2", "test escalation after retries")
	maxLoops := 3

	// Go through plan phase.
	_ = task.Transition(StatePlanning)
	_ = task.Transition(StatePlanReview)

	// First two requeues succeed.
	_ = task.Requeue(maxLoops)
	_ = task.Transition(StatePlanReview)
	_ = task.Requeue(maxLoops)
	_ = task.Transition(StatePlanReview)

	// Third requeue should escalate (loop count is 2, 2+1 >= 3).
	err := task.Requeue(maxLoops)
	if !errors.Is(err, ErrMaxLoopsReached) {
		t.Errorf("expected ErrMaxLoopsReached, got: %v", err)
	}
	if task.State != StateEscalated {
		t.Errorf("expected state escalated, got %s", task.State)
	}
}

func TestRequeueFromNonReviewState(t *testing.T) {
	task := NewTask("t-bad-requeue", "test bad requeue")
	_ = task.Transition(StatePlanning)

	// Requeueing from planning (not a review state) should fail.
	// planning -> planning is not a valid transition.
	err := task.Requeue(3)
	if err == nil {
		t.Error("expected error when requeueing from non-review state")
	}
}

func TestRequeueCodePhase(t *testing.T) {
	task := NewTask("t-code-loop", "test code requeue")
	_ = task.Transition(StatePlanning)
	_ = task.Transition(StatePlanReview)
	_ = task.Transition(StateCoding)
	_ = task.Transition(StateCodeReview)

	if err := task.Requeue(3); err != nil {
		t.Fatalf("requeue code phase should succeed: %v", err)
	}
	if task.State != StateCoding {
		t.Errorf("expected state coding, got %s", task.State)
	}
	if task.LoopCount[PhaseCode] != 1 {
		t.Errorf("expected code loop count 1, got %d", task.LoopCount[PhaseCode])
	}
	// Plan loop count should be untouched.
	if task.LoopCount[PhasePlan] != 0 {
		t.Errorf("expected plan loop count 0, got %d", task.LoopCount[PhasePlan])
	}
}

func TestRequeueQualityPhase(t *testing.T) {
	task := NewTask("t-qual-loop", "test quality requeue")
	_ = task.Transition(StatePlanning)
	_ = task.Transition(StatePlanReview)
	_ = task.Transition(StateCoding)
	_ = task.Transition(StateCodeReview)
	_ = task.Transition(StateQuality)
	_ = task.Transition(StateQualityReview)

	if err := task.Requeue(3); err != nil {
		t.Fatalf("requeue quality phase should succeed: %v", err)
	}
	if task.State != StateQuality {
		t.Errorf("expected state quality, got %s", task.State)
	}
	if task.LoopCount[PhaseQuality] != 1 {
		t.Errorf("expected quality loop count 1, got %d", task.LoopCount[PhaseQuality])
	}
}

func TestEscalationFromAllReviewStates(t *testing.T) {
	reviews := []struct {
		name   string
		setup  func(*Task)
		review TaskState
	}{
		{"plan_review", func(task *Task) {
			_ = task.Transition(StatePlanning)
			_ = task.Transition(StatePlanReview)
		}, StatePlanReview},
		{"code_review", func(task *Task) {
			_ = task.Transition(StatePlanning)
			_ = task.Transition(StatePlanReview)
			_ = task.Transition(StateCoding)
			_ = task.Transition(StateCodeReview)
		}, StateCodeReview},
		{"quality_review", func(task *Task) {
			_ = task.Transition(StatePlanning)
			_ = task.Transition(StatePlanReview)
			_ = task.Transition(StateCoding)
			_ = task.Transition(StateCodeReview)
			_ = task.Transition(StateQuality)
			_ = task.Transition(StateQualityReview)
		}, StateQualityReview},
	}

	for _, tc := range reviews {
		t.Run(tc.name, func(t *testing.T) {
			task := NewTask("t-esc-"+tc.name, "test")
			tc.setup(task)

			err := task.Transition(StateEscalated)
			if err != nil {
				t.Errorf("escalation from %s should be valid: %v", tc.review, err)
			}
			if task.State != StateEscalated {
				t.Errorf("expected state escalated, got %s", task.State)
			}
		})
	}
}

func TestAllPhases(t *testing.T) {
	phases := AllPhases()
	if len(phases) != 3 {
		t.Errorf("expected 3 phases, got %d", len(phases))
	}
	if phases[0] != PhasePlan || phases[1] != PhaseCode || phases[2] != PhaseQuality {
		t.Errorf("unexpected phase order: %v", phases)
	}
}
