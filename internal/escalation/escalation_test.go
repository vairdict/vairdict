package escalation

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// fakePRCommenter records calls and returns a configurable error.
type fakePRCommenter struct {
	calls []fakeCall
	err   error
}

type fakeCall struct {
	prNumber int
	body     string
}

func (f *fakePRCommenter) AddComment(_ context.Context, prNumber int, body string) error {
	f.calls = append(f.calls, fakeCall{prNumber: prNumber, body: body})
	return f.err
}

// taskInReviewState builds a task that has reached plan_review (a valid
// origin for transition to escalated).
func taskInReviewState(t *testing.T) *state.Task {
	t.Helper()
	task := state.NewTask("abc12345", "build a thing")
	if err := task.Transition(state.StatePlanning); err != nil {
		t.Fatalf("transition to planning: %v", err)
	}
	if err := task.Transition(state.StatePlanReview); err != nil {
		t.Fatalf("transition to plan_review: %v", err)
	}
	return task
}

func sampleResult() Result {
	return Result{
		Phase:     state.PhasePlan,
		Loops:     3,
		LastScore: 42,
		Gaps: []state.Gap{
			{Severity: state.SeverityP0, Description: "missing auth requirement", Blocking: true},
			{Severity: state.SeverityP2, Description: "minor doc nit", Blocking: false},
			{Severity: state.SeverityP1, Description: "no error handling", Blocking: true},
		},
	}
}

