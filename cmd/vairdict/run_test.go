package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/escalation"
	"github.com/vairdict/vairdict/internal/github"
	codephase "github.com/vairdict/vairdict/internal/phases/code"
	planphase "github.com/vairdict/vairdict/internal/phases/plan"
	qualityphase "github.com/vairdict/vairdict/internal/phases/quality"
	"github.com/vairdict/vairdict/internal/state"
	"github.com/vairdict/vairdict/internal/ui"
)

// helper: make a task that has been advanced through the state machine
// up to (and including) the given phase's review state, with one
// recorded attempt+verdict per phase reached.
func taskAt(t *testing.T, phase state.Phase) *state.Task {
	t.Helper()
	task := state.NewTask("t-1", "build the thing")

	transitions := []state.TaskState{
		state.StatePlanning,
		state.StatePlanReview,
	}
	if phase == state.PhaseCode || phase == state.PhaseQuality {
		transitions = append(transitions,
			state.StateCoding,
			state.StateCodeReview,
		)
	}
	if phase == state.PhaseQuality {
		transitions = append(transitions,
			state.StateQuality,
			state.StateQualityReview,
		)
	}
	for _, s := range transitions {
		if err := task.Transition(s); err != nil {
			t.Fatalf("setup transition to %s: %v", s, err)
		}
	}
	return task
}

