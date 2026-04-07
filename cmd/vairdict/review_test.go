package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/github"
	"github.com/vairdict/vairdict/internal/state"
)

// fakeReviewGH is a hand-rolled fake covering only the surface that
// runReviewWith uses. Records every PostVerdict call so tests can assert
// on what was sent.
type fakeReviewGH struct {
	pr           *github.PRDetails
	prErr        error
	issue        *github.IssueDetails
	issueErr     error
	diff         string
	diffErr      error
	postErr      error
	postedNumber int
	postedVerd   *state.Verdict
	postCalled   bool
}

func (f *fakeReviewGH) FetchPR(_ context.Context, _ int) (*github.PRDetails, error) {
	return f.pr, f.prErr
}
func (f *fakeReviewGH) FetchIssue(_ context.Context, _ int) (*github.IssueDetails, error) {
	return f.issue, f.issueErr
}
func (f *fakeReviewGH) FetchPRDiff(_ context.Context, _ int) (string, error) {
	return f.diff, f.diffErr
}
func (f *fakeReviewGH) PostVerdict(_ context.Context, n int, v *state.Verdict, _ state.Phase, _ int) error {
	f.postCalled = true
	f.postedNumber = n
	f.postedVerd = v
	return f.postErr
}

// fakeReviewJudge captures the inputs to Judge so tests can verify the
// command stitches the intent + diff together correctly.
type fakeReviewJudge struct {
	verdict *state.Verdict
	err     error
	intent  string
	plan    string
}

func (f *fakeReviewJudge) Judge(_ context.Context, intent, plan, _ string) (*state.Verdict, error) {
	f.intent = intent
	f.plan = plan
	return f.verdict, f.err
}

// resetReviewFlags clears package-level flag state between tests so they
// don't bleed into each other (cobra parses flags into globals).
func resetReviewFlags() {
	reviewIntentFlag = ""
	reviewNoCommentFlag = false
}

func passingVerdict() *state.Verdict {
	return &state.Verdict{Score: 90, Pass: true}
}

func failingVerdict() *state.Verdict {
	return &state.Verdict{Score: 30, Pass: false, Gaps: []state.Gap{
		{Severity: state.SeverityP0, Description: "broken", Blocking: true},
	}}
}

func TestRunReview_HappyPath_LinkedIssue(t *testing.T) {
	resetReviewFlags()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 46, Title: "feat: foo", Body: "Closes #48"},
		issue: &github.IssueDetails{Number: 48, Title: "review cmd", Body: "build it"},
		diff:  "diff --git a/x b/x\n+hi\n",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 46, reviewDeps{
		gh:     gh,
		judge:  judge,
		stdout: os.Stdout,
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gh.postCalled {
		t.Error("expected PostVerdict to be called")
	}
	if gh.postedNumber != 46 {
		t.Errorf("posted to PR %d, want 46", gh.postedNumber)
	}
	if !strings.Contains(judge.intent, "review cmd") || !strings.Contains(judge.intent, "build it") {
		t.Errorf("judge intent missing issue text: %q", judge.intent)
	}
	if !strings.Contains(judge.intent, "diff --git") {
		t.Errorf("judge intent missing diff: %q", judge.intent)
	}
	if judge.plan != "" {
		t.Errorf("plan should be empty in review mode, got %q", judge.plan)
	}
}

func TestRunReview_ExplicitIntentOverridesLinkedIssue(t *testing.T) {
	resetReviewFlags()
	reviewIntentFlag = "do exactly this"
	defer resetReviewFlags()

	gh := &fakeReviewGH{
		pr:   &github.PRDetails{Number: 5, Body: "Closes #1"},
		diff: "+x",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	if err := runReviewWith(context.Background(), 5, reviewDeps{gh: gh, judge: judge, stdout: os.Stdout}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(judge.intent, "do exactly this") {
		t.Errorf("expected explicit intent in judge call, got %q", judge.intent)
	}
	if gh.issue != nil && strings.Contains(judge.intent, "review cmd") {
		t.Error("issue should not have been fetched when --intent set")
	}
}

func TestRunReview_NoLinkedIssue_NoIntentFlag_Errors(t *testing.T) {
	resetReviewFlags()
	gh := &fakeReviewGH{
		pr: &github.PRDetails{Number: 9, Body: "no closing keyword here"},
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 9, reviewDeps{gh: gh, judge: judge, stdout: os.Stdout})
	if err == nil {
		t.Fatal("expected error for missing intent")
	}
	if !strings.Contains(err.Error(), "no linked issue") {
		t.Errorf("error should mention linked issue, got: %v", err)
	}
	if gh.postCalled {
		t.Error("PostVerdict should not be called on intent error")
	}
}

func TestRunReview_JudgeError_Propagates(t *testing.T) {
	resetReviewFlags()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue: &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{err: errors.New("boom")}

	err := runReviewWith(context.Background(), 1, reviewDeps{gh: gh, judge: judge, stdout: os.Stdout})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "quality judge") {
		t.Errorf("error should mention quality judge, got: %v", err)
	}
	if gh.postCalled {
		t.Error("verdict should not be posted on judge error")
	}
}

func TestRunReview_FailingVerdict_PostsAndExitsNonZero(t *testing.T) {
	resetReviewFlags()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue: &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{verdict: failingVerdict()}

	err := runReviewWith(context.Background(), 1, reviewDeps{gh: gh, judge: judge, stdout: os.Stdout})
	if err == nil {
		t.Fatal("expected non-nil error to gate CI on failing verdict")
	}
	if !gh.postCalled {
		t.Error("failing verdict should still be posted before exiting")
	}
}

func TestRunReview_NoComment_PrintsToStdout(t *testing.T) {
	resetReviewFlags()
	reviewNoCommentFlag = true
	defer resetReviewFlags()

	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue: &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	// Pipe stdout to a temp file we can read back.
	tmp, err := os.CreateTemp("", "review-stdout-*")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = os.Remove(tmp.Name()) }()
	defer func() { _ = tmp.Close() }()

	if err := runReviewWith(context.Background(), 1, reviewDeps{gh: gh, judge: judge, stdout: tmp}); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gh.postCalled {
		t.Error("PostVerdict should not be called when --no-comment is set")
	}
	if _, err := tmp.Seek(0, 0); err != nil {
		t.Fatal(err)
	}
	out, err := os.ReadFile(tmp.Name())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "VAIrdict Verdict") {
		t.Errorf("expected verdict in stdout, got: %q", string(out))
	}
}

func TestRunReview_FetchPRError(t *testing.T) {
	resetReviewFlags()
	gh := &fakeReviewGH{prErr: errors.New("not found")}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 99, reviewDeps{gh: gh, judge: judge, stdout: os.Stdout})
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunReview_FetchDiffError(t *testing.T) {
	resetReviewFlags()
	gh := &fakeReviewGH{
		pr:      &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue:   &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diffErr: errors.New("no diff"),
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 1, reviewDeps{gh: gh, judge: judge, stdout: os.Stdout})
	if err == nil {
		t.Fatal("expected error")
	}
}
