package plan

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// multiResponseClient is a test double that returns different responses on
// successive calls. It wraps a slice of responses and an optional slice of
// errors, advancing the index on each call.
type multiResponseClient struct {
	responses []any
	errors    []error
	calls     []fakeCall
	idx       int
}

type fakeCall struct {
	system string
	prompt string
}

func (m *multiResponseClient) CompleteWithSystem(_ context.Context, system, prompt string, target any) error {
	m.calls = append(m.calls, fakeCall{system: system, prompt: prompt})
	i := m.idx
	m.idx++

	if i < len(m.errors) && m.errors[i] != nil {
		return m.errors[i]
	}

	if i >= len(m.responses) {
		return fmt.Errorf("multiResponseClient: no response for call %d", i)
	}

	data, err := json.Marshal(m.responses[i])
	if err != nil {
		return fmt.Errorf("multiResponseClient: marshalling: %w", err)
	}
	return json.Unmarshal(data, target)
}

// fakeJudge is a test double for the plan judge.
type fakeJudge struct {
	verdicts []*state.Verdict
	errors   []error
	calls    int
}

func (f *fakeJudge) Judge(_ context.Context, _, _ string) (*state.Verdict, error) {
	i := f.calls
	f.calls++

	if i < len(f.errors) && f.errors[i] != nil {
		return nil, f.errors[i]
	}
	if i >= len(f.verdicts) {
		return nil, fmt.Errorf("fakeJudge: no verdict for call %d", i)
	}
	return f.verdicts[i], nil
}

func testConfig() config.PlanPhaseConfig {
	return config.PlanPhaseConfig{
		CoverageThreshold: 80,
		MaxLoops:          3,
		Severity: config.SeverityConfig{
			BlockOn:  []string{"P0", "P1"},
			AssumeOn: []string{"P2"},
			DeferOn:  []string{"P3"},
		},
	}
}

func newPendingTask() *state.Task {
	return state.NewTask("test-1", "build a REST API with authentication")
}

func passingPlanResponse() plannerResponse {
	return plannerResponse{
		Requirements: "1. REST API with CRUD\n2. JWT authentication",
		Plan:         "1. Set up routes\n2. Add auth middleware\n3. Write tests",
	}
}

func passingVerdict() *state.Verdict {
	return &state.Verdict{
		Score: 90,
		Pass:  true,
		Gaps:  []state.Gap{},
	}
}

func failingVerdict() *state.Verdict {
	return &state.Verdict{
		Score: 50,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityP0, Description: "no error handling", Blocking: true},
			{Severity: state.SeverityP1, Description: "missing input validation", Blocking: true},
		},
		Questions: []state.Question{
			{Text: "What database will be used?", Priority: "high"},
		},
	}
}

func TestPlanPhase_PassOnFirstTry(t *testing.T) {
	planner := &multiResponseClient{
		responses: []any{passingPlanResponse()},
	}
	judge := &fakeJudge{
		verdicts: []*state.Verdict{passingVerdict()},
	}

	phase := New(planner, judge, testConfig())
	task := newPendingTask()

	result, err := phase.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Pass {
		t.Error("expected Pass=true")
	}
	if result.Escalate {
		t.Error("expected Escalate=false")
	}
	if result.Loops != 1 {
		t.Errorf("expected 1 loop, got %d", result.Loops)
	}
	if result.LastScore != 90 {
		t.Errorf("expected score 90, got %f", result.LastScore)
	}

	// Task should be in coding state.
	if task.State != state.StateCoding {
		t.Errorf("expected state coding, got %s", task.State)
	}
	if task.Phase != state.PhaseCode {
		t.Errorf("expected phase code, got %s", task.Phase)
	}

	// One attempt should be stored.
	if len(task.Attempts) != 1 {
		t.Fatalf("expected 1 attempt, got %d", len(task.Attempts))
	}
	if task.Attempts[0].Loop != 1 {
		t.Errorf("expected attempt loop 1, got %d", task.Attempts[0].Loop)
	}
	if !task.Attempts[0].Verdict.Pass {
		t.Error("expected attempt verdict to pass")
	}

	// Planner should have been called once.
	if len(planner.calls) != 1 {
		t.Errorf("expected 1 planner call, got %d", len(planner.calls))
	}
	// Judge should have been called once.
	if judge.calls != 1 {
		t.Errorf("expected 1 judge call, got %d", judge.calls)
	}
}

