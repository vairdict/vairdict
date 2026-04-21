package code

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/standards"
	"github.com/vairdict/vairdict/internal/state"
)

func TestBuildCoderPrompt_IncludesBaseline(t *testing.T) {
	// #84: the coder must see the non-negotiable standards so it doesn't
	// write code that would be flagged during quality.
	prompt := buildCoderPrompt("do stuff", "step 1", "", nil)
	if !strings.Contains(prompt, standards.Block) {
		t.Error("coder prompt must include the baseline standards block")
	}
	for _, tag := range standards.AllRules {
		if !strings.Contains(prompt, string(tag)) {
			t.Errorf("coder prompt missing baseline rule tag %q", tag)
		}
	}
}

// fakeCoder returns configurable results.
type fakeCoder struct {
	results []state.AgentResult
	err     error
	calls   int
}

func (f *fakeCoder) Run(_ context.Context, _ string, _ string) (state.AgentResult, error) {
	if f.err != nil {
		return state.AgentResult{}, f.err
	}
	idx := f.calls
	f.calls++
	if idx < len(f.results) {
		return f.results[idx], nil
	}
	return state.AgentResult{Output: "code written"}, nil
}

// fakeJudge returns configurable verdicts.
type fakeJudge struct {
	verdicts []*state.Verdict
	err      error
	calls    int
}

func (f *fakeJudge) Judge(_ context.Context, _ string) (*state.Verdict, error) {
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

func codingTask() *state.Task {
	t := state.NewTask("test-1", "implement feature X")
	// Advance to coding state.
	_ = t.Transition(state.StatePlanning)
	_ = t.Transition(state.StatePlanReview)
	_ = t.Transition(state.StateCoding)
	return t
}

func defaultCfg() config.CodePhaseConfig {
	return config.CodePhaseConfig{
		MaxLoops:        3,
		RequireTests:    true,
		CoverageMinimum: 70,
	}
}

func TestRun_PassFirstTry(t *testing.T) {
	coder := &fakeCoder{results: []state.AgentResult{{Output: "done"}}}
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{Score: 100, Pass: true},
	}}

	task := codingTask()
	phase := New(coder, judge, defaultCfg(), "/work")

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
	if task.State != state.StateQuality {
		t.Errorf("state = %s, want quality", task.State)
	}
	if len(task.Attempts) != 1 {
		t.Errorf("attempts = %d, want 1", len(task.Attempts))
	}
}

func TestRun_PassOnRetry(t *testing.T) {
	coder := &fakeCoder{}
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{Score: 50, Pass: false, Gaps: []state.Gap{{Severity: state.SeverityP1, Description: "test failed", Blocking: true}}},
		{Score: 100, Pass: true},
	}}

	task := codingTask()
	phase := New(coder, judge, defaultCfg(), "/work")

	result, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Pass {
		t.Error("expected pass on retry")
	}
	if result.Loops != 2 {
		t.Errorf("loops = %d, want 2", result.Loops)
	}
	if len(task.Attempts) != 2 {
		t.Errorf("attempts = %d, want 2", len(task.Attempts))
	}
}

func TestRun_Escalation(t *testing.T) {
	coder := &fakeCoder{}
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{Score: 25, Pass: false, Gaps: []state.Gap{{Severity: state.SeverityP0, Description: "build broken"}}},
	}}

	task := codingTask()
	cfg := defaultCfg()
	cfg.MaxLoops = 2
	phase := New(coder, judge, cfg, "/work")

	result, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !result.Escalate {
		t.Error("expected escalation")
	}
	if result.Pass {
		t.Error("should not pass")
	}
}

func TestRun_CoderError(t *testing.T) {
	coder := &fakeCoder{err: errors.New("claude crashed")}
	judge := &fakeJudge{verdicts: []*state.Verdict{{Score: 100, Pass: true}}}

	task := codingTask()
	phase := New(coder, judge, defaultCfg(), "/work")

	_, err := phase.Run(context.Background(), task, "the plan")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRun_JudgeError(t *testing.T) {
	coder := &fakeCoder{}
	judge := &fakeJudge{err: errors.New("spm crashed")}

	task := codingTask()
	phase := New(coder, judge, defaultCfg(), "/work")

	_, err := phase.Run(context.Background(), task, "the plan")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRun_WrongState(t *testing.T) {
	coder := &fakeCoder{}
	judge := &fakeJudge{verdicts: []*state.Verdict{{Score: 100, Pass: true}}}

	task := state.NewTask("test-1", "intent")
	phase := New(coder, judge, defaultCfg(), "/work")

	_, err := phase.Run(context.Background(), task, "plan")
	if err == nil {
		t.Fatal("expected error for wrong state")
	}
}

func TestRun_AttemptsStored(t *testing.T) {
	coder := &fakeCoder{}
	judge := &fakeJudge{verdicts: []*state.Verdict{
		{Score: 50, Pass: false},
		{Score: 75, Pass: false},
		{Score: 100, Pass: true},
	}}

	task := codingTask()
	phase := New(coder, judge, defaultCfg(), "/work")

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
		if a.Phase != state.PhaseCode {
			t.Errorf("attempt[%d].phase = %s, want code", i, a.Phase)
		}
		if a.Loop != i+1 {
			t.Errorf("attempt[%d].loop = %d, want %d", i, a.Loop, i+1)
		}
	}
}

func TestBuildCoderPrompt(t *testing.T) {
	prompt := buildCoderPrompt("intent", "plan", "fix tests", []state.Assumption{
		{Severity: state.SeverityP2, Description: "assumed X"},
	})

	if !containsStr(prompt, "intent") {
		t.Error("prompt should contain intent")
	}
	if !containsStr(prompt, "plan") {
		t.Error("prompt should contain plan")
	}
	if !containsStr(prompt, "fix tests") {
		t.Error("prompt should contain feedback")
	}
	if !containsStr(prompt, "assumed X") {
		t.Error("prompt should contain assumptions")
	}
	if !containsStr(prompt, "Avoid duplicating logic") {
		t.Error("prompt should contain code-reuse guidance")
	}
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
