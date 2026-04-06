package quality

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// fakeJudge returns configurable verdicts in order.
type fakeJudge struct {
	verdicts []*state.Verdict
	err      error
	calls    int
}

func (f *fakeJudge) Judge(_ context.Context, _, _, _ string) (*state.Verdict, error) {
	if f.err != nil {
		return nil, f.err
	}
	idx := f.calls
	f.calls++
	if idx < len(f.verdicts) {
		return f.verdicts[idx], nil
	}
	return f.verdicts[len(f.verdicts)-1], nil
}

func qualityTask(t *testing.T) *state.Task {
	t.Helper()
	task := state.NewTask("test-1", "implement feature X")
	for _, s := range []state.TaskState{
		state.StatePlanning,
		state.StatePlanReview,
		state.StateCoding,
		state.StateCodeReview,
		state.StateQuality,
	} {
		if err := task.Transition(s); err != nil {
			t.Fatalf("setup: transition to %s: %v", s, err)
		}
	}
	return task
}

func defaultCfg() config.QualityPhaseConfig {
	return config.QualityPhaseConfig{
		MaxLoops:     3,
		E2ERequired:  false,
		PRReviewMode: "auto",
	}
}

func TestRun_PassFirstTry(t *testing.T) {
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{Score: 95, Pass: true},
	}}

	task := qualityTask(t)
	phase := New(judge, defaultCfg(), "/work")

	result, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Pass {
		t.Error("expected pass")
	}
	if result.Loops != 1 {
		t.Errorf("loops = %d, want 1", result.Loops)
	}
	if task.State != state.StateDone {
		t.Errorf("state = %s, want done", task.State)
	}
	if len(task.Attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(task.Attempts))
	}
	if task.Attempts[0].Phase != state.PhaseQuality {
		t.Errorf("attempt phase = %s, want quality", task.Attempts[0].Phase)
	}
}

func TestRun_PassOnRetry_NonBlockingGaps(t *testing.T) {
	// Loop 1: fails with non-blocking P2 gaps (e.g. flaky e2e treated as P2).
	// Loop 2: passes after re-judge.
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{
			Score: 60,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "minor polish", Blocking: false},
			},
		},
		{Score: 90, Pass: true},
	}}

	task := qualityTask(t)
	phase := New(judge, defaultCfg(), "/work")

	result, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Pass {
		t.Errorf("expected pass on retry, got %+v", result)
	}
	if result.Loops != 2 {
		t.Errorf("loops = %d, want 2", result.Loops)
	}
	if task.State != state.StateDone {
		t.Errorf("state = %s, want done", task.State)
	}
	if len(task.Attempts) != 2 {
		t.Errorf("attempts = %d, want 2", len(task.Attempts))
	}
}

func TestRun_RequeueToCode_OnP0Gap(t *testing.T) {
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{
			Score: 30,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "intent mismatch", Blocking: true},
			},
		},
	}}

	task := qualityTask(t)
	phase := New(judge, defaultCfg(), "/work")

	result, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.RequeueToCode {
		t.Errorf("expected RequeueToCode, got %+v", result)
	}
	if result.Pass {
		t.Error("should not pass")
	}
	if result.Escalate {
		t.Error("should not escalate yet — orchestrator routes to code")
	}
	if result.Loops != 1 {
		t.Errorf("loops = %d, want 1", result.Loops)
	}
	if !strings.Contains(result.Feedback, "intent mismatch") {
		t.Errorf("feedback should contain gap description, got %q", result.Feedback)
	}
}

func TestRun_RequeueToCode_OnP1Gap(t *testing.T) {
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{
			Score: 55,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP1, Description: "e2e tests failed", Blocking: true},
			},
		},
	}}

	task := qualityTask(t)
	phase := New(judge, defaultCfg(), "/work")

	result, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.RequeueToCode {
		t.Errorf("expected RequeueToCode for P1 blocking, got %+v", result)
	}
}

func TestRun_Escalation_NonBlockingLoopOut(t *testing.T) {
	// Every loop fails with non-blocking gaps → loops exhausted → escalate.
	failing := &state.Verdict{
		Score: 60,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityP2, Description: "polish", Blocking: false},
		},
	}
	judge := &fakeJudge{verdicts: []*state.Verdict{failing, failing, failing}}

	task := qualityTask(t)
	cfg := defaultCfg()
	cfg.MaxLoops = 2
	phase := New(judge, cfg, "/work")

	result, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Escalate {
		t.Errorf("expected escalation, got %+v", result)
	}
	if result.Pass {
		t.Error("should not pass")
	}
	if result.RequeueToCode {
		t.Error("should not requeue to code (no blocking gaps)")
	}
}