func TestPlanPhase_PassOnRetry(t *testing.T) {
	planner := &multiResponseClient{
		responses: []any{
			passingPlanResponse(),
			passingPlanResponse(),
		},
	}
	judge := &fakeJudge{
		verdicts: []*state.Verdict{
			failingVerdict(),
			passingVerdict(),
		},
	}

	phase := New(planner, judge, testConfig())
	task := newPendingTask()

	result, err := phase.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Pass {
		t.Error("expected Pass=true on retry")
	}
	if result.Escalate {
		t.Error("expected Escalate=false")
	}
	if result.Loops != 2 {
		t.Errorf("expected 2 loops, got %d", result.Loops)
	}
	if result.LastScore != 90 {
		t.Errorf("expected last score 90, got %f", result.LastScore)
	}

	// Task should be in coding state.
	if task.State != state.StateCoding {
		t.Errorf("expected state coding, got %s", task.State)
	}

	// Two attempts stored.
	if len(task.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(task.Attempts))
	}
	if task.Attempts[0].Verdict.Pass {
		t.Error("expected first attempt to fail")
	}
	if !task.Attempts[1].Verdict.Pass {
		t.Error("expected second attempt to pass")
	}

	// Loop counter should be 1 (one requeue).
	if task.LoopCount[state.PhasePlan] != 1 {
		t.Errorf("expected loop count 1, got %d", task.LoopCount[state.PhasePlan])
	}

	// Second planner call should include feedback.
	if len(planner.calls) != 2 {
		t.Fatalf("expected 2 planner calls, got %d", len(planner.calls))
	}
	if !strings.Contains(planner.calls[1].prompt, "Previous Attempt Feedback") {
		t.Error("expected second prompt to include feedback")
	}
	if !strings.Contains(planner.calls[1].prompt, "no error handling") {
		t.Error("expected feedback to include gap description")
	}
}

func TestPlanPhase_EscalationAtMaxLoops(t *testing.T) {
	// Config with max_loops=2 for faster test.
	cfg := testConfig()
	cfg.MaxLoops = 2

	planner := &multiResponseClient{
		responses: []any{
			passingPlanResponse(),
			passingPlanResponse(),
		},
	}
	judge := &fakeJudge{
		verdicts: []*state.Verdict{
			failingVerdict(),
			failingVerdict(),
		},
	}

	phase := New(planner, judge, cfg)
	task := newPendingTask()

	result, err := phase.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.Pass {
		t.Error("expected Pass=false on escalation")
	}
	if !result.Escalate {
		t.Error("expected Escalate=true")
	}
	if result.Loops != 2 {
		t.Errorf("expected 2 loops before escalation, got %d", result.Loops)
	}

	// Task should be in escalated state.
	if task.State != state.StateEscalated {
		t.Errorf("expected state escalated, got %s", task.State)
	}

	// Two attempts stored (both loops run, second requeue triggers escalation).
	if len(task.Attempts) != 2 {
		t.Fatalf("expected 2 attempts, got %d", len(task.Attempts))
	}
}

