package main

import (
	"bytes"
	"context"
	"errors"
	"io"
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
	mergeCalled  bool
	mergeErr     error
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
func (f *fakeReviewGH) PostVerdictWithDiff(_ context.Context, n int, v *state.Verdict, _ state.Phase, _ int, _ string) error {
	f.postCalled = true
	f.postedNumber = n
	f.postedVerd = v
	return f.postErr
}
func (f *fakeReviewGH) MergePR(_ context.Context, _ int) error {
	f.mergeCalled = true
	return f.mergeErr
}

// fakeReviewJudge captures the inputs to Judge so tests can verify the
// command threads intent / plan / diff through correctly.
type fakeReviewJudge struct {
	verdict *state.Verdict
	err     error
	intent  string
	plan    string
	diff    string
}

func (f *fakeReviewJudge) Judge(_ context.Context, intent, plan, diff string) (*state.Verdict, error) {
	f.intent = intent
	f.plan = plan
	f.diff = diff
	return f.verdict, f.err
}

func passingVerdict() *state.Verdict {
	return &state.Verdict{Score: 90, Pass: true}
}

func failingVerdict() *state.Verdict {
	return &state.Verdict{Score: 30, Pass: false, Gaps: []state.Gap{
		{Severity: state.SeverityP0, Description: "broken", Blocking: true},
	}}
}

// baseDeps builds a reviewDeps with sensible defaults; tests override
// the fields they care about. Stdout defaults to io.Discard so tests
// don't accidentally pollute the test runner's output.
func baseDeps(gh reviewGH, judge reviewJudge) reviewDeps {
	return reviewDeps{gh: gh, judge: judge, stdout: io.Discard}
}

func TestRunReview_HappyPath_LinkedIssue(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 46, Title: "feat: foo", Body: "Closes #48"},
		issue: &github.IssueDetails{Number: 48, Title: "review cmd", Body: "build it"},
		diff:  "diff --git a/x b/x\n+hi\n",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	if err := runReviewWith(context.Background(), 46, baseDeps(gh, judge)); err != nil {
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
	if !strings.Contains(judge.diff, "diff --git") {
		t.Errorf("judge diff missing PR diff: %q", judge.diff)
	}
	if judge.plan != "" {
		t.Errorf("plan should be empty in review mode, got %q", judge.plan)
	}
}

func TestRunReview_ExplicitIntentOverridesLinkedIssue(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:   &github.PRDetails{Number: 5, Body: "Closes #1"},
		diff: "+x",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	deps := baseDeps(gh, judge)
	deps.intent = "do exactly this"

	if err := runReviewWith(context.Background(), 5, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(judge.intent, "do exactly this") {
		t.Errorf("expected explicit intent in judge call, got %q", judge.intent)
	}
	if gh.issue != nil && strings.Contains(judge.intent, "review cmd") {
		t.Error("issue should not have been fetched when intent override set")
	}
}

func TestRunReview_NoLinkedIssue_NoIntentFlag_Errors(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr: &github.PRDetails{Number: 9, Body: "no closing keyword here"},
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 9, baseDeps(gh, judge))
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
	t.Parallel()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue: &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{err: errors.New("boom")}

	err := runReviewWith(context.Background(), 1, baseDeps(gh, judge))
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
	t.Parallel()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue: &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{verdict: failingVerdict()}

	err := runReviewWith(context.Background(), 1, baseDeps(gh, judge))
	if err == nil {
		t.Fatal("expected non-nil error to gate CI on failing verdict")
	}
	if !gh.postCalled {
		t.Error("failing verdict should still be posted before exiting")
	}
}

func TestRunReview_NoComment_PrintsToStdout(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue: &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	var buf bytes.Buffer
	deps := baseDeps(gh, judge)
	deps.noComment = true
	deps.stdout = &buf

	if err := runReviewWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gh.postCalled {
		t.Error("PostVerdict should not be called when noComment is set")
	}
	if !strings.Contains(buf.String(), "VAIrdict Verdict") {
		t.Errorf("expected verdict in stdout, got: %q", buf.String())
	}
}

func TestRunReview_FetchPRError(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{prErr: errors.New("not found")}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 99, baseDeps(gh, judge))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunReview_FetchDiffError(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:      &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue:   &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diffErr: errors.New("no diff"),
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 1, baseDeps(gh, judge))
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestRunReview_PostVerdictError_Propagates(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:      &github.PRDetails{Number: 1, Body: "Closes #2"},
		issue:   &github.IssueDetails{Number: 2, Title: "t", Body: "b"},
		diff:    "diff",
		postErr: errors.New("api down"),
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 1, baseDeps(gh, judge))
	if err == nil {
		t.Fatal("expected post error to propagate")
	}
	if !strings.Contains(err.Error(), "posting verdict") {
		t.Errorf("error should mention posting, got: %v", err)
	}
}

func TestRunReview_AutoMerge_Enabled(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 10, Body: "Closes #11"},
		issue: &github.IssueDetails{Number: 11, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	deps := baseDeps(gh, judge)
	deps.autoMerge = true

	if err := runReviewWith(context.Background(), 10, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !gh.mergeCalled {
		t.Error("expected MergePR to be called when autoMerge is enabled")
	}
}

func TestRunReview_AutoMerge_Disabled(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 10, Body: "Closes #11"},
		issue: &github.IssueDetails{Number: 11, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	deps := baseDeps(gh, judge)
	// autoMerge defaults to false

	if err := runReviewWith(context.Background(), 10, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gh.mergeCalled {
		t.Error("MergePR should not be called when autoMerge is disabled")
	}
}

func TestRunReview_AutoMerge_FailingVerdict_NoMerge(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:    &github.PRDetails{Number: 10, Body: "Closes #11"},
		issue: &github.IssueDetails{Number: 11, Title: "t", Body: "b"},
		diff:  "diff",
	}
	judge := &fakeReviewJudge{verdict: failingVerdict()}

	deps := baseDeps(gh, judge)
	deps.autoMerge = true

	err := runReviewWith(context.Background(), 10, deps)
	if err == nil {
		t.Fatal("expected error for failing verdict")
	}
	if gh.mergeCalled {
		t.Error("MergePR should not be called on failing verdict")
	}
}

func TestRunReview_AutoMerge_Error_WarnsOnly(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:       &github.PRDetails{Number: 10, Body: "Closes #11"},
		issue:    &github.IssueDetails{Number: 11, Title: "t", Body: "b"},
		diff:     "diff",
		mergeErr: errors.New("merge conflict"),
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	deps := baseDeps(gh, judge)
	deps.autoMerge = true

	// Auto-merge failure should warn, not error — the verdict still passed.
	if err := runReviewWith(context.Background(), 10, deps); err != nil {
		t.Fatalf("auto-merge failure should not propagate as error, got: %v", err)
	}
	if !gh.mergeCalled {
		t.Error("expected MergePR to be called")
	}
}
