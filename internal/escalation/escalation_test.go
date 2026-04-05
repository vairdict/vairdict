package escalation

import (
	"bytes"
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// mockGitHubCommenter records calls to AddComment.
type mockGitHubCommenter struct {
	calls     []commentCall
	returnErr error
}

type commentCall struct {
	prNumber int
	body     string
}

func (m *mockGitHubCommenter) AddComment(_ context.Context, prNumber int, body string) error {
	m.calls = append(m.calls, commentCall{prNumber: prNumber, body: body})
	return m.returnErr
}

func newEscalatedTask() *state.Task {
	task := state.NewTask("abc123", "implement feature X")
	// Move to a review state so we can escalate.
	_ = task.Transition(state.StatePlanning)
	_ = task.Transition(state.StatePlanReview)
	_ = task.Transition(state.StateEscalated)
	return task
}

func newPlanReviewTask() *state.Task {
	task := state.NewTask("abc123", "implement feature X")
	_ = task.Transition(state.StatePlanning)
	_ = task.Transition(state.StatePlanReview)
	return task
}

func TestEscalate_StdoutNotification(t *testing.T) {
	task := newPlanReviewTask()

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhasePlan,
		Loops:     3,
		LastScore: 45,
		Verdict: &state.Verdict{
			Score: 45,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "missing error handling", Blocking: true},
				{Severity: state.SeverityP2, Description: "could use better naming", Blocking: false},
			},
		},
	}

	cfg := config.EscalationConfig{
		AfterLoops: 3,
		NotifyVia:  "stdout",
	}

	var buf bytes.Buffer
	err := Escalate(context.Background(), info, cfg, &buf, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if task.State != state.StateEscalated {
		t.Errorf("expected state escalated, got %s", task.State)
	}

	output := buf.String()
	if !strings.Contains(output, "plan phase failed") {
		t.Errorf("expected phase name in output, got:\n%s", output)
	}
	if !strings.Contains(output, "45%") {
		t.Errorf("expected score in output, got:\n%s", output)
	}
	if !strings.Contains(output, "missing error handling") {
		t.Errorf("expected blocking gap in output, got:\n%s", output)
	}
	if !strings.Contains(output, "could use better naming") {
		t.Errorf("expected non-blocking gap in output, got:\n%s", output)
	}
	if !strings.Contains(output, "Human intervention required") {
		t.Errorf("expected intervention message in output, got:\n%s", output)
	}
}

func TestEscalate_GitHubNotification(t *testing.T) {
	task := newPlanReviewTask()

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhaseCode,
		Loops:     3,
		LastScore: 60,
		PRNumber:  42,
		Verdict: &state.Verdict{
			Score: 60,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP1, Description: "tests failing", Blocking: true},
			},
		},
	}

	cfg := config.EscalationConfig{
		AfterLoops: 3,
		NotifyVia:  "github",
	}

	gh := &mockGitHubCommenter{}
	var buf bytes.Buffer

	err := Escalate(context.Background(), info, cfg, &buf, gh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(gh.calls) != 1 {
		t.Fatalf("expected 1 github comment, got %d", len(gh.calls))
	}
	if gh.calls[0].prNumber != 42 {
		t.Errorf("expected PR #42, got #%d", gh.calls[0].prNumber)
	}
	if !strings.Contains(gh.calls[0].body, "code phase failed") {
		t.Errorf("expected phase in comment body, got:\n%s", gh.calls[0].body)
	}
}

func TestEscalate_MultipleChannels(t *testing.T) {
	task := newPlanReviewTask()

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhasePlan,
		Loops:     2,
		LastScore: 30,
		PRNumber:  10,
	}

	cfg := config.EscalationConfig{
		AfterLoops: 3,
		NotifyVia:  "stdout,github",
	}

	gh := &mockGitHubCommenter{}
	var buf bytes.Buffer

	err := Escalate(context.Background(), info, cfg, &buf, gh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Stdout should have output.
	if buf.Len() == 0 {
		t.Error("expected stdout output")
	}

	// GitHub should have a comment.
	if len(gh.calls) != 1 {
		t.Errorf("expected 1 github comment, got %d", len(gh.calls))
	}
}

