package state

import (
	"errors"
	"strings"
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

func TestRewindToCode_ResetsCodeAndQualityBudget(t *testing.T) {
	// Issue #87: "a code retry triggered by a plan rewind does not count
	// against the code phase's original budget". More generally, a rewind
	// resets the per-cycle counter for the target phase and every phase
	// downstream. The plan counter stays put — the rewind doesn't
	// touch the phase we're bypassing.
	task := NewTask("t-rewind-code", "intent")
	task.LoopCount = map[Phase]int{
		PhasePlan:    2,
		PhaseCode:    3,
		PhaseQuality: 1,
	}
	// Advance through the state machine to QualityReview so the
	// downstream transition is valid.
	for _, s := range []TaskState{
		StatePlanning, StatePlanReview, StateCoding, StateCodeReview,
		StateQuality, StateQualityReview,
	} {
		if err := task.Transition(s); err != nil {
			t.Fatalf("setup transition to %s: %v", s, err)
		}
	}

	if err := task.Rewind(PhaseCode); err != nil {
		t.Fatalf("rewind to code should succeed from quality_review: %v", err)
	}

	if task.State != StateCoding {
		t.Errorf("state = %s, want coding", task.State)
	}
	if task.LoopCount[PhasePlan] != 2 {
		t.Errorf("plan budget should be preserved on code rewind, got %d", task.LoopCount[PhasePlan])
	}
	if _, ok := task.LoopCount[PhaseCode]; ok {
		t.Errorf("code budget should be reset on code rewind, got %d", task.LoopCount[PhaseCode])
	}
	if _, ok := task.LoopCount[PhaseQuality]; ok {
		t.Errorf("quality budget should be reset on code rewind, got %d", task.LoopCount[PhaseQuality])
	}
}

func TestRewindToPlan_ResetsAllPhaseBudgets(t *testing.T) {
	// A plan rewind resets every downstream phase's budget — the code
	// phase is downstream of plan, so it must not inherit the old
	// counter from before the rewind.
	task := NewTask("t-rewind-plan", "intent")
	task.LoopCount = map[Phase]int{
		PhasePlan:    1,
		PhaseCode:    2,
		PhaseQuality: 3,
	}
	for _, s := range []TaskState{
		StatePlanning, StatePlanReview, StateCoding, StateCodeReview,
		StateQuality, StateQualityReview,
	} {
		if err := task.Transition(s); err != nil {
			t.Fatalf("setup transition to %s: %v", s, err)
		}
	}

	if err := task.Rewind(PhasePlan); err != nil {
		t.Fatalf("rewind to plan should succeed from quality_review: %v", err)
	}
	if task.State != StatePlanning {
		t.Errorf("state = %s, want planning", task.State)
	}
	for _, p := range []Phase{PhasePlan, PhaseCode, PhaseQuality} {
		if _, ok := task.LoopCount[p]; ok {
			t.Errorf("%s budget should be reset on plan rewind, got %d", p, task.LoopCount[p])
		}
	}
}

func TestRewind_RejectsInvalidTarget(t *testing.T) {
	task := NewTask("t", "intent")
	for _, s := range []TaskState{
		StatePlanning, StatePlanReview, StateCoding, StateCodeReview,
		StateQuality, StateQualityReview,
	} {
		_ = task.Transition(s)
	}
	if err := task.Rewind(PhaseQuality); err == nil {
		t.Error("rewinding to quality should fail — it is not a valid rewind target")
	}
	if err := task.Rewind(Phase("bogus")); err == nil {
		t.Error("rewinding to an unknown phase should fail")
	}
}

func TestRewind_FromNonReviewState(t *testing.T) {
	// Rewinds are only valid from review states (specifically
	// quality_review, per validTransitions). Trying to rewind from
	// active states like StateCoding must error.
	task := NewTask("t", "intent")
	_ = task.Transition(StatePlanning)
	_ = task.Transition(StatePlanReview)
	_ = task.Transition(StateCoding)

	if err := task.Rewind(PhasePlan); err == nil {
		t.Error("rewind from coding should fail — no valid transition coding → planning")
	}
}

func TestRewindThenRequeue_UsesFreshBudget(t *testing.T) {
	// End-to-end budget behavior: after rewind, subsequent requeues in
	// the target phase count from zero — the quality failure that
	// triggered the rewind does not count against the code phase's
	// new-cycle budget.
	task := NewTask("t", "intent")
	for _, s := range []TaskState{
		StatePlanning, StatePlanReview, StateCoding, StateCodeReview,
	} {
		_ = task.Transition(s)
	}
	// Exhaust code's first-cycle budget nearly to the limit.
	task.LoopCount[PhaseCode] = 2
	// Advance through quality review.
	_ = task.Transition(StateQuality)
	_ = task.Transition(StateQualityReview)

	if err := task.Rewind(PhaseCode); err != nil {
		t.Fatalf("rewind: %v", err)
	}
	// After rewind the code phase should be able to loop the full
	// budget again without triggering ErrMaxLoopsReached.
	_ = task.Transition(StateCodeReview)
	if err := task.Requeue(3); err != nil {
		t.Fatalf("first requeue in new cycle should not hit max loops (maxLoops=3, fresh budget), got %v", err)
	}
	if task.LoopCount[PhaseCode] != 1 {
		t.Errorf("post-rewind code loop count = %d, want 1", task.LoopCount[PhaseCode])
	}
}

func TestQualityReviewTransitions_IncludeRewinds(t *testing.T) {
	// The state machine must allow cross-phase rewinds from
	// quality_review. Missing transitions would manifest as
	// ErrInvalidTransition deep inside the orchestrator.
	for _, target := range []TaskState{StateCoding, StatePlanning} {
		t.Run(string(target), func(t *testing.T) {
			task := NewTask("t-"+string(target), "intent")
			for _, s := range []TaskState{
				StatePlanning, StatePlanReview, StateCoding, StateCodeReview,
				StateQuality, StateQualityReview,
			} {
				if err := task.Transition(s); err != nil {
					t.Fatalf("setup %s: %v", s, err)
				}
			}
			if err := task.Transition(target); err != nil {
				t.Errorf("quality_review → %s should be allowed: %v", target, err)
			}
		})
	}
}

func TestRewindContextsFor_FiltersByTarget(t *testing.T) {
	all := []RewindContext{
		{Cycle: 1, Target: PhasePlan, RootCause: "A"},
		{Cycle: 1, Target: PhaseCode, RootCause: "B"},
		{Cycle: 2, Target: PhasePlan, RootCause: "C"},
	}

	plan := RewindContextsFor(all, PhasePlan)
	if len(plan) != 2 {
		t.Fatalf("expected 2 plan-targeted contexts, got %d", len(plan))
	}
	if plan[0].RootCause != "A" || plan[1].RootCause != "C" {
		t.Errorf("unexpected plan-targeted contexts: %+v", plan)
	}

	code := RewindContextsFor(all, PhaseCode)
	if len(code) != 1 || code[0].RootCause != "B" {
		t.Errorf("expected one code-targeted context B, got %+v", code)
	}

	if got := RewindContextsFor(nil, PhasePlan); got != nil {
		t.Errorf("nil input must yield nil output, got %v", got)
	}
}

func TestRewindContext_RenderPromptBlock_Framing(t *testing.T) {
	// The canonical framing from issue #86 is load-bearing — if any of
	// the three required phrases disappears the prompt no longer tells
	// the agent what it must not reproduce, and rewinds converge.
	rc := RewindContext{
		Cycle:         2,
		Target:        PhaseCode,
		RootCause:     "missing timeout",
		TriedApproach: "call fn without ctx",
		MustAddress:   []string{"wire ctx into fn"},
		Failure:       []string{"[P0] TestTimeout failed"},
	}
	var b strings.Builder
	rc.RenderPromptBlock(&b)
	out := b.String()

	for _, phrase := range []string{
		"Previous attempt failed because:",
		"You may not reproduce approach",
		"must explicitly address",
		"missing timeout",
		"call fn without ctx",
		"wire ctx into fn",
		"TestTimeout failed",
		"Cycle 2",
	} {
		if !strings.Contains(out, phrase) {
			t.Errorf("RenderPromptBlock must include %q — got:\n%s", phrase, out)
		}
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