func TestPlanPhase_P2GapsLoggedAsAssumptions(t *testing.T) {
	planner := &multiResponseClient{
		responses: []any{passingPlanResponse()},
	}
	judge := &fakeJudge{
		verdicts: []*state.Verdict{
			{
				Score: 85,
				Pass:  true,
				Gaps: []state.Gap{
					{Severity: state.SeverityP2, Description: "database choice unclear", Blocking: false},
					{Severity: state.SeverityP2, Description: "caching strategy not defined", Blocking: false},
					{Severity: state.SeverityP3, Description: "nice to have: monitoring", Blocking: false},
				},
			},
		},
	}

	phase := New(planner, judge, testConfig())
	task := newPendingTask()

	result, err := phase.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Pass {
		t.Error("expected Pass=true")
	}

	// Two P2 gaps should be logged as assumptions.
	if len(task.Assumptions) != 2 {
		t.Fatalf("expected 2 assumptions, got %d", len(task.Assumptions))
	}

	if task.Assumptions[0].Description != "database choice unclear" {
		t.Errorf("unexpected assumption: %s", task.Assumptions[0].Description)
	}
	if task.Assumptions[0].Severity != state.SeverityP2 {
		t.Errorf("expected severity P2, got %s", task.Assumptions[0].Severity)
	}
	if task.Assumptions[0].Phase != state.PhasePlan {
		t.Errorf("expected phase plan, got %s", task.Assumptions[0].Phase)
	}

	if task.Assumptions[1].Description != "caching strategy not defined" {
		t.Errorf("unexpected assumption: %s", task.Assumptions[1].Description)
	}
}

func TestPlanPhase_AttemptsStored(t *testing.T) {
	cfg := testConfig()
	cfg.MaxLoops = 3

	planner := &multiResponseClient{
		responses: []any{
			passingPlanResponse(),
			passingPlanResponse(),
			passingPlanResponse(),
		},
	}
	judge := &fakeJudge{
		verdicts: []*state.Verdict{
			{Score: 40, Pass: false, Gaps: []state.Gap{{Severity: state.SeverityP0, Description: "gap1", Blocking: true}}},
			{Score: 60, Pass: false, Gaps: []state.Gap{{Severity: state.SeverityP1, Description: "gap2", Blocking: true}}},
			{Score: 90, Pass: true, Gaps: []state.Gap{}},
		},
	}

	phase := New(planner, judge, cfg)
	task := newPendingTask()

	result, err := phase.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !result.Pass {
		t.Error("expected Pass=true on third try")
	}
	if result.Loops != 3 {
		t.Errorf("expected 3 loops, got %d", result.Loops)
	}

	// Three attempts should be stored with correct loop numbers.
	if len(task.Attempts) != 3 {
		t.Fatalf("expected 3 attempts, got %d", len(task.Attempts))
	}
	for i, attempt := range task.Attempts {
		if attempt.Loop != i+1 {
			t.Errorf("attempt %d: expected loop %d, got %d", i, i+1, attempt.Loop)
		}
		if attempt.Phase != state.PhasePlan {
			t.Errorf("attempt %d: expected phase plan, got %s", i, attempt.Phase)
		}
	}

	// Scores should be increasing.
	if task.Attempts[0].Verdict.Score != 40 {
		t.Errorf("expected first score 40, got %f", task.Attempts[0].Verdict.Score)
	}
	if task.Attempts[1].Verdict.Score != 60 {
		t.Errorf("expected second score 60, got %f", task.Attempts[1].Verdict.Score)
	}
	if task.Attempts[2].Verdict.Score != 90 {
		t.Errorf("expected third score 90, got %f", task.Attempts[2].Verdict.Score)
	}
}

func TestPlanPhase_PlannerError(t *testing.T) {
	planner := &multiResponseClient{
		responses: []any{nil},
		errors:    []error{fmt.Errorf("connection refused")},
	}
	judge := &fakeJudge{}

	phase := New(planner, judge, testConfig())
	task := newPendingTask()

	_, err := phase.Run(context.Background(), task)
	if err == nil {
		t.Fatal("expected error when planner fails")
	}
	if !strings.Contains(err.Error(), "calling planner agent") {
		t.Errorf("expected planner error context, got: %s", err.Error())
	}
}

func TestPlanPhase_JudgeError(t *testing.T) {
	planner := &multiResponseClient{
		responses: []any{passingPlanResponse()},
	}
	judge := &fakeJudge{
		errors: []error{fmt.Errorf("judge failed")},
	}

	phase := New(planner, judge, testConfig())
	task := newPendingTask()

	_, err := phase.Run(context.Background(), task)
	if err == nil {
		t.Fatal("expected error when judge fails")
	}
	if !strings.Contains(err.Error(), "running plan judge") {
		t.Errorf("expected judge error context, got: %s", err.Error())
	}
}