func TestEscalate_AlreadyEscalated(t *testing.T) {
	task := newEscalatedTask()

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhasePlan,
		Loops:     3,
		LastScore: 50,
	}

	cfg := config.EscalationConfig{
		AfterLoops: 3,
		NotifyVia:  "stdout",
	}

	var buf bytes.Buffer
	err := Escalate(context.Background(), info, cfg, &buf, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if task.State != state.StateEscalated {
		t.Errorf("expected state escalated, got %s", task.State)
	}
}

func TestEscalate_GitHubNoPR(t *testing.T) {
	task := newPlanReviewTask()

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhasePlan,
		Loops:     3,
		LastScore: 40,
		PRNumber:  0, // No PR.
	}

	cfg := config.EscalationConfig{
		AfterLoops: 3,
		NotifyVia:  "github",
	}

	gh := &mockGitHubCommenter{}
	var buf bytes.Buffer

	err := Escalate(context.Background(), info, cfg, &buf, gh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should not have posted a comment.
	if len(gh.calls) != 0 {
		t.Errorf("expected 0 github comments, got %d", len(gh.calls))
	}
}

func TestEscalate_GitHubNoClient(t *testing.T) {
	task := newPlanReviewTask()

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhasePlan,
		Loops:     3,
		LastScore: 40,
		PRNumber:  5,
	}

	cfg := config.EscalationConfig{
		AfterLoops: 3,
		NotifyVia:  "github",
	}

	var buf bytes.Buffer
	// nil github client — should not panic.
	err := Escalate(context.Background(), info, cfg, &buf, nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestEscalate_GitHubCommentError(t *testing.T) {
	task := newPlanReviewTask()

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhasePlan,
		Loops:     3,
		LastScore: 40,
		PRNumber:  5,
	}

	cfg := config.EscalationConfig{
		AfterLoops: 3,
		NotifyVia:  "github",
	}

	gh := &mockGitHubCommenter{returnErr: fmt.Errorf("api error")}
	var buf bytes.Buffer

	// Should not return error — github failures are logged, not fatal.
	err := Escalate(context.Background(), info, cfg, &buf, gh)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestFormatSummary_WithGapsAndQuestions(t *testing.T) {
	task := state.NewTask("task-1", "build the widget")

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhaseQuality,
		Loops:     3,
		LastScore: 55,
		Verdict: &state.Verdict{
			Score: 55,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "e2e tests fail", Blocking: true},
				{Severity: state.SeverityP1, Description: "missing edge case", Blocking: true},
				{Severity: state.SeverityP3, Description: "style nit", Blocking: false},
			},
			Questions: []state.Question{
				{Text: "Should we retry with different approach?", Priority: "high"},
			},
		},
	}

	summary := FormatSummary(info)

	checks := []string{
		"quality phase failed",
		"task-1",
		"build the widget",
		"55%",
		"Loops attempted:** 3",
		"Blocking gaps",
		"e2e tests fail",
		"missing edge case",
		"Non-blocking gaps",
		"style nit",
		"Open questions",
		"Should we retry",
		"Human intervention required",
	}

	for _, check := range checks {
		if !strings.Contains(summary, check) {
			t.Errorf("expected %q in summary, got:\n%s", check, summary)
		}
	}
}

func TestFormatSummary_NoVerdict(t *testing.T) {
	task := state.NewTask("task-2", "fix the bug")

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhaseCode,
		Loops:     3,
		LastScore: 0,
		Verdict:   nil,
	}

	summary := FormatSummary(info)

	if !strings.Contains(summary, "code phase failed") {
		t.Errorf("expected phase in summary, got:\n%s", summary)
	}
	if strings.Contains(summary, "Blocking gaps") {
		t.Errorf("expected no gaps section when verdict is nil, got:\n%s", summary)
	}
}

func TestFormatSummary_EmptyGaps(t *testing.T) {
	task := state.NewTask("task-3", "refactor module")

	info := EscalationInfo{
		Task:      task,
		Phase:     state.PhasePlan,
		Loops:     2,
		LastScore: 65,
		Verdict: &state.Verdict{
			Score: 65,
			Pass:  false,
			Gaps:  []state.Gap{},
		},
	}

	summary := FormatSummary(info)

	if strings.Contains(summary, "Blocking gaps") {
		t.Errorf("expected no gaps section when gaps empty, got:\n%s", summary)
	}
}
