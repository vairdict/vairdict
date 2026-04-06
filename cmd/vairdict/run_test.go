package main

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/escalation"
	"github.com/vairdict/vairdict/internal/state"
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