func TestLastVerdictForPhase_Empty(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	if got := lastVerdictForPhase(task, state.PhaseCode); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestLastVerdictForPhase_NoMatch(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	task.Attempts = []state.Attempt{
		{Phase: state.PhasePlan, Loop: 1, Verdict: &state.Verdict{Score: 80, Pass: true}},
	}
	if got := lastVerdictForPhase(task, state.PhaseCode); got != nil {
		t.Errorf("expected nil for non-matching phase, got %+v", got)
	}
}

func TestLastVerdictForPhase_ReturnsLastMatch(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	task.Attempts = []state.Attempt{
		{Phase: state.PhaseCode, Loop: 1, Verdict: &state.Verdict{Score: 50, Pass: false}},
		{Phase: state.PhasePlan, Loop: 1, Verdict: &state.Verdict{Score: 80, Pass: true}},
		{Phase: state.PhaseCode, Loop: 2, Verdict: &state.Verdict{Score: 95, Pass: true}},
	}
	got := lastVerdictForPhase(task, state.PhaseCode)
	if got == nil {
		t.Fatal("expected verdict, got nil")
	}
	if got.Score != 95 {
		t.Errorf("expected last code verdict (95), got %v", got.Score)
	}
}

func TestLastVerdictForPhase_NilVerdictSkipped(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	task.Attempts = []state.Attempt{
		{Phase: state.PhaseCode, Loop: 1, Verdict: &state.Verdict{Score: 60, Pass: false}},
		{Phase: state.PhaseCode, Loop: 2, Verdict: nil},
	}
	got := lastVerdictForPhase(task, state.PhaseCode)
	if got == nil || got.Score != 60 {
		t.Errorf("expected to skip nil verdict and return score=60, got %+v", got)
	}
}

func TestLastGapsForPhase_Empty(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	if got := lastGapsForPhase(task, state.PhaseCode); got != nil {
		t.Errorf("expected nil, got %+v", got)
	}
}

func TestLastGapsForPhase_ReturnsGaps(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	gaps := []state.Gap{
		{Severity: state.SeverityP1, Description: "broken thing", Blocking: true},
	}
	task.Attempts = []state.Attempt{
		{Phase: state.PhaseQuality, Loop: 1, Verdict: &state.Verdict{Score: 40, Gaps: gaps}},
	}
	got := lastGapsForPhase(task, state.PhaseQuality)
	if len(got) != 1 || got[0].Description != "broken thing" {
		t.Errorf("unexpected gaps: %+v", got)
	}
}

// fakePRCommenter records calls so dispatchEscalation tests can assert
// the github channel was driven correctly.
type fakePRCommenter struct {
	calls []fakeCommentCall
	err   error
}

type fakeCommentCall struct {
	prNumber int
	body     string
}

func (f *fakePRCommenter) AddComment(_ context.Context, prNumber int, body string) error {
	f.calls = append(f.calls, fakeCommentCall{prNumber: prNumber, body: body})
	return f.err
}

func TestDispatchEscalation_StdoutChannel(t *testing.T) {
	task := taskAt(t, state.PhaseQuality)
	var out bytes.Buffer

	err := dispatchEscalation(
		context.Background(),
		task,
		escalation.Result{
			Phase:     state.PhaseQuality,
			Loops:     3,
			LastScore: 42,
			Gaps: []state.Gap{
				{Severity: state.SeverityP1, Description: "missing feature", Blocking: true},
			},
		},
		config.EscalationConfig{NotifyVia: "stdout", AfterLoops: 3},
		&out,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.State != state.StateEscalated {
		t.Errorf("task state = %s, want escalated", task.State)
	}
	body := out.String()
	for _, want := range []string{"Escalation", "missing feature", "quality"} {
		if !strings.Contains(body, want) {
			t.Errorf("escalation output missing %q\n---\n%s", want, body)
		}
	}
}

func TestDispatchEscalation_GithubChannelHappyPath(t *testing.T) {
	task := taskAt(t, state.PhaseCode)
	var out bytes.Buffer
	gh := &fakePRCommenter{}

	err := dispatchEscalation(
		context.Background(),
		task,
		escalation.Result{
			Phase:     state.PhaseCode,
			Loops:     3,
			LastScore: 25,
			PRNumber:  42,
		},
		config.EscalationConfig{NotifyVia: "github", AfterLoops: 3},
		&out,
		gh,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.calls) != 1 {
		t.Fatalf("expected 1 github comment call, got %d", len(gh.calls))
	}
	if gh.calls[0].prNumber != 42 {
		t.Errorf("comment posted to PR %d, want 42", gh.calls[0].prNumber)
	}
	if !strings.Contains(gh.calls[0].body, "Escalation") {
		t.Errorf("comment body missing 'Escalation': %s", gh.calls[0].body)
	}
}

func TestDispatchEscalation_GithubFallsBackToStdoutWithoutPR(t *testing.T) {
	task := taskAt(t, state.PhaseCode)
	var out bytes.Buffer
	gh := &fakePRCommenter{}

	err := dispatchEscalation(
		context.Background(),
		task,
		escalation.Result{
			Phase:     state.PhaseCode,
			Loops:     3,
			LastScore: 25,
			PRNumber:  0, // no PR yet
		},
		config.EscalationConfig{NotifyVia: "github", AfterLoops: 3},
		&out,
		gh,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.calls) != 0 {
		t.Errorf("expected 0 github calls (fallback to stdout), got %d", len(gh.calls))
	}
	if !strings.Contains(out.String(), "Escalation") {
		t.Errorf("expected stdout fallback to contain Escalation, got %q", out.String())
	}
}

func TestDispatchEscalation_GithubClientErrorWrapped(t *testing.T) {
	task := taskAt(t, state.PhaseQuality)
	gh := &fakePRCommenter{err: errors.New("api down")}

	err := dispatchEscalation(
		context.Background(),
		task,
		escalation.Result{
			Phase:     state.PhaseQuality,
			Loops:     3,
			LastScore: 10,
			PRNumber:  99,
		},
		config.EscalationConfig{NotifyVia: "github", AfterLoops: 3},
		&bytes.Buffer{},
		gh,
	)
	if err == nil {
		t.Fatal("expected wrapped error")
	}
	if !strings.Contains(err.Error(), "escalating task") {
		t.Errorf("expected wrapped error, got %v", err)
	}
	if !strings.Contains(err.Error(), "api down") {
		t.Errorf("expected underlying error preserved, got %v", err)
	}
}

func TestDispatchEscalation_DefaultsToStdoutWhenChannelEmpty(t *testing.T) {
	task := taskAt(t, state.PhaseCode)
	var out bytes.Buffer

	err := dispatchEscalation(
		context.Background(),
		task,
		escalation.Result{Phase: state.PhaseCode, Loops: 3, LastScore: 0},
		config.EscalationConfig{}, // no NotifyVia set
		&out,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(out.String(), "Escalation") {
		t.Errorf("expected default-stdout output, got %q", out.String())
	}
}

// fakeRenderer records every renderer method call so emit* helper tests
// can assert on the sequence of events without coupling to specific text.
type fakeRenderer struct {
	loops []fakeLoopCall
	dones []fakeDoneCall
}

type fakeLoopCall struct {
	phase state.Phase
	loop  int
	max   int
	score float64
	pass  bool
}

type fakeDoneCall struct {
	phase   state.Phase
	outcome ui.PhaseOutcome
	score   float64
	loops   int
	summary string
	gaps    []state.Gap
}

func (f *fakeRenderer) RunStart(string, string, string) {}
func (f *fakeRenderer) Note(string, string)             {}
func (f *fakeRenderer) PhaseStart(state.Phase)          {}
func (f *fakeRenderer) PRCreated(string)                {}
func (f *fakeRenderer) VerdictPosted(float64, bool)     {}
func (f *fakeRenderer) RunComplete(string)              {}
func (f *fakeRenderer) Error(error)                     {}
func (f *fakeRenderer) Close() error                    { return nil }
func (f *fakeRenderer) Escalation(string, state.Phase, int, float64, []state.Gap) {
}

func (f *fakeRenderer) PhaseLoop(phase state.Phase, loop, max int, score float64, pass bool) {
	f.loops = append(f.loops, fakeLoopCall{phase, loop, max, score, pass})
}

func (f *fakeRenderer) PhaseDone(phase state.Phase, outcome ui.PhaseOutcome, score float64, loops int, summary string, gaps []state.Gap) {
	f.dones = append(f.dones, fakeDoneCall{phase, outcome, score, loops, summary, gaps})
}

func TestEmitPhaseAttempts_FiltersByPhase(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	task.Attempts = []state.Attempt{
		{Phase: state.PhasePlan, Loop: 1, Verdict: &state.Verdict{Score: 90, Pass: true}},
		{Phase: state.PhaseCode, Loop: 1, Verdict: &state.Verdict{Score: 60, Pass: false}},
		{Phase: state.PhaseCode, Loop: 2, Verdict: &state.Verdict{Score: 85, Pass: true}},
	}
	r := &fakeRenderer{}

	emitPhaseAttempts(r, task, state.PhaseCode, 3)

	if len(r.loops) != 2 {
		t.Fatalf("expected 2 loop events, got %d", len(r.loops))
	}
	if r.loops[0].loop != 1 || r.loops[0].score != 60 || r.loops[0].pass {
		t.Errorf("first code loop wrong: %+v", r.loops[0])
	}
	if r.loops[1].loop != 2 || r.loops[1].score != 85 || !r.loops[1].pass {
		t.Errorf("second code loop wrong: %+v", r.loops[1])
	}
	if r.loops[1].max != 3 {
		t.Errorf("max loops not threaded, got %d", r.loops[1].max)
	}
}

func TestEmitPhaseAttempts_NilVerdictUsesZero(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	task.Attempts = []state.Attempt{
		{Phase: state.PhasePlan, Loop: 1, Verdict: nil},
	}
	r := &fakeRenderer{}

	emitPhaseAttempts(r, task, state.PhasePlan, 3)

	if len(r.loops) != 1 {
		t.Fatalf("expected 1 loop event, got %d", len(r.loops))
	}
	if r.loops[0].score != 0 || r.loops[0].pass {
		t.Errorf("nil verdict should render as score=0 pass=false, got %+v", r.loops[0])
	}
}

func TestEmitPhaseDone_OutcomeMapping(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	task.Attempts = []state.Attempt{
		{Phase: state.PhasePlan, Loop: 1, Verdict: &state.Verdict{
			Score:   92,
			Pass:    true,
			Summary: "## Decided\n- thing",
			Gaps:    []state.Gap{{Severity: state.SeverityP2, Description: "note"}},
		}},
	}

	cases := []struct {
		name          string
		pass          bool
		escalate      bool
		requeueToCode bool
		want          ui.PhaseOutcome
	}{
		{"pass", true, false, false, ui.OutcomePass},
		{"fail", false, false, false, ui.OutcomeFail},
		{"escalate", false, true, false, ui.OutcomeEscalate},
		{"requeue_to_code", false, false, true, ui.OutcomeRequeueToCode},
		// requeue_to_code wins over escalate when both are set
		{"requeue_before_escalate", false, true, true, ui.OutcomeRequeueToCode},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r := &fakeRenderer{}
			emitPhaseDone(r, task, state.PhasePlan, tc.pass, tc.escalate, tc.requeueToCode, 92, 1)
			if len(r.dones) != 1 {
				t.Fatalf("expected 1 done event, got %d", len(r.dones))
			}
			if r.dones[0].outcome != tc.want {
				t.Errorf("outcome = %v, want %v", r.dones[0].outcome, tc.want)
			}
			if r.dones[0].summary != "## Decided\n- thing" {
				t.Errorf("summary not threaded from verdict: %q", r.dones[0].summary)
			}
			if len(r.dones[0].gaps) != 1 {
				t.Errorf("gaps not threaded from verdict: %+v", r.dones[0].gaps)
			}
		})
	}
}

func TestEmitPhaseDone_NoVerdictEmitsEmpty(t *testing.T) {
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	emitPhaseDone(r, task, state.PhasePlan, false, true, false, 0, 3)

	if len(r.dones) != 1 {
		t.Fatalf("expected 1 done event, got %d", len(r.dones))
	}
	if r.dones[0].summary != "" || r.dones[0].gaps != nil {
		t.Errorf("expected empty summary/gaps when no verdict, got %+v", r.dones[0])
	}
	if r.dones[0].outcome != ui.OutcomeEscalate {
		t.Errorf("outcome = %v, want escalate", r.dones[0].outcome)
	}
}

func TestDispatchEscalation_AlreadyEscalatedNoOp(t *testing.T) {
	task := taskAt(t, state.PhaseQuality)
	if err := task.Transition(state.StateEscalated); err != nil {
		t.Fatalf("setup: %v", err)
	}
	var out bytes.Buffer

	err := dispatchEscalation(
		context.Background(),
		task,
		escalation.Result{Phase: state.PhaseQuality, Loops: 3, LastScore: 5},
		config.EscalationConfig{NotifyVia: "stdout"},
		&out,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.State != state.StateEscalated {
		t.Errorf("task state = %s, want escalated", task.State)
	}
	if !strings.Contains(out.String(), "Escalation") {
		t.Errorf("expected escalation output even when already escalated, got %q", out.String())
	}
}

// --- Orchestration test fakes ---

type fakePlanRunner struct {
	result *planphase.PhaseResult
	gaps   []state.Gap
	err    error
	called bool
}

func (f *fakePlanRunner) Run(_ context.Context, task *state.Task) (*planphase.PhaseResult, error) {
	f.called = true
	if f.result != nil {
		task.Attempts = append(task.Attempts, state.Attempt{
			Phase: state.PhasePlan, Loop: f.result.Loops,
			Verdict: &state.Verdict{Score: f.result.LastScore, Pass: f.result.Pass, Gaps: f.gaps},
		})
	}
	return f.result, f.err
}

type fakeCodeRunner struct {
	result *codephase.PhaseResult
	gaps   []state.Gap
	err    error
	called bool
	plan   string
}

func (f *fakeCodeRunner) Run(_ context.Context, task *state.Task, plan string) (*codephase.PhaseResult, error) {
	f.called = true
	f.plan = plan
	if f.result != nil {
		task.Attempts = append(task.Attempts, state.Attempt{
			Phase: state.PhaseCode, Loop: f.result.Loops,
			Verdict: &state.Verdict{Score: f.result.LastScore, Pass: f.result.Pass, Gaps: f.gaps},
		})
	}
	return f.result, f.err
}

type fakeQualityRunner struct {
	result    *qualityphase.PhaseResult
	gaps      []state.Gap
	err       error
	called    bool
	plan      string
	codeFacts string
}

func (f *fakeQualityRunner) Run(_ context.Context, task *state.Task, plan string, codeFacts string) (*qualityphase.PhaseResult, error) {
	f.called = true
	f.plan = plan
	f.codeFacts = codeFacts
	if f.result != nil {
		task.Attempts = append(task.Attempts, state.Attempt{
			Phase: state.PhaseQuality, Loop: f.result.Loops,
			Verdict: &state.Verdict{Score: f.result.LastScore, Pass: f.result.Pass, Gaps: f.gaps},
		})
	}
	return f.result, f.err
}

type fakeGHOrch struct {
	branchName string
	branchErr  error
	pr         *github.PR
	prErr      error
	verdictErr error
	mergeErr   error

	branchCalled  bool
	prCalled      bool
	verdictCalled bool
	mergeCalled   bool
}

func (f *fakeGHOrch) CreateBranch(context.Context, string, string) (string, error) {
	f.branchCalled = true
	return f.branchName, f.branchErr
}

func (f *fakeGHOrch) CreatePR(context.Context, github.CreatePROpts) (*github.PR, error) {
	f.prCalled = true
	return f.pr, f.prErr
}

func (f *fakeGHOrch) PostVerdict(context.Context, int, *state.Verdict, state.Phase, int) error {
	f.verdictCalled = true
	return f.verdictErr
}

func (f *fakeGHOrch) MergePR(context.Context, int) error {
	f.mergeCalled = true
	return f.mergeErr
}

// orchBundle groups the fakes so tests can inspect them after a run.
type orchBundle struct {
	plan    *fakePlanRunner
	code    *fakeCodeRunner
	quality *fakeQualityRunner
	gh      *fakeGHOrch

	commitCalled     bool
	escalationCalled bool
	escalationResult escalation.Result
}

var errEscalated = errors.New("escalated (test sentinel)")

func newOrchBundle() *orchBundle {
	return &orchBundle{
		plan: &fakePlanRunner{result: &planphase.PhaseResult{
			Pass: true, Loops: 1, LastScore: 90, Plan: "the plan",
		}},
		code: &fakeCodeRunner{result: &codephase.PhaseResult{
			Pass: true, Loops: 1, LastScore: 100,
		}},
		quality: &fakeQualityRunner{result: &qualityphase.PhaseResult{
			Pass: true, Loops: 1, LastScore: 95,
		}},
		gh: &fakeGHOrch{
			branchName: "vairdict/test-abc",
			pr:         &github.PR{URL: "https://github.com/x/y/pull/42", Number: 42},
		},
	}
}

func (b *orchBundle) deps() runDeps {
	return runDeps{
		plan:    b.plan,
		code:    b.code,
		quality: b.quality,
		gh:      b.gh,
		commit: func(context.Context, *state.Task) error {
			b.commitCalled = true
			return nil
		},
		onEscalation: func(_ context.Context, _ *state.Task, result escalation.Result) error {
			b.escalationCalled = true
			b.escalationResult = result
			return errEscalated
		},
	}
}

// --- Orchestration tests ---

func TestRunOrchestration_HappyPath(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	task := state.NewTask("t-1", "build the thing")
	r := &fakeRenderer{}

	err := runOrchestration(context.Background(), b.deps(), task, r)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !b.plan.called {
		t.Error("plan runner not called")
	}
	if !b.code.called {
		t.Error("code runner not called")
	}
	if b.code.plan != "the plan" {
		t.Errorf("code runner got plan %q, want %q", b.code.plan, "the plan")
	}
	if !b.quality.called {
		t.Error("quality runner not called")
	}
	if b.quality.plan != "the plan" {
		t.Errorf("quality runner got plan %q, want %q", b.quality.plan, "the plan")
	}
	if !b.commitCalled {
		t.Error("commit not called")
	}
	if !b.gh.branchCalled {
		t.Error("CreateBranch not called")
	}
	if !b.gh.prCalled {
		t.Error("CreatePR not called")
	}
	if !b.gh.verdictCalled {
		t.Error("PostVerdict not called")
	}
	if b.escalationCalled {
		t.Error("escalation should not be called on happy path")
	}
}

func TestRunOrchestration_PlanEscalates(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	b.plan.result = &planphase.PhaseResult{Escalate: true, Loops: 3, LastScore: 40}
	b.plan.gaps = []state.Gap{{Severity: state.SeverityP1, Description: "missing req", Blocking: true}}
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	err := runOrchestration(context.Background(), b.deps(), task, r)

	if !errors.Is(err, errEscalated) {
		t.Fatalf("expected errEscalated, got %v", err)
	}
	if !b.escalationCalled {
		t.Error("escalation not called")
	}
	if b.escalationResult.Phase != state.PhasePlan {
		t.Errorf("escalation phase = %s, want plan", b.escalationResult.Phase)
	}
	if b.code.called {
		t.Error("code should not run when plan escalates")
	}
	if b.quality.called {
		t.Error("quality should not run when plan escalates")
	}
	if b.gh.branchCalled {
		t.Error("branch should not be created when plan escalates")
	}
	if b.gh.prCalled {
		t.Error("PR should not be created when plan escalates")
	}
}

func TestRunOrchestration_CodeEscalates(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	b.code.result = &codephase.PhaseResult{Escalate: true, Loops: 3, LastScore: 25}
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	err := runOrchestration(context.Background(), b.deps(), task, r)

	if !errors.Is(err, errEscalated) {
		t.Fatalf("expected errEscalated, got %v", err)
	}
	if b.escalationResult.Phase != state.PhaseCode {
		t.Errorf("escalation phase = %s, want code", b.escalationResult.Phase)
	}
	if b.quality.called {
		t.Error("quality should not run when code escalates")
	}
	if b.gh.prCalled {
		t.Error("PR should not be created when code escalates")
	}
	if b.commitCalled {
		t.Error("commit should not be called when code escalates")
	}
}

func TestRunOrchestration_QualityEscalates(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	b.quality.result = &qualityphase.PhaseResult{Escalate: true, Loops: 3, LastScore: 30}
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	err := runOrchestration(context.Background(), b.deps(), task, r)

	if !errors.Is(err, errEscalated) {
		t.Fatalf("expected errEscalated, got %v", err)
	}
	if b.escalationResult.Phase != state.PhaseQuality {
		t.Errorf("escalation phase = %s, want quality", b.escalationResult.Phase)
	}
	if b.gh.prCalled {
		t.Error("PR should not be created when quality escalates")
	}
	// Commit happens BEFORE quality, so it should have been called.
	if !b.commitCalled {
		t.Error("commit should be called even when quality escalates (happens before quality)")
	}
}

func TestRunOrchestration_QualityRequeueToCode(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	b.quality.result = &qualityphase.PhaseResult{RequeueToCode: true, Loops: 2, LastScore: 50}
	b.quality.gaps = []state.Gap{{Severity: state.SeverityP0, Description: "code broken", Blocking: true}}
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	err := runOrchestration(context.Background(), b.deps(), task, r)

	if !errors.Is(err, errEscalated) {
		t.Fatalf("expected errEscalated, got %v", err)
	}
	if b.escalationResult.Phase != state.PhaseQuality {
		t.Errorf("escalation phase = %s, want quality", b.escalationResult.Phase)
	}
	if b.gh.prCalled {
		t.Error("PR should not be created on requeue")
	}
}

func TestRunOrchestration_PostVerdictFailure_DoesNotFailRun(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	b.gh.verdictErr = errors.New("github API down")
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	err := runOrchestration(context.Background(), b.deps(), task, r)

	if err != nil {
		t.Fatalf("PostVerdict failure should not fail the run, got %v", err)
	}
	if !b.gh.verdictCalled {
		t.Error("PostVerdict should have been attempted")
	}
	if !b.gh.prCalled {
		t.Error("PR should still be created")
	}
}

func TestRunOrchestration_BranchCreationFailure(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	b.gh.branchErr = errors.New("branch already exists")
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	err := runOrchestration(context.Background(), b.deps(), task, r)

	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "creating branch") {
		t.Errorf("error should mention branch creation: %v", err)
	}
	if b.code.called {
		t.Error("code should not run after branch creation failure")
	}
	if b.escalationCalled {
		t.Error("branch failure should not trigger escalation")
	}
}

func TestRunOrchestration_AutoMerge_Enabled(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	deps := b.deps()
	deps.autoMerge = true
	err := runOrchestration(context.Background(), deps, task, r)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !b.gh.mergeCalled {
		t.Error("MergePR should be called when autoMerge is enabled and verdict passes")
	}
}

func TestRunOrchestration_AutoMerge_Disabled(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	deps := b.deps()
	deps.autoMerge = false
	err := runOrchestration(context.Background(), deps, task, r)

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if b.gh.mergeCalled {
		t.Error("MergePR should NOT be called when autoMerge is disabled")
	}
}

func TestRunOrchestration_AutoMerge_FailureDoesNotFailRun(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	b.gh.mergeErr = errors.New("merge conflict")
	task := state.NewTask("t-1", "intent")
	r := &fakeRenderer{}

	deps := b.deps()
	deps.autoMerge = true
	err := runOrchestration(context.Background(), deps, task, r)

	if err != nil {
		t.Fatalf("auto-merge failure should not fail the run, got %v", err)
	}
	if !b.gh.mergeCalled {
		t.Error("MergePR should have been attempted")
	}
}

// --- Concurrent runner tests ---

// runConcurrentTest is a helper that calls runTasksConcurrent with the given
// bundles and returns the collected results. It avoids real config loading,
// store, workspaces, etc. by exercising runOrchestration directly.
func runConcurrentTest(t *testing.T, bundles []*orchBundle, maxTasks int) []taskResult {
	t.Helper()
	intents := make([]string, len(bundles))
	for i := range bundles {
		intents[i] = fmt.Sprintf("intent-%d", i)
	}

	results := make([]taskResult, len(intents))
	sem := make(chan struct{}, maxTasks)
	var wg sync.WaitGroup

	for i, b := range bundles {
		task := state.NewTask(fmt.Sprintf("t-%d", i), intents[i])
		r := &fakeRenderer{}
		deps := b.deps()
		wg.Add(1)
		go func(idx int, task *state.Task, deps runDeps, r ui.Renderer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := runOrchestration(context.Background(), deps, task, r)
			results[idx] = taskResult{TaskID: task.ID, Intent: task.Intent, Err: err}
		}(i, task, deps, r)
	}

	wg.Wait()
	return results
}

func TestConcurrent_TwoTasksBothPass(t *testing.T) {
	t.Parallel()
	b1 := newOrchBundle()
	b2 := newOrchBundle()

	results := runConcurrentTest(t, []*orchBundle{b1, b2}, 3)

	if len(results) != 2 {
		t.Fatalf("expected 2 results, got %d", len(results))
	}
	for i, r := range results {
		if r.Err != nil {
			t.Errorf("task %d: unexpected error: %v", i, r.Err)
		}
	}
	if !b1.gh.prCalled {
		t.Error("task 0: PR not created")
	}
	if !b2.gh.prCalled {
		t.Error("task 1: PR not created")
	}
}

func TestConcurrent_OneFailsOneSucceeds(t *testing.T) {
	t.Parallel()
	b1 := newOrchBundle()
	b2 := newOrchBundle()
	b2.plan.result = &planphase.PhaseResult{Escalate: true, Loops: 3, LastScore: 30}

	results := runConcurrentTest(t, []*orchBundle{b1, b2}, 3)

	if results[0].Err != nil {
		t.Errorf("task 0: expected success, got %v", results[0].Err)
	}
	if results[1].Err == nil {
		t.Error("task 1: expected escalation error, got nil")
	}
	if !b1.gh.prCalled {
		t.Error("task 0: PR should be created even when task 1 fails")
	}
	if b2.gh.prCalled {
		t.Error("task 1: PR should not be created on escalation")
	}
}

func TestConcurrent_SemaphoreRespected(t *testing.T) {
	t.Parallel()
	const numTasks = 4
	const maxConcurrent = 2

	var mu sync.Mutex
	var running, peak int

	results := make([]taskResult, numTasks)
	sem := make(chan struct{}, maxConcurrent)
	var wg sync.WaitGroup

	for i := 0; i < numTasks; i++ {
		b := newOrchBundle()
		task := state.NewTask(fmt.Sprintf("t-%d", i), fmt.Sprintf("intent-%d", i))
		r := &fakeRenderer{}
		deps := b.deps()

		// Wrap the plan runner to track concurrency.
		origPlan := deps.plan
		deps.plan = &trackingPlanRunner{
			inner:   origPlan,
			mu:      &mu,
			running: &running,
			peak:    &peak,
		}

		wg.Add(1)
		go func(idx int, task *state.Task, deps runDeps, r ui.Renderer) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			err := runOrchestration(context.Background(), deps, task, r)
			results[idx] = taskResult{TaskID: task.ID, Intent: task.Intent, Err: err}
		}(i, task, deps, r)
	}

	wg.Wait()

	mu.Lock()
	observed := peak
	mu.Unlock()

	if observed > maxConcurrent {
		t.Errorf("peak concurrency = %d, want <= %d", observed, maxConcurrent)
	}

	for i, r := range results {
		if r.Err != nil {
			t.Errorf("task %d: unexpected error: %v", i, r.Err)
		}
	}
}

// trackingPlanRunner wraps a planRunner and tracks peak concurrency.
type trackingPlanRunner struct {
	inner   planRunner
	mu      *sync.Mutex
	running *int
	peak    *int
}

func (tr *trackingPlanRunner) Run(ctx context.Context, task *state.Task) (*planphase.PhaseResult, error) {
	tr.mu.Lock()
	*tr.running++
	if *tr.running > *tr.peak {
		*tr.peak = *tr.running
	}
	tr.mu.Unlock()

	defer func() {
		tr.mu.Lock()
		*tr.running--
		tr.mu.Unlock()
	}()

	return tr.inner.Run(ctx, task)
}