func TestEscalate_StdoutDefault(t *testing.T) {
	task := taskInReviewState(t)
	var buf bytes.Buffer

	err := Escalate(
		context.Background(),
		task,
		sampleResult(),
		config.EscalationConfig{}, // empty NotifyVia → stdout default
		&buf,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if buf.Len() == 0 {
		t.Fatal("expected stdout output, got empty buffer")
	}
	if task.State != state.StateEscalated {
		t.Errorf("expected task state escalated, got %s", task.State)
	}
}

func TestEscalate_StdoutExplicit(t *testing.T) {
	task := taskInReviewState(t)
	var buf bytes.Buffer

	err := Escalate(
		context.Background(),
		task,
		sampleResult(),
		config.EscalationConfig{NotifyVia: "stdout"},
		&buf,
		nil,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	out := buf.String()
	if !strings.Contains(out, "VAIrdict Escalation") {
		t.Errorf("expected escalation header in output: %s", out)
	}
	if !strings.Contains(out, "abc12345") {
		t.Errorf("expected task id in output")
	}
	if !strings.Contains(out, "build a thing") {
		t.Errorf("expected intent in output")
	}
}

func TestEscalate_GitHubHappyPath(t *testing.T) {
	task := taskInReviewState(t)
	var buf bytes.Buffer
	gh := &fakePRCommenter{}

	result := sampleResult()
	result.PRNumber = 42

	err := Escalate(
		context.Background(),
		task,
		result,
		config.EscalationConfig{NotifyVia: "github"},
		&buf,
		gh,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.calls) != 1 {
		t.Fatalf("expected 1 PR comment, got %d", len(gh.calls))
	}
	if gh.calls[0].prNumber != 42 {
		t.Errorf("expected PR number 42, got %d", gh.calls[0].prNumber)
	}
	if !strings.Contains(gh.calls[0].body, "VAIrdict Escalation") {
		t.Errorf("expected escalation body posted to PR")
	}
	// stdout should NOT be used in github happy path.
	if buf.Len() != 0 {
		t.Errorf("expected empty stdout, got %q", buf.String())
	}
}

func TestEscalate_GitHubNoPRFallsBackToStdout(t *testing.T) {
	task := taskInReviewState(t)
	var buf bytes.Buffer
	gh := &fakePRCommenter{}

	result := sampleResult()
	result.PRNumber = 0 // no PR yet

	err := Escalate(
		context.Background(),
		task,
		result,
		config.EscalationConfig{NotifyVia: "github"},
		&buf,
		gh,
	)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.calls) != 0 {
		t.Errorf("expected no PR comments, got %d", len(gh.calls))
	}
	if buf.Len() == 0 {
		t.Error("expected fallback stdout output")
	}
}

func TestEscalate_GitHubMissingClient(t *testing.T) {
	task := taskInReviewState(t)
	var buf bytes.Buffer

	result := sampleResult()
	result.PRNumber = 99

	err := Escalate(
		context.Background(),
		task,
		result,
		config.EscalationConfig{NotifyVia: "github"},
		&buf,
		nil, // PRNumber set but no client
	)
	if err == nil {
		t.Fatal("expected error when github channel used without commenter")
	}
	if !strings.Contains(err.Error(), "no PR commenter") {
		t.Errorf("error should mention missing commenter, got: %v", err)
	}
}

func TestEscalate_GitHubClientError(t *testing.T) {
	task := taskInReviewState(t)
	var buf bytes.Buffer
	gh := &fakePRCommenter{err: errors.New("api down")}

	result := sampleResult()
	result.PRNumber = 7

	err := Escalate(
		context.Background(),
		task,
		result,
		config.EscalationConfig{NotifyVia: "github"},
		&buf,
		gh,
	)
	if err == nil {
		t.Fatal("expected error when commenter fails")
	}
	if !strings.Contains(err.Error(), "api down") {
		t.Errorf("expected wrapped client error, got: %v", err)
	}
}

func TestEscalate_UnknownChannel(t *testing.T) {
	task := taskInReviewState(t)
	var buf bytes.Buffer

	err := Escalate(
		context.Background(),
		task,
		sampleResult(),
		config.EscalationConfig{NotifyVia: "carrier-pigeon"},
		&buf,
		nil,
	)
	if err == nil {
		t.Fatal("expected error for unsupported channel")
	}
	if !strings.Contains(err.Error(), "carrier-pigeon") {
		t.Errorf("expected error to mention bad channel, got: %v", err)
	}
}

func TestEscalate_TransitionsToEscalated(t *testing.T) {
	task := taskInReviewState(t)
	if task.State == state.StateEscalated {
		t.Fatal("precondition: task should not start escalated")
	}
	var buf bytes.Buffer

	if err := Escalate(
		context.Background(),
		task,
		sampleResult(),
		config.EscalationConfig{NotifyVia: "stdout"},
		&buf,
		nil,
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.State != state.StateEscalated {
		t.Errorf("expected escalated state, got %s", task.State)
	}
}

func TestEscalate_AlreadyEscalatedNoOp(t *testing.T) {
	task := taskInReviewState(t)
	if err := task.Transition(state.StateEscalated); err != nil {
		t.Fatalf("setup transition: %v", err)
	}
	var buf bytes.Buffer

	// Should still notify even though state was already escalated.
	if err := Escalate(
		context.Background(),
		task,
		sampleResult(),
		config.EscalationConfig{NotifyVia: "stdout"},
		&buf,
		nil,
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if task.State != state.StateEscalated {
		t.Errorf("expected state to remain escalated")
	}
	if buf.Len() == 0 {
		t.Error("expected notification even when already escalated")
	}
}

func TestEscalate_InvalidStateReturnsError(t *testing.T) {
	// Pending state cannot transition directly to escalated.
	task := state.NewTask("xx", "test")
	var buf bytes.Buffer

	err := Escalate(
		context.Background(),
		task,
		sampleResult(),
		config.EscalationConfig{NotifyVia: "stdout"},
		&buf,
		nil,
	)
	if err == nil {
		t.Fatal("expected error transitioning from pending to escalated")
	}
	if !strings.Contains(err.Error(), "transitioning") {
		t.Errorf("expected transition error, got: %v", err)
	}
}

func TestEscalate_NilTask(t *testing.T) {
	var buf bytes.Buffer
	err := Escalate(
		context.Background(),
		nil,
		sampleResult(),
		config.EscalationConfig{NotifyVia: "stdout"},
		&buf,
		nil,
	)
	if err == nil {
		t.Fatal("expected error for nil task")
	}
}

func TestEscalate_NilWriter(t *testing.T) {
	task := taskInReviewState(t)
	err := Escalate(
		context.Background(),
		task,
		sampleResult(),
		config.EscalationConfig{NotifyVia: "stdout"},
		nil,
		nil,
	)
	if err == nil {
		t.Fatal("expected error for nil writer")
	}
}

func TestFormatSummary_ContainsAllFields(t *testing.T) {
	task := state.NewTask("task-99", "implement quality phase")
	result := Result{
		Phase:     state.PhaseQuality,
		Loops:     3,
		LastScore: 55.7,
		Gaps: []state.Gap{
			{Severity: state.SeverityP0, Description: "intent mismatch", Blocking: true},
		},
	}

	got := FormatSummary(task, result)

	cases := []string{
		"VAIrdict Escalation",
		"task-99",
		"implement quality phase",
		"quality",
		"Loops used:** 3",
		"56", // 55.7 rounds to 56 with %.0f
		"Blocking gaps",
		"P0",
		"intent mismatch",
		"Human intervention required",
	}
	for _, want := range cases {
		if !strings.Contains(got, want) {
			t.Errorf("summary missing %q\n--- summary ---\n%s", want, got)
		}
	}
}

func TestFormatSummary_OnlyBlockingGapsListed(t *testing.T) {
	task := state.NewTask("t1", "intent")
	result := Result{
		Phase: state.PhaseCode,
		Loops: 2,
		Gaps: []state.Gap{
			{Severity: state.SeverityP0, Description: "blocking-one", Blocking: true},
			{Severity: state.SeverityP2, Description: "non-blocking-nit", Blocking: false},
			{Severity: state.SeverityP3, Description: "deferred-thing", Blocking: false},
		},
	}

	got := FormatSummary(task, result)

	if !strings.Contains(got, "blocking-one") {
		t.Error("expected blocking gap in summary")
	}
	if strings.Contains(got, "non-blocking-nit") {
		t.Error("non-blocking gaps must not appear in summary")
	}
	if strings.Contains(got, "deferred-thing") {
		t.Error("deferred gaps must not appear in summary")
	}
}

func TestFormatSummary_NoBlockingGapsPlaceholder(t *testing.T) {
	task := state.NewTask("t1", "intent")
	result := Result{
		Phase: state.PhaseCode,
		Loops: 1,
		Gaps: []state.Gap{
			{Severity: state.SeverityP2, Description: "minor", Blocking: false},
		},
	}

	got := FormatSummary(task, result)
	if !strings.Contains(got, "No blocking gaps recorded") {
		t.Errorf("expected placeholder when no blocking gaps, got:\n%s", got)
	}
}
