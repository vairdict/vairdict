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
	prompt := buildCoderPrompt("do stuff", "step 1", "", nil, nil)
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
	}, nil)

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

func TestBuildCoderPrompt_WithRewindContext(t *testing.T) {
	// #86: when the outer loop rewinds to code, the coder must see
	// structured context — root cause, prior approach, what to address —
	// with the mandated framing. Shallow "fix these gaps" feedback
	// alone lets the coder regenerate the same broken approach.
	contexts := []state.RewindContext{
		{
			Cycle:         2,
			Target:        state.PhaseCode,
			RootCause:     "retry loop grew unbounded",
			TriedApproach: "wrap in for-range without cap",
			MustAddress:   []string{"cap retries at 3"},
			Failure:       []string{"[P0] TestRetryLimit failed"},
		},
	}
	prompt := buildCoderPrompt("intent", "plan", "", nil, contexts)

	if !strings.Contains(prompt, "Rewind Context") {
		t.Error("expected rewind context section header")
	}
	if !strings.Contains(prompt, "Previous attempt failed because: retry loop grew unbounded") {
		t.Error("expected the mandated 'failed because X' framing")
	}
	if !strings.Contains(prompt, "You may not reproduce approach") {
		t.Error("expected the mandated 'may not reproduce' framing")
	}
	if !strings.Contains(prompt, "wrap in for-range without cap") {
		t.Error("expected prior approach text in the 'may not reproduce' block")
	}
	if !strings.Contains(prompt, "must explicitly address") {
		t.Error("expected the mandated 'must explicitly address' framing")
	}
	if !strings.Contains(prompt, "cap retries at 3") {
		t.Error("expected MustAddress entries rendered into the prompt")
	}
	if !strings.Contains(prompt, "TestRetryLimit failed") {
		t.Error("expected observed failure list rendered into the prompt")
	}
}

func TestCodePhase_RewindContextPropagatedToCoder(t *testing.T) {
	// End-to-end: when the task already carries a code-targeted rewind
	// context, the very next coder invocation must see it in the prompt.
	// Plan-targeted entries must not leak into the coder prompt.
	coder := &fakeCoder{results: []state.AgentResult{{Output: "done"}}}
	judge := &fakeJudge{verdicts: []*state.Verdict{{Score: 100, Pass: true}}}

	task := codingTask()
	task.RewindContexts = []state.RewindContext{
		{
			Cycle:       1,
			Target:      state.PhaseCode,
			RootCause:   "previous code crashed on nil map",
			MustAddress: []string{"guard map writes"},
		},
		{
			Cycle:     1,
			Target:    state.PhasePlan,
			RootCause: "plan omitted the cache layer",
		},
	}

	phase := New(coder, judge, defaultCfg(), "/work")
	// Capture prompt via a wrapping coder.
	capture := &capturingCoder{inner: coder}
	phase.coder = capture

	_, err := phase.Run(context.Background(), task, "the plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(capture.prompts) != 1 {
		t.Fatalf("expected 1 coder call, got %d", len(capture.prompts))
	}
	prompt := capture.prompts[0]
	if !containsStr(prompt, "previous code crashed on nil map") {
		t.Error("expected code-targeted rewind context in coder prompt")
	}
	if !containsStr(prompt, "guard map writes") {
		t.Error("expected code-targeted MustAddress in coder prompt")
	}
	if containsStr(prompt, "plan omitted the cache layer") {
		t.Error("plan-targeted rewind context must not leak into coder prompt")
	}
}

// capturingCoder wraps another Coder and records every prompt passed
// to Run, so tests can assert on the exact prompt the phase built.
type capturingCoder struct {
	inner   Coder
	prompts []string
}

func (c *capturingCoder) Run(ctx context.Context, prompt string, workDir string) (state.AgentResult, error) {
	c.prompts = append(c.prompts, prompt)
	return c.inner.Run(ctx, prompt, workDir)
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