func TestRun_JudgeError(t *testing.T) {
	judge := &fakeJudge{err: errors.New("claude crashed")}

	task := qualityTask(t)
	phase := New(judge, defaultCfg(), "/work")

	_, err := phase.Run(context.Background(), task, "the plan")
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "running quality judge") {
		t.Errorf("error should be wrapped, got %v", err)
	}
}

func TestRun_WrongState(t *testing.T) {
	judge := &fakeJudge{verdicts: []*state.Verdict{{Score: 100, Pass: true}}}

	task := state.NewTask("test-1", "intent")
	phase := New(judge, defaultCfg(), "/work")

	_, err := phase.Run(context.Background(), task, "plan")
	if err == nil {
		t.Fatal("expected error for wrong state")
	}
	if !strings.Contains(err.Error(), "unexpected state") {
		t.Errorf("error should mention state, got %v", err)
	}
}

func TestRun_AttemptsStored(t *testing.T) {
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{Score: 60, Pass: false, Gaps: []state.Gap{{Severity: state.SeverityP2, Blocking: false, Description: "x"}}},
		{Score: 65, Pass: false, Gaps: []state.Gap{{Severity: state.SeverityP2, Blocking: false, Description: "y"}}},
		{Score: 95, Pass: true},
	}}

	task := qualityTask(t)
	phase := New(judge, defaultCfg(), "/work")

	result, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Pass {
		t.Error("expected pass")
	}
	if len(task.Attempts) != 3 {
		t.Errorf("attempts = %d, want 3", len(task.Attempts))
	}
	for i, a := range task.Attempts {
		if a.Phase != state.PhaseQuality {
			t.Errorf("attempt[%d].phase = %s, want quality", i, a.Phase)
		}
		if a.Loop != i+1 {
			t.Errorf("attempt[%d].loop = %d, want %d", i, a.Loop, i+1)
		}
	}
}

func TestNeedsCodeRework(t *testing.T) {
	tests := []struct {
		name string
		v    *state.Verdict
		want bool
	}{
		{
			name: "no gaps",
			v:    &state.Verdict{},
			want: false,
		},
		{
			name: "non-blocking P2",
			v: &state.Verdict{Gaps: []state.Gap{
				{Severity: state.SeverityP2, Blocking: false},
			}},
			want: false,
		},
		{
			name: "blocking P0",
			v: &state.Verdict{Gaps: []state.Gap{
				{Severity: state.SeverityP0, Blocking: true},
			}},
			want: true,
		},
		{
			name: "blocking P1",
			v: &state.Verdict{Gaps: []state.Gap{
				{Severity: state.SeverityP1, Blocking: true},
			}},
			want: true,
		},
		{
			name: "blocking P3 (unusual)",
			v: &state.Verdict{Gaps: []state.Gap{
				{Severity: state.SeverityP3, Blocking: true},
			}},
			want: false,
		},
		{
			name: "mix: non-blocking P0, blocking P2",
			v: &state.Verdict{Gaps: []state.Gap{
				{Severity: state.SeverityP0, Blocking: false},
				{Severity: state.SeverityP2, Blocking: true},
			}},
			want: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := needsCodeRework(tc.v); got != tc.want {
				t.Errorf("needsCodeRework = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBuildQualityFeedback(t *testing.T) {
	v := &state.Verdict{
		Score: 72.5,
		Gaps: []state.Gap{
			{Severity: state.SeverityP1, Description: "thing missing", Blocking: true},
			{Severity: state.SeverityP3, Description: "nice to have", Blocking: false},
		},
		Questions: []state.Question{
			{Text: "is X required?", Priority: "high"},
		},
	}
	out := buildQualityFeedback(v)
	for _, want := range []string{"72.5", "thing missing", "[BLOCKING]", "nice to have", "is X required?", "high"} {
		if !strings.Contains(out, want) {
			t.Errorf("feedback missing %q\ngot:\n%s", want, out)
		}
	}
}
