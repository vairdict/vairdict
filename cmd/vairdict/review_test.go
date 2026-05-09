package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/github"
	"github.com/vairdict/vairdict/internal/state"
)

// TestRunReview_ValidatesOnlyQualityJudge guards the regression where
// `vairdict review` was rejecting the dogfood config in CI because
// validateBackends insists on a `claude` binary on PATH for the
// agents.coder slot — even though `vairdict review` never invokes the
// coder. The review subcommand only ever runs the quality judge, so
// it must validate that one role and nothing else.
//
// The scenario we reproduce is the exact one the auto-review action
// hits: a stock CI runner with no `claude` CLI installed and an
// Anthropic API key in env, against the in-repo vairdict.yaml where
// `agents.judge: claude` (smart default).
func TestRunReview_ValidatesOnlyQualityJudge(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude",
		Coder:   "claude-code", // would fail validateCoderBackend with no CLI
		Judge:   "claude",      // smart default — no binary required
	}}
	probes := backendProbes{
		cliAvailable:  func(string) bool { return false },
		apiKeyPresent: func() bool { return true },
	}

	// Sanity: the full validateBackends would reject this config.
	if err := validateBackends(cfg, probes); err == nil {
		t.Fatal("test setup is wrong: validateBackends must reject when claude binary is missing")
	}

	// What review.go now does: validate only the quality judge slot.
	if err := validateCompleterBackend(
		"agents.quality_judge",
		cfg.Agents.QualityJudgeBackend(),
		probes,
	); err != nil {
		t.Errorf("review-scoped validation must accept smart-default judge with no binary: %v", err)
	}
}

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
	verdict   *state.Verdict
	err       error
	intent    string
	plan      string
	diff      string
	checklist []state.ChecklistItem
}

func (f *fakeReviewJudge) Judge(_ context.Context, intent, plan, diff string, _ []state.Gap, checklist []state.ChecklistItem) (*state.Verdict, error) {
	f.checklist = checklist
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
		{Severity: state.SeverityCritical, Description: "broken", Blocking: true},
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

func TestRunReview_NoLinkedIssue_FallsBackToPRTitleBody(t *testing.T) {
	t.Parallel()
	gh := &fakeReviewGH{
		pr:   &github.PRDetails{Number: 9, Title: "fix: handle nil pointer", Body: "no closing keyword here"},
		diff: "diff --git a/x b/x\n+fix\n",
	}
	judge := &fakeReviewJudge{verdict: passingVerdict()}

	err := runReviewWith(context.Background(), 9, baseDeps(gh, judge))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(judge.intent, "fix: handle nil pointer") {
		t.Errorf("expected PR title in intent, got %q", judge.intent)
	}
	if !strings.Contains(judge.intent, "no closing keyword here") {
		t.Errorf("expected PR body in intent, got %q", judge.intent)
	}
}

// TestRunReview_Issue136_Fixtures covers the three scenarios called
// out in the acceptance criteria for issue #136:
//
//	(a) Closes #N PR with matching diff → judge runs against the
//	    linked issue's intent, verdict passes.
//	(b) Closes #N PR with empty diff → judge runs against the linked
//	    issue's intent and fails (correct behaviour).
//	(c) PR with only bare #N references in the body and docs-only
//	    diff → judge falls back to PR title+body; the linked issue
//	    is NOT consulted, so its acceptance criteria do not bleed
//	    into the verdict.
//
// Scenario (c) is the regression: PR #135 hit a P0 false-blocker
// because a bare "#126" mention in its body was interpreted as a
// closing keyword link.
func TestRunReview_Issue136_Fixtures(t *testing.T) {
	t.Parallel()
	const codeIssueIntentMarker = "internal/agents/codex package required"
	const docsBodyMarker = "No code changes — docs only."

	cases := []struct {
		name              string
		body              string
		title             string
		diff              string
		verdict           *state.Verdict
		wantIntentHas     string
		wantIntentMissing string
		wantErr           bool
	}{
		{
			name:          "(a) Closes #N with matching diff — passes",
			body:          "Closes #48",
			title:         "feat: add review subcommand",
			diff:          "diff --git a/cmd/review.go b/cmd/review.go\n+++ b/cmd/review.go\n+func Review() {}\n",
			verdict:       passingVerdict(),
			wantIntentHas: codeIssueIntentMarker,
		},
		{
			name:          "(b) Closes #N with empty diff — fails",
			body:          "Closes #48",
			title:         "feat: add review subcommand",
			diff:          "",
			verdict:       failingVerdict(),
			wantIntentHas: codeIssueIntentMarker,
			wantErr:       true,
		},
		{
			name:              "(c) bare #N references on docs-only PR — no false blocker",
			body:              docsBodyMarker + "\n\nUnblocks #126 by recording that #128 has landed.",
			title:             "docs: update PROGRESS.md and ROADMAP.md",
			diff:              "diff --git a/plans/PROGRESS.md b/plans/PROGRESS.md\n+++ b/plans/PROGRESS.md\n+ - #128 done\n",
			verdict:           passingVerdict(),
			wantIntentHas:     docsBodyMarker,
			wantIntentMissing: codeIssueIntentMarker,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gh := &fakeReviewGH{
				pr: &github.PRDetails{Number: 999, Title: tc.title, Body: tc.body},
				// Linked-issue body is the would-be wrong rubric — if
				// the regex incorrectly matches a bare #N, this body
				// would be threaded into the judge's intent and trigger
				// the PR #135 failure mode. The test checks it stays
				// out for scenario (c).
				issue: &github.IssueDetails{Number: 126, Title: "agents/codex backend", Body: codeIssueIntentMarker},
				diff:  tc.diff,
			}
			judge := &fakeReviewJudge{verdict: tc.verdict}

			err := runReviewWith(context.Background(), 999, baseDeps(gh, judge))
			if (err != nil) != tc.wantErr {
				t.Fatalf("err = %v, wantErr = %v", err, tc.wantErr)
			}
			if tc.wantIntentHas != "" && !strings.Contains(judge.intent, tc.wantIntentHas) {
				t.Errorf("intent missing %q.\nintent:\n%s", tc.wantIntentHas, judge.intent)
			}
			if tc.wantIntentMissing != "" && strings.Contains(judge.intent, tc.wantIntentMissing) {
				t.Errorf("intent must not contain %q (linked-issue body bled in).\nintent:\n%s",
					tc.wantIntentMissing, judge.intent)
			}
		})
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