func TestPlanPhase_AssumptionsIncludedInRetryPrompt(t *testing.T) {
	planner := &multiResponseClient{
		responses: []any{
			passingPlanResponse(),
			passingPlanResponse(),
		},
	}
	judge := &fakeJudge{
		verdicts: []*state.Verdict{
			{
				Score: 60,
				Pass:  false,
				Gaps: []state.Gap{
					{Severity: state.SeverityP2, Description: "unclear caching approach", Blocking: false},
					{Severity: state.SeverityP0, Description: "missing auth", Blocking: true},
				},
			},
			passingVerdict(),
		},
	}

	phase := New(planner, judge, testConfig())
	task := newPendingTask()

	_, err := phase.Run(context.Background(), task)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Second prompt should include the assumption from the P2 gap.
	if len(planner.calls) < 2 {
		t.Fatal("expected at least 2 planner calls")
	}
	secondPrompt := planner.calls[1].prompt
	if !strings.Contains(secondPrompt, "Assumptions from Previous Loops") {
		t.Error("expected second prompt to include assumptions section")
	}
	if !strings.Contains(secondPrompt, "unclear caching approach") {
		t.Error("expected second prompt to include P2 assumption")
	}
}

func TestPlanPhase_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // Cancel immediately.

	planner := &multiResponseClient{
		responses: []any{nil},
		errors:    []error{ctx.Err()},
	}
	judge := &fakeJudge{}

	phase := New(planner, judge, testConfig())
	task := newPendingTask()

	_, err := phase.Run(ctx, task)
	if err == nil {
		t.Fatal("expected error on cancelled context")
	}
}

func TestBuildPlannerPrompt_NoFeedback(t *testing.T) {
	prompt := buildPlannerPrompt("build an API", "", nil)

	if !strings.Contains(prompt, "## Task Intent") {
		t.Error("expected intent header")
	}
	if !strings.Contains(prompt, "build an API") {
		t.Error("expected intent in prompt")
	}
	if strings.Contains(prompt, "Previous Attempt Feedback") {
		t.Error("did not expect feedback section")
	}
	if strings.Contains(prompt, "Assumptions") {
		t.Error("did not expect assumptions section")
	}
}

func TestBuildPlannerPrompt_WithFeedback(t *testing.T) {
	prompt := buildPlannerPrompt("build an API", "missing auth", nil)

	if !strings.Contains(prompt, "Previous Attempt Feedback") {
		t.Error("expected feedback section")
	}
	if !strings.Contains(prompt, "missing auth") {
		t.Error("expected feedback content")
	}
}

func TestBuildPlannerPrompt_WithAssumptions(t *testing.T) {
	assumptions := []state.Assumption{
		{Description: "using PostgreSQL", Severity: state.SeverityP2, Phase: state.PhasePlan},
	}
	prompt := buildPlannerPrompt("build an API", "some feedback", assumptions)

	if !strings.Contains(prompt, "Assumptions from Previous Loops") {
		t.Error("expected assumptions section")
	}
	if !strings.Contains(prompt, "using PostgreSQL") {
		t.Error("expected assumption in prompt")
	}
}

func TestBuildFeedbackSummary(t *testing.T) {
	verdict := &state.Verdict{
		Score: 55.5,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityP0, Description: "critical issue", Blocking: true},
			{Severity: state.SeverityP2, Description: "minor thing", Blocking: false},
		},
		Questions: []state.Question{
			{Text: "What DB?", Priority: "high"},
		},
	}

	summary := buildFeedbackSummary(verdict)

	if !strings.Contains(summary, "Score: 55.5") {
		t.Errorf("expected score in summary, got: %s", summary)
	}
	if !strings.Contains(summary, "[P0] [BLOCKING] critical issue") {
		t.Errorf("expected P0 blocking gap, got: %s", summary)
	}
	if !strings.Contains(summary, "[P2] minor thing") {
		t.Errorf("expected P2 gap, got: %s", summary)
	}
	if !strings.Contains(summary, "[high] What DB?") {
		t.Errorf("expected question, got: %s", summary)
	}
}
