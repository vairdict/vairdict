package github

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/vairdict/vairdict/internal/state"
)

func successRunner() *FakeRunner {
	return &FakeRunner{
		Responses: map[string]fakeResponse{
			"git rev-parse": {Output: []byte(".git")},
			"git remote":    {Output: []byte("https://github.com/foo/bar")},
			"gh auth":       {Output: []byte("Logged in")},
			"git push":      {Output: []byte("ok")},
			"git checkout":  {Output: []byte("ok")},
			"gh pr create":  {Output: []byte("https://github.com/foo/bar/pull/1\n")},
			"gh pr comment": {Output: []byte("ok")},
			"gh pr review":  {Output: []byte("ok")},
			"gh pr merge":   {Output: []byte("ok")},
		},
	}
}

func TestCreateBranch(t *testing.T) {
	cases := []struct {
		name, taskID, intent, want string
	}{
		{"empty_intent", "abc123", "", "vairdict/abc123"},
		{"simple_intent", "abc123", "add logo to verdict", "vairdict/add-logo-to-verdict-abc123"},
		{"conventional_prefix", "abc123", "ui: VAIrdict logo in PR header", "vairdict/vairdict-logo-in-pr-header-abc123"},
		{"multiline_takes_first_line", "abc123", "fix: parse error\n\nmore details here", "vairdict/parse-error-abc123"},
		{"truncates_long_intent", "abc123", "this is a very long intent that should get cut off well before forty characters of slug", "vairdict/this-is-a-very-long-intent-that-should-g-abc123"},
		{"unsluggable_falls_back", "abc123", "!!! ???", "vairdict/abc123"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			runner := successRunner()
			client := New(runner)
			branch, err := client.CreateBranch(context.Background(), tc.taskID, tc.intent)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if branch != tc.want {
				t.Errorf("branch = %q, want %q", branch, tc.want)
			}
		})
	}
}

func TestCreatePR_Success(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	pr, err := client.CreatePR(context.Background(), CreatePROpts{
		Title:      "test PR",
		Body:       "test body",
		BaseBranch: "main",
		HeadBranch: "vairdict/abc123",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr.URL != "https://github.com/foo/bar/pull/1" {
		t.Errorf("url = %q", pr.URL)
	}
}

func TestCreatePR_NotGitRepo(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"git rev-parse": {Err: errors.New("not a git repo")},
		},
	}
	client := New(runner)

	_, err := client.CreatePR(context.Background(), CreatePROpts{Title: "test"})
	if err == nil {
		t.Fatal("expected error for non-git repo")
	}
}

func TestCreatePR_NoRemote(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"git rev-parse": {Output: []byte(".git")},
			"git remote":    {Err: errors.New("no remote")},
		},
	}
	client := New(runner)

	_, err := client.CreatePR(context.Background(), CreatePROpts{Title: "test"})
	if err == nil {
		t.Fatal("expected error for no remote")
	}
}

func TestCreatePR_AuthFailure(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"git rev-parse": {Output: []byte(".git")},
			"git remote":    {Output: []byte("url")},
			"gh auth":       {Err: errors.New("not logged in")},
		},
	}
	client := New(runner)

	_, err := client.CreatePR(context.Background(), CreatePROpts{Title: "test"})
	if err == nil {
		t.Fatal("expected error for auth failure")
	}
}

func TestAddComment(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	err := client.AddComment(context.Background(), 1, "test comment")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestApprovePR(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	err := client.ApprovePR(context.Background(), 42, "Looks good!")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verify the gh pr review command was called.
	found := false
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 3 && call.Args[0] == "pr" && call.Args[1] == "review" {
			found = true
			if call.Args[3] != "--approve" {
				t.Errorf("expected --approve flag, got %q", call.Args[3])
			}
		}
	}
	if !found {
		t.Error("gh pr review was not called")
	}
}

func TestApprovePR_Error(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh pr review": {Err: errors.New("review failed")},
		},
	}
	client := New(runner)

	err := client.ApprovePR(context.Background(), 42, "body")
	if err == nil {
		t.Fatal("expected error")
	}
}

func TestPostVerdict_Pass_ApprovesAndComments(t *testing.T) {
	runner := successRunner()
	runner.Responses["gh pr review"] = fakeResponse{Output: []byte("ok")}
	client := New(runner)

	verdict := &state.Verdict{
		Score: 95,
		Pass:  true,
		Gaps: []state.Gap{
			{Severity: state.SeverityLow, Description: "minor style", Blocking: false},
		},
	}

	err := client.PostVerdict(context.Background(), 7, verdict, state.PhaseQuality, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have called gh pr review (approve), not gh pr comment.
	foundReview := false
	foundComment := false
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 2 {
			if call.Args[0] == "pr" && call.Args[1] == "review" {
				foundReview = true
			}
			if call.Args[0] == "pr" && call.Args[1] == "comment" {
				foundComment = true
			}
		}
	}
	if !foundReview {
		t.Error("expected gh pr review to be called for passing verdict")
	}
	if foundComment {
		t.Error("did not expect gh pr comment for passing verdict")
	}
}

func TestPostVerdict_Pass_SelfAuthored_FallsBackToComment(t *testing.T) {
	// gh returns this exact error message when the authenticated user
	// tries to approve their own PR. PostVerdict should swallow it and
	// fall back to a plain comment so review-mode dogfooding works.
	runner := successRunner()
	runner.Responses["gh pr review"] = fakeResponse{
		Err: errors.New("failed to create review: GraphQL: Review Can not approve your own pull request (addPullRequestReview)"),
	}
	client := New(runner)

	verdict := &state.Verdict{Score: 92, Pass: true}
	err := client.PostVerdict(context.Background(), 59, verdict, state.PhaseQuality, 1)
	if err != nil {
		t.Fatalf("expected fallback to comment, got error: %v", err)
	}

	foundComment := false
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "pr" && call.Args[1] == "comment" {
			foundComment = true
		}
	}
	if !foundComment {
		t.Error("expected fallback to gh pr comment after approval rejection")
	}
}

func TestPostVerdict_Pass_ActionsTokenDenied_FallsBackToComment(t *testing.T) {
	// GitHub Actions tokens cannot approve PRs unless explicitly allowed
	// in repo settings. PostVerdict should fall back to a comment.
	runner := successRunner()
	runner.Responses["gh pr review"] = fakeResponse{
		Err: errors.New("failed to create review: GraphQL: GitHub Actions is not permitted to approve pull requests. (addPullRequestReview)"),
	}
	client := New(runner)

	verdict := &state.Verdict{Score: 95, Pass: true}
	err := client.PostVerdict(context.Background(), 75, verdict, state.PhaseQuality, 1)
	if err != nil {
		t.Fatalf("expected fallback to comment, got error: %v", err)
	}

	foundComment := false
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 2 && call.Args[0] == "pr" && call.Args[1] == "comment" {
			foundComment = true
		}
	}
	if !foundComment {
		t.Error("expected fallback to gh pr comment after Actions approval rejection")
	}
}

func TestPostVerdict_Pass_ApprovalError_FallsBackToComment(t *testing.T) {
	runner := successRunner()
	runner.Responses["gh pr review"] = fakeResponse{Err: errors.New("network error")}
	client := New(runner)

	verdict := &state.Verdict{Score: 92, Pass: true}
	err := client.PostVerdict(context.Background(), 7, verdict, state.PhaseQuality, 1)
	if err != nil {
		t.Fatalf("expected fallback to comment, got error: %v", err)
	}
}

func TestPostVerdict_Fail_CommentsOnly(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	verdict := &state.Verdict{
		Score: 40,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityCritical, Description: "build fails", Blocking: true},
			{Severity: state.SeverityHigh, Description: "tests fail", Blocking: true},
		},
	}

	err := client.PostVerdict(context.Background(), 7, verdict, state.PhaseCode, 2)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have called gh pr comment, not gh pr review.
	foundReview := false
	foundComment := false
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 2 {
			if call.Args[0] == "pr" && call.Args[1] == "review" {
				foundReview = true
			}
			if call.Args[0] == "pr" && call.Args[1] == "comment" {
				foundComment = true
			}
		}
	}
	if foundReview {
		t.Error("did not expect gh pr review for failing verdict")
	}
	if !foundComment {
		t.Error("expected gh pr comment to be called for failing verdict")
	}
}

func TestPostVerdict_DeletesPreviousVerdicts(t *testing.T) {
	runner := successRunner()
	// Simulate gh api listing two previous verdict comment IDs.
	runner.Responses["gh api"] = fakeResponse{
		Output: []byte("111\n222\n"),
	}
	client := New(runner)

	verdict := &state.Verdict{Score: 90, Pass: true}
	err := client.PostVerdict(context.Background(), 7, verdict, state.PhaseQuality, 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have called gh api DELETE for each old comment ID.
	deleteCount := 0
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 3 &&
			call.Args[0] == "api" && call.Args[1] == "-X" && call.Args[2] == "DELETE" {
			deleteCount++
		}
	}
	if deleteCount != 2 {
		t.Errorf("expected 2 DELETE calls for old verdicts, got %d", deleteCount)
	}
}

func TestMergePR_Success(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	err := client.MergePR(context.Background(), 42)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	found := false
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 3 &&
			call.Args[0] == "pr" && call.Args[1] == "merge" && call.Args[2] == "42" {
			found = true
			// Verify squash + delete-branch flags
			hasSquash := false
			hasDelete := false
			for _, a := range call.Args {
				if a == "--squash" {
					hasSquash = true
				}
				if a == "--delete-branch" {
					hasDelete = true
				}
			}
			if !hasSquash {
				t.Error("expected --squash flag")
			}
			if !hasDelete {
				t.Error("expected --delete-branch flag")
			}
		}
	}
	if !found {
		t.Error("expected gh pr merge to be called")
	}
}

func TestMergePR_Error(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh pr merge": {Err: errors.New("merge conflict")},
		},
	}
	client := New(runner)

	err := client.MergePR(context.Background(), 99)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "merge conflict") {
		t.Errorf("expected wrapped merge conflict error, got: %v", err)
	}
}

func TestFormatVerdictComment_Pass(t *testing.T) {
	verdict := &state.Verdict{
		Score: 95,
		Pass:  true,
		Gaps: []state.Gap{
			{Severity: state.SeverityLow, Description: "minor style nit", Blocking: false},
		},
	}

	comment := FormatVerdictComment(verdict, state.PhaseQuality, 1, nil)

	checks := []struct {
		name string
		want string
	}{
		{"header", "VAIrdict Verdict: ✅ PASS</h2>"},
		{"logo", `<img src="`},
		{"logo alt", `alt="VAIrdict"`},
		{"logo height", `height="24"`},
		{"phase", "**Phase:** quality"},
		{"loop", "**Loop:** 1"},
		{"gap severity", "| Low |"},
		{"gap description", "minor style nit"},
		{"footer", "@vairdict-judge"},
	}

	for _, c := range checks {
		if !contains(comment, c.want) {
			t.Errorf("comment missing %s (%q)", c.name, c.want)
		}
	}

	// Pass verdict should NOT have blocking gaps section.
	if contains(comment, "### Blocking Gaps") {
		t.Error("pass verdict should not have blocking gaps section")
	}
}

func TestFormatVerdictComment_Fail(t *testing.T) {
	verdict := &state.Verdict{
		Score: 40,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityCritical, Description: "build broken", Blocking: true},
			{Severity: state.SeverityHigh, Description: "tests fail", Blocking: true},
			{Severity: state.SeverityLow, Description: "docs missing", Blocking: false},
		},
		Questions: []state.Question{
			{Text: "Is the API stable?", Priority: "high"},
		},
	}

	comment := FormatVerdictComment(verdict, state.PhaseCode, 2, nil)

	checks := []struct {
		name string
		want string
	}{
		{"header", "VAIrdict Verdict: ❌ FAIL</h2>"},
		{"logo", `<img src="`},
		{"logo alt", `alt="VAIrdict"`},
		{"logo height", `height="24"`},
		{"loop", "**Loop:** 2"},
		{"blocking section", "### Blocking Gaps"},
		{"critical gap", "**[Critical]** build broken"},
		{"high gap", "**[High]** tests fail"},
		{"question", "Is the API stable?"},
		{"criteria table", "| Severity | Status | Description |"},
	}

	for _, c := range checks {
		if !contains(comment, c.want) {
			t.Errorf("comment missing %s (%q)", c.name, c.want)
		}
	}
}

func TestFormatVerdictComment_NoGaps(t *testing.T) {
	verdict := &state.Verdict{
		Score: 100,
		Pass:  true,
	}

	comment := FormatVerdictComment(verdict, state.PhasePlan, 1, nil)

	if contains(comment, "### Criteria") {
		t.Error("should not have criteria table when no gaps")
	}
	if !contains(comment, "PASS") {
		t.Error("should contain PASS")
	}
	// A passing verdict with no gaps must still say something concrete
	// about the review outcome — otherwise reviewers of a large diff see
	// nothing and assume the judge was a no-op.
	if !contains(comment, "No issues found") {
		t.Error("pass with no gaps should explicitly say no issues found")
	}
}

func TestFormatVerdictComment_RendersModel(t *testing.T) {
	// AC: verdict output records which model produced the verdict so PR
	// comments can show it. When Model is set, the score line surfaces it
	// alongside score/phase/loop.
	verdict := &state.Verdict{
		Score: 95,
		Pass:  true,
		Model: "claude-opus-4-7",
	}

	comment := FormatVerdictComment(verdict, state.PhaseQuality, 1, nil)

	if !contains(comment, "claude-opus-4-7") {
		t.Errorf("comment must mention the model that produced the verdict, got:\n%s", comment)
	}
	if !contains(comment, "**Model:**") {
		t.Errorf("comment must label the model, got:\n%s", comment)
	}
}

func TestFormatVerdictComment_NoModelOmitted(t *testing.T) {
	// Code judge runs deterministic shell checks (lint/test/build) and
	// leaves Model empty. The summary line must omit the model field
	// rather than render an empty `Model: \`\`` artifact.
	verdict := &state.Verdict{
		Score: 100,
		Pass:  true,
	}

	comment := FormatVerdictComment(verdict, state.PhaseCode, 1, nil)

	if contains(comment, "**Model:**") {
		t.Errorf("comment must omit Model label when verdict has no model, got:\n%s", comment)
	}
}

// TestFormatVerdictComment_OmitsScoreField pins the deliberate
// removal of the Score field from the rendered comment. With AC
// tracing landed, Pass / NEEDS_WORK is mechanical — gate is
// DeriveVerdictState(gaps, checklist), not score. A 100% score next
// to a NEEDS_WORK verdict (or 60% next to PASS) is more confusing
// than helpful, and the header / Criteria table / AC matrix already
// convey what the reader needs. Score still lives on state.Verdict
// for debug logs; just not in the rendered comment.
func TestFormatVerdictComment_OmitsScoreField(t *testing.T) {
	cases := []struct {
		name string
		v    *state.Verdict
	}{
		{"pass with full score", &state.Verdict{Score: 100, Pass: true}},
		{"fail with low score", &state.Verdict{Score: 40, Pass: false}},
		{"pass with mid score", &state.Verdict{Score: 75, Pass: true}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := FormatVerdictComment(tc.v, state.PhaseQuality, 1, nil)
			if contains(got, "**Score:**") {
				t.Errorf("rendered comment must not contain **Score:** label, got:\n%s", got)
			}
		})
	}
}

func TestFormatVerdictComment_FailWithNoGaps_NoSuchMessage(t *testing.T) {
	// "No issues found" is a PASS-only affirmation. A failing verdict
	// (even one without structured gaps) must never render it.
	verdict := &state.Verdict{
		Score: 0,
		Pass:  false,
	}

	comment := FormatVerdictComment(verdict, state.PhaseQuality, 1, nil)

	if contains(comment, "No issues found") {
		t.Error("fail verdict must not render the no-issues affirmation")
	}
}

func TestFormatVerdictComment_RendersSummary(t *testing.T) {
	// A passing verdict with no gaps previously rendered only the score
	// line, dropping the judge's Reviewed/Notes narrative — see PR #107
	// where a 1200-line diff got an empty comment. The summary must
	// survive into the posted PR comment.
	summary := "## Reviewed\n- diff against plan\n\n## Notes\n- watch for follow-up"
	verdict := &state.Verdict{
		Score:   95,
		Pass:    true,
		Summary: summary,
	}

	comment := FormatVerdictComment(verdict, state.PhaseQuality, 1, nil)

	if !contains(comment, "## Reviewed") {
		t.Error("comment missing Reviewed section from Summary")
	}
	if !contains(comment, "diff against plan") {
		t.Error("comment missing Reviewed bullet from Summary")
	}
	if !contains(comment, "## Notes") {
		t.Error("comment missing Notes section from Summary")
	}
	if !contains(comment, "watch for follow-up") {
		t.Error("comment missing Notes bullet from Summary")
	}
}

func TestFormatVerdictComment_EmptySummaryNotRendered(t *testing.T) {
	verdict := &state.Verdict{
		Score:   100,
		Pass:    true,
		Summary: "   \n\t  ",
	}

	comment := FormatVerdictComment(verdict, state.PhasePlan, 1, nil)

	if contains(comment, "## Reviewed") || contains(comment, "## Notes") {
		t.Error("whitespace-only summary should not emit any section")
	}
}

func TestParsePRNumber(t *testing.T) {
	tests := []struct {
		url  string
		want int
	}{
		{"https://github.com/foo/bar/pull/123", 123},
		{"https://github.com/foo/bar/pull/1", 1},
		{"", 0},
		{"not-a-url", 0},
		{"https://github.com/foo/bar/pull/abc", 0},
	}
	for _, tt := range tests {
		got := parsePRNumber(tt.url)
		if got != tt.want {
			t.Errorf("parsePRNumber(%q) = %d, want %d", tt.url, got, tt.want)
		}
	}
}

func TestCreatePR_ParsesNumber(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	pr, err := client.CreatePR(context.Background(), CreatePROpts{
		Title:      "test",
		Body:       "body",
		BaseBranch: "main",
		HeadBranch: "vairdict/abc",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr.Number != 1 {
		t.Errorf("pr.Number = %d, want 1", pr.Number)
	}
}

func TestFormatPRBody(t *testing.T) {
	task := &state.Task{
		Intent: "add feature X",
		Assumptions: []state.Assumption{
			{Severity: state.SeverityMedium, Description: "assumed Y"},
		},
		Attempts: []state.Attempt{
			{
				Loop: 2,
				Verdict: &state.Verdict{
					Score: 92,
					Pass:  true,
				},
			},
		},
	}

	body := FormatPRBody(task, 42, "Implemented feature X")

	if !contains(body, "Closes #42") {
		t.Error("body should contain issue link")
	}
	if !contains(body, "Implemented feature X") {
		t.Error("body should contain summary")
	}
	if !contains(body, "assumed Y") {
		t.Error("body should contain assumptions")
	}
	if !contains(body, "Score: 92%") {
		t.Error("body should contain verdict score")
	}
}

func TestGeneratePRTitle_Short(t *testing.T) {
	task := &state.Task{Intent: "add feature X"}
	title := GeneratePRTitle(task)
	if title != "add feature X" {
		t.Errorf("title = %q", title)
	}
}

func TestGeneratePRTitle_Long(t *testing.T) {
	task := &state.Task{
		Intent: "this is a very long intent that exceeds seventy characters and should be truncated properly",
	}
	title := GeneratePRTitle(task)
	if len(title) > 70 {
		t.Errorf("title length = %d, want <= 70", len(title))
	}
}

func TestFetchPR(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh pr view": {Output: []byte(`{"number":46,"title":"add review cmd","body":"Closes #48","headRefName":"feat/x","baseRefName":"main"}`)},
		},
	}
	client := New(runner)

	pr, err := client.FetchPR(context.Background(), 46)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if pr.Number != 46 || pr.Title != "add review cmd" || pr.HeadRefName != "feat/x" || pr.BaseRefName != "main" {
		t.Errorf("unexpected pr: %+v", pr)
	}
}

func TestFetchPR_RunError(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh pr view": {Err: errors.New("not found")},
		},
	}
	if _, err := New(runner).FetchPR(context.Background(), 9); err == nil {
		t.Fatal("expected error")
	}
}

func TestFetchPR_InvalidJSON(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh pr view": {Output: []byte("not json")},
		},
	}
	if _, err := New(runner).FetchPR(context.Background(), 9); err == nil {
		t.Fatal("expected json parse error")
	}
}

func TestFetchIssue(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh issue view": {Output: []byte(`{"number":48,"title":"review cmd","body":"intent here"}`)},
		},
	}
	iss, err := New(runner).FetchIssue(context.Background(), 48)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if iss.Number != 48 || iss.Title != "review cmd" || iss.Body != "intent here" {
		t.Errorf("unexpected issue: %+v", iss)
	}
}

func TestFetchPRDiff(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh pr diff": {Output: []byte("diff --git a/x b/x\n+hello\n")},
		},
	}
	diff, err := New(runner).FetchPRDiff(context.Background(), 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !contains(diff, "+hello") {
		t.Errorf("unexpected diff: %q", diff)
	}
}

func TestParseLinkedIssue(t *testing.T) {
	cases := []struct {
		name string
		body string
		want int
	}{
		// Positives — closing keyword directly attached to the #N ref.
		{"closes capitalised", "Closes #42", 42},
		{"closes lowercase", "closes #42", 42},
		{"fixes with body", "Fixes #7\n\nlots of context", 7},
		{"resolves with em-dash", "Resolves #123 — done", 123},
		{"fixed past tense", "fixed #5", 5},
		{"resolved past tense", "resolved #5", 5},
		{"closes with colon", "some text Closes: #99", 99},
		{"closes after heading", "## Issue\nCloses #48\n", 48},

		// Negatives — no closing keyword, or keyword separated from #N.
		{"plain prose", "no linked issue here", 0},
		{"bare ref", "#42 alone is not enough", 0},
		{"see ref", "see #42 for context", 0},
		{"empty", "", 0},

		// Negatives that the previous (greedy) regex got wrong — the
		// keyword and the #N must be adjacent, otherwise the body is just
		// referring to an issue, not committing to close it. These are
		// the PR #135 / issue #136 reproducers.
		{"fixes typo while referring to ref", "This PR fixes a typo while preparing for #126.", 0},
		{"closes far from ref", "Closes the connection bug discussed in PR #135 about #126", 0},
		{"fix preceding ref by word", "fix typo in #126 doc", 0},
		{"unblocks bare ref", "Unblocks #126 work — docs only", 0},
		{"updates bare refs", "Updates PROGRESS.md to track #126 progress and #128 too.", 0},
		{"verbatim PR135 body", "No code changes — docs only.\n\nUnblocks #126 by recording that #128 has landed.", 0},
		{"close in different sentence", "Closes the loop on logging.\nSee related discussion in #200.", 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ParseLinkedIssue(tc.body); got != tc.want {
				t.Errorf("ParseLinkedIssue(%q) = %d, want %d", tc.body, got, tc.want)
			}
		})
	}
}

func TestPostVerdictWithDiff_InlineComments(t *testing.T) {
	diff := `diff --git a/internal/foo/bar.go b/internal/foo/bar.go
--- a/internal/foo/bar.go
+++ b/internal/foo/bar.go
@@ -10,6 +10,8 @@ func existing() {
 	unchanged := true
 	_ = unchanged
+	added1 := "new"
+	added2 := "also new"
 	more := "context"
 }
`
	runner := successRunner()
	client := New(runner)

	verdict := &state.Verdict{
		Score: 40,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityHigh, Description: "bug on added line", Blocking: true, File: "internal/foo/bar.go", Line: 12},
			{Severity: state.SeverityMedium, Description: "style issue elsewhere", Blocking: false}, // no file/line
			{Severity: state.SeverityLow, Description: "line not in diff", Blocking: false, File: "internal/foo/bar.go", Line: 1},
		},
	}

	err := client.PostVerdictWithDiff(context.Background(), 7, verdict, state.PhaseQuality, 1, diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Should have called gh api to create a review with inline comments.
	foundReviewAPI := false
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 2 &&
			call.Args[0] == "api" && strings.Contains(strings.Join(call.Args, " "), "reviews") {
			foundReviewAPI = true
		}
	}
	if !foundReviewAPI {
		t.Error("expected gh api call to create review with inline comments")
	}
}

func TestPostVerdictWithDiff_UnanchoredGapsSurfaceInComment(t *testing.T) {
	// Gaps without file/line don't produce inline comments, so when
	// there are no inline comments the verdict is posted as a plain
	// comment. The unanchored gaps must still appear in the criteria
	// table of that comment.
	diff := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,3 +1,4 @@
 package main
+func New() {}
 func Old() {}
`
	runner := successRunner()
	client := New(runner)

	verdict := &state.Verdict{
		Score: 80,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityMedium, Description: "missing docs", Blocking: false},
		},
	}

	if err := client.PostVerdictWithDiff(context.Background(), 5, verdict, state.PhaseQuality, 1, diff); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// No inline comments → should fall back to gh pr comment, not review API.
	var sawComment bool
	for _, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) >= 2 && call.Args[1] == "comment" {
			sawComment = true
			body := strings.Join(call.Args, " ")
			if !strings.Contains(body, "missing docs") {
				t.Error("verdict comment should contain the unanchored gap")
			}
		}
	}
	if !sawComment {
		t.Error("expected gh pr comment call for verdict with no inline comments")
	}
}

func TestPostVerdictWithDiff_EmptyDiffSkipsInline(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	verdict := &state.Verdict{
		Score: 90,
		Pass:  true,
		Gaps: []state.Gap{
			{Severity: state.SeverityLow, Description: "nit", Blocking: false, File: "x.go", Line: 5},
		},
	}

	// Empty diff — should not attempt inline comments.
	err := client.PostVerdictWithDiff(context.Background(), 3, verdict, state.PhaseQuality, 1, "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBuildInlineReview_FiltersByResolvability(t *testing.T) {
	// #72: gaps with file+line resolvable to a diff position become inline
	// comments. Gaps without file/line, or with file/line that don't map
	// into the diff, are surfaced as bullets in the review body so they
	// remain visible in the PR review rather than only in the verdict
	// criteria table.
	diff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -10,3 +10,5 @@
 ctx0
+added11
+added12
 ctx1
`
	verdict := &state.Verdict{
		Gaps: []state.Gap{
			{Severity: state.SeverityHigh, Description: "in diff", Blocking: true, File: "foo.go", Line: 11},
			{Severity: state.SeverityMedium, Description: "no file info"},
			{Severity: state.SeverityLow, Description: "line outside diff", File: "foo.go", Line: 999},
			{Severity: state.SeverityCritical, Description: "wrong file", File: "bar.go", Line: 11},
		},
	}

	result := BuildInlineReview(verdict, diff)
	if result.Payload == nil {
		t.Fatal("expected review payload, got nil")
	}
	if result.Payload.Event != "COMMENT" {
		t.Errorf("expected event=COMMENT (batched, no notification spam), got %q", result.Payload.Event)
	}
	if len(result.Payload.Comments) != 1 {
		t.Fatalf("expected 1 inline comment, got %d", len(result.Payload.Comments))
	}
	only := result.Payload.Comments[0]
	if only.Path != "foo.go" {
		t.Errorf("expected path foo.go, got %q", only.Path)
	}
	if only.Position <= 0 {
		t.Errorf("expected positive diff position, got %d", only.Position)
	}
	if !contains(only.Body, "in diff") {
		t.Errorf("expected body to include gap description, got %q", only.Body)
	}
	// The three non-resolvable gaps must now appear in the review body so
	// reviewers see every concern, not just the subset with anchors.
	for _, want := range []string{"no file info", "line outside diff", "wrong file"} {
		if !contains(result.Payload.Body, want) {
			t.Errorf("expected unanchored gap %q in review body, got %q", want, result.Payload.Body)
		}
	}
	// Verify inline gap index tracking.
	if !result.InlineGapIndices[0] {
		t.Error("expected gap index 0 to be marked as inline")
	}
	if result.InlineGapIndices[1] || result.InlineGapIndices[2] || result.InlineGapIndices[3] {
		t.Error("non-inline gap indices should not be in InlineGapIndices")
	}
}

func TestBuildInlineReview_UnanchoredGapsStillSurface(t *testing.T) {
	// When every gap is location-less or out-of-diff we used to return
	// nil and drop the whole review; now we still post a review whose
	// body lists the unanchored gaps. Guards the "no gap silently
	// disappears" invariant.
	diff := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,3 +1,4 @@
 a
+b
 c
`
	verdict := &state.Verdict{
		Gaps: []state.Gap{
			{Severity: state.SeverityMedium, Description: "no location"},
			{Severity: state.SeverityLow, Description: "out of diff", File: "x.go", Line: 99},
		},
	}

	result := BuildInlineReview(verdict, diff)
	if result.Payload == nil {
		t.Fatal("expected review payload so unanchored gaps surface, got nil")
	}
	if len(result.Payload.Comments) != 0 {
		t.Errorf("expected 0 inline comments, got %d", len(result.Payload.Comments))
	}
	for _, want := range []string{"no location", "out of diff"} {
		if !contains(result.Payload.Body, want) {
			t.Errorf("expected unanchored gap %q in review body, got %q", want, result.Payload.Body)
		}
	}
}

func TestBuildInlineReview_NilWhenNoGapsAtAll(t *testing.T) {
	// The only case where we skip the API call entirely is when the
	// verdict has no gaps — no inline, no unanchored, nothing to post.
	diff := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,3 +1,4 @@
 a
+b
 c
`
	verdict := &state.Verdict{}
	result := BuildInlineReview(verdict, diff)
	if result.Payload != nil {
		t.Errorf("expected nil payload when verdict has no gaps, got %+v", result.Payload)
	}
}

func TestBuildInlineReview_MixedInlineAndSummary(t *testing.T) {
	// A gap with a resolvable location produces an inline comment; a
	// location-less gap alongside is dropped from the inline payload but
	// still appears in the summary rendered by FormatVerdictComment. This
	// test pins both halves to guard the "additive, not replacement" rule
	// in the issue.
	diff := `diff --git a/p.go b/p.go
--- a/p.go
+++ b/p.go
@@ -1,2 +1,3 @@
 a
+b
 c
`
	verdict := &state.Verdict{
		Score: 60,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityHigh, Description: "bad on added line", Blocking: true, File: "p.go", Line: 2},
			{Severity: state.SeverityMedium, Description: "architectural concern"},
		},
	}

	result := BuildInlineReview(verdict, diff)
	if result.Payload == nil || len(result.Payload.Comments) != 1 {
		t.Fatalf("expected exactly 1 inline comment, got %+v", result)
	}

	// When inlineGapIndices is passed, the inline gap should be excluded
	// from the criteria table but the non-inline gap should remain.
	summary := FormatVerdictComment(verdict, state.PhaseQuality, 1, result.InlineGapIndices)
	if contains(summary, "| High |") {
		t.Error("inline gap should NOT appear in criteria table when inlineGapIndices is set")
	}
	if !contains(summary, "architectural concern") {
		t.Error("summary must list the location-less gap that has no inline counterpart")
	}
	if !contains(summary, "1 additional comment(s) posted inline") {
		t.Error("summary should note inline comments were posted")
	}
}

func TestFormatInlineComment_Blocking(t *testing.T) {
	g := state.Gap{Severity: state.SeverityHigh, Description: "security issue", Blocking: true}
	body := formatInlineComment(g)
	if !contains(body, "[High]") {
		t.Errorf("expected [High] in body, got %q", body)
	}
	if !contains(body, "security issue") {
		t.Errorf("expected description in body, got %q", body)
	}
}

func TestFormatInlineComment_NonBlocking(t *testing.T) {
	g := state.Gap{Severity: state.SeverityLow, Description: "style nit", Blocking: false}
	body := formatInlineComment(g)
	if !contains(body, "[Low]") {
		t.Errorf("expected [Low] in body, got %q", body)
	}
}

func TestFormatInlineComment_WithSuggestion(t *testing.T) {
	g := state.Gap{
		Severity:    state.SeverityHigh,
		Description: "hardcoded key",
		Blocking:    true,
		Suggestion:  "\tapiKey := os.Getenv(\"API_KEY\")",
	}
	body := formatInlineComment(g)
	if !contains(body, "```suggestion") {
		t.Errorf("expected suggestion block, got %q", body)
	}
	if !contains(body, "os.Getenv") {
		t.Errorf("expected suggestion content, got %q", body)
	}
	if !contains(body, "[High]") {
		t.Errorf("expected severity prefix, got %q", body)
	}
	if !contains(body, "hardcoded key") {
		t.Errorf("expected description, got %q", body)
	}
}

func TestFormatInlineComment_NoSuggestion(t *testing.T) {
	g := state.Gap{
		Severity:    state.SeverityMedium,
		Description: "design concern",
		Blocking:    false,
		Suggestion:  "",
	}
	body := formatInlineComment(g)
	if contains(body, "suggestion") {
		t.Errorf("should not contain suggestion block when empty, got %q", body)
	}
}

func TestBuildInlineReview_SuggestionPreserved(t *testing.T) {
	diff := "diff --git a/foo.go b/foo.go\n" +
		"--- a/foo.go\n" +
		"+++ b/foo.go\n" +
		"@@ -1,3 +1,3 @@\n" +
		" package foo\n" +
		"-var old = 1\n" +
		"+var secret = \"sk-live-abc\"\n"

	verdict := &state.Verdict{
		Score: 80,
		Pass:  false,
		Gaps: []state.Gap{
			{
				Severity:    state.SeverityHigh,
				Description: "hardcoded secret",
				Blocking:    true,
				File:        "foo.go",
				Line:        2,
				Suggestion:  "var secret = os.Getenv(\"SECRET\")",
			},
		},
	}

	result := BuildInlineReview(verdict, diff)
	if result.Payload == nil {
		t.Fatal("expected review payload")
	}
	if len(result.Payload.Comments) != 1 {
		t.Fatalf("expected 1 inline comment, got %d", len(result.Payload.Comments))
	}
	if !contains(result.Payload.Comments[0].Body, "```suggestion") {
		t.Errorf("inline comment should contain suggestion block, got %q", result.Payload.Comments[0].Body)
	}
	if !contains(result.Payload.Comments[0].Body, "os.Getenv") {
		t.Errorf("suggestion should contain replacement code, got %q", result.Payload.Comments[0].Body)
	}
}

func TestFormatVerdictComment_GapWithFileLocation(t *testing.T) {
	// Gaps with file/line should show the location in the criteria table.
	verdict := &state.Verdict{
		Score: 70,
		Pass:  true,
		Gaps: []state.Gap{
			{Severity: state.SeverityMedium, Description: "magic number", Blocking: false, File: "foo.go", Line: 42},
			{Severity: state.SeverityLow, Description: "style nit", Blocking: false},
		},
	}

	comment := FormatVerdictComment(verdict, state.PhaseQuality, 1, nil)

	// Both gaps should appear in the table regardless of file/line.
	if !contains(comment, "magic number") {
		t.Error("expected gap description in comment")
	}
	if !contains(comment, "style nit") {
		t.Error("expected second gap description in comment")
	}
}

func TestSetCommitStatus_Success(t *testing.T) {
	runner := &FakeRunner{Responses: map[string]fakeResponse{"gh api": {Output: []byte("{}")}}}
	err := New(runner).SetCommitStatus(context.Background(), "abc123", "success", "vairdict/review", "Overridden by @alice")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Sanity check the gh args so a future refactor that drops the SHA or context name gets caught.
	var apiCall *FakeCall
	for i, call := range runner.Calls {
		if call.Name == "gh" && len(call.Args) > 0 && call.Args[0] == "api" {
			apiCall = &runner.Calls[i]
			break
		}
	}
	if apiCall == nil {
		t.Fatal("gh api was not called")
	}
	joined := ""
	for _, a := range apiCall.Args {
		joined += a + " "
	}
	if !contains(joined, "abc123") {
		t.Errorf("args missing SHA: %q", joined)
	}
	if !contains(joined, "state=success") {
		t.Errorf("args missing state: %q", joined)
	}
	if !contains(joined, "context=vairdict/review") {
		t.Errorf("args missing context: %q", joined)
	}
	if !contains(joined, "description=Overridden by @alice") {
		t.Errorf("args missing description: %q", joined)
	}
}

func TestSetCommitStatus_EmptySHA(t *testing.T) {
	runner := &FakeRunner{}
	err := New(runner).SetCommitStatus(context.Background(), "", "success", "ctx", "desc")
	if err == nil {
		t.Fatal("expected error for empty SHA")
	}
	for _, call := range runner.Calls {
		if call.Name == "gh" {
			t.Error("gh should not be invoked when SHA is empty")
		}
	}
}

func TestSetCommitStatus_GhError(t *testing.T) {
	runner := &FakeRunner{Responses: map[string]fakeResponse{"gh api": {Err: errors.New("403")}}}
	err := New(runner).SetCommitStatus(context.Background(), "abc", "success", "ctx", "desc")
	if err == nil {
		t.Fatal("expected error from failing gh call")
	}
}

func TestRecentCommentExists_True(t *testing.T) {
	runner := &FakeRunner{Responses: map[string]fakeResponse{"gh api": {Output: []byte("2\n")}}}
	ok, err := New(runner).RecentCommentExists(context.Background(), 1, "marker", 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !ok {
		t.Error("expected true when count > 0")
	}
}

func TestRecentCommentExists_False_ZeroCount(t *testing.T) {
	runner := &FakeRunner{Responses: map[string]fakeResponse{"gh api": {Output: []byte("0\n")}}}
	ok, err := New(runner).RecentCommentExists(context.Background(), 1, "marker", 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected false when count == 0")
	}
}

func TestRecentCommentExists_False_EmptyOutput(t *testing.T) {
	runner := &FakeRunner{Responses: map[string]fakeResponse{"gh api": {Output: []byte("")}}}
	ok, err := New(runner).RecentCommentExists(context.Background(), 1, "marker", 30*time.Second)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if ok {
		t.Error("expected false when output is empty")
	}
}

func TestRecentCommentExists_GhError(t *testing.T) {
	runner := &FakeRunner{Responses: map[string]fakeResponse{"gh api": {Err: errors.New("api down")}}}
	if _, err := New(runner).RecentCommentExists(context.Background(), 1, "marker", time.Second); err == nil {
		t.Fatal("expected error to propagate")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i+len(substr) <= len(s); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}

// --- Issue: dedupe bot review threads across pushes ---

// twoPageThreadResponse returns two pages of unresolved + viewer-authored
// threads + a couple noise threads (resolved, other-author) so the test
// asserts both filtering and pagination.
const firstPageThreadResponse = `{
  "data": {
    "viewer": {"login": "vairdict[bot]"},
    "repository": {"pullRequest": {"reviewThreads": {
      "pageInfo": {"hasNextPage": true, "endCursor": "CURSOR1"},
      "nodes": [
        {"id": "T1", "isResolved": false, "comments": {"nodes": [{"author": {"login": "vairdict[bot]"}}]}},
        {"id": "T2-resolved", "isResolved": true, "comments": {"nodes": [{"author": {"login": "vairdict[bot]"}}]}},
        {"id": "T3-otheruser", "isResolved": false, "comments": {"nodes": [{"author": {"login": "someone-else"}}]}}
      ]
    }}}
  }
}`

const secondPageThreadResponse = `{
  "data": {
    "viewer": {"login": "vairdict[bot]"},
    "repository": {"pullRequest": {"reviewThreads": {
      "pageInfo": {"hasNextPage": false, "endCursor": null},
      "nodes": [
        {"id": "T4", "isResolved": false, "comments": {"nodes": [{"author": {"login": "vairdict[bot]"}}]}}
      ]
    }}}
  }
}`

func TestListUnresolvedSelfThreadIDs_SkipsThreadsWithReplies(t *testing.T) {
	// A thread the bot started but a human replied to should NOT be
	// auto-resolved — that would hide ongoing discussion. Detected via
	// comments.totalCount > 1 on the GraphQL response.
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh repo": {Output: []byte("vairdict/vairdict\n")},
			"gh api graphql": {Output: []byte(`{
  "data": {
    "viewer": {"login": "vairdict[bot]"},
    "repository": {"pullRequest": {"reviewThreads": {
      "pageInfo": {"hasNextPage": false, "endCursor": null},
      "nodes": [
        {"id": "T-replied", "isResolved": false, "comments": {"totalCount": 2, "nodes": [{"author": {"login": "vairdict[bot]"}}]}},
        {"id": "T-solo",    "isResolved": false, "comments": {"totalCount": 1, "nodes": [{"author": {"login": "vairdict[bot]"}}]}}
      ]
    }}}
  }
}`)},
		},
	}
	client := New(runner)

	ids, err := client.listUnresolvedSelfThreadIDs(context.Background(), 138)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 1 || ids[0] != "T-solo" {
		t.Errorf("ids = %v, want [T-solo] (T-replied has a human reply and must be preserved)", ids)
	}
}

func TestListUnresolvedSelfThreadIDs_FiltersAndPaginates(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh repo": {Output: []byte("vairdict/vairdict\n")},
		},
		Sequence: map[string][]fakeResponse{
			"gh api graphql": {
				{Output: []byte(firstPageThreadResponse)},
				{Output: []byte(secondPageThreadResponse)},
			},
		},
	}
	client := New(runner)

	ids, err := client.listUnresolvedSelfThreadIDs(context.Background(), 138)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	want := []string{"T1", "T4"}
	if len(ids) != len(want) {
		t.Fatalf("ids = %v, want %v", ids, want)
	}
	for i, id := range want {
		if ids[i] != id {
			t.Errorf("ids[%d] = %q, want %q", i, ids[i], id)
		}
	}
	// Sanity: the second graphql call should pass the cursor from page 1.
	graphqlCalls := 0
	sawCursor := false
	for _, c := range runner.Calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "api" && c.Args[1] == "graphql" {
			graphqlCalls++
			for _, a := range c.Args {
				if a == "cursor=CURSOR1" {
					sawCursor = true
				}
			}
		}
	}
	if graphqlCalls != 2 {
		t.Errorf("expected 2 graphql calls (paginated), got %d", graphqlCalls)
	}
	if !sawCursor {
		t.Errorf("expected page 2 to pass cursor=CURSOR1, args were: %+v", runner.Calls)
	}
}

func TestListUnresolvedSelfThreadIDs_Empty(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh repo": {Output: []byte("vairdict/vairdict\n")},
			"gh api graphql": {Output: []byte(`{
  "data": {
    "viewer": {"login": "vairdict[bot]"},
    "repository": {"pullRequest": {"reviewThreads": {
      "pageInfo": {"hasNextPage": false, "endCursor": null},
      "nodes": []
    }}}
  }
}`)},
		},
	}
	client := New(runner)

	ids, err := client.listUnresolvedSelfThreadIDs(context.Background(), 138)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(ids) != 0 {
		t.Errorf("expected empty slice, got %v", ids)
	}
}

func TestListUnresolvedSelfThreadIDs_RepoLookupError(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh repo": {Err: errors.New("not in a git repo")},
		},
	}
	client := New(runner)

	_, err := client.listUnresolvedSelfThreadIDs(context.Background(), 138)
	if err == nil {
		t.Fatal("expected error when gh repo view fails")
	}
}

func TestListUnresolvedSelfThreadIDs_GraphqlError(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh repo":        {Output: []byte("vairdict/vairdict\n")},
			"gh api graphql": {Err: errors.New("graphql 500")},
		},
	}
	client := New(runner)

	_, err := client.listUnresolvedSelfThreadIDs(context.Background(), 138)
	if err == nil {
		t.Fatal("expected error when graphql query fails")
	}
}

func TestResolveReviewThread_SendsMutation(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh api graphql": {Output: []byte(`{"data":{"resolveReviewThread":{"thread":{"id":"T1"}}}}`)},
		},
	}
	client := New(runner)

	err := client.resolveReviewThread(context.Background(), "T1")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Must have called gh api graphql with the thread id parameter.
	found := false
	for _, c := range runner.Calls {
		if c.Name != "gh" || len(c.Args) < 2 || c.Args[0] != "api" || c.Args[1] != "graphql" {
			continue
		}
		for _, a := range c.Args {
			if a == "id=T1" {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected mutation arg id=T1, calls: %+v", runner.Calls)
	}
}

func TestResolveReviewThread_PropagatesError(t *testing.T) {
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh api graphql": {Err: errors.New("forbidden")},
		},
	}
	client := New(runner)

	err := client.resolveReviewThread(context.Background(), "T1")
	if err == nil {
		t.Fatal("expected error from gh api graphql failure")
	}
}

func TestResolveSelfReviewThreads_ResolvesEachListedID(t *testing.T) {
	// Single graphql list response with two viewer-authored threads,
	// followed by two graphql mutation responses. Sequence drives the
	// list-then-resolve order on the same key.
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh repo": {Output: []byte("vairdict/vairdict\n")},
		},
		Sequence: map[string][]fakeResponse{
			"gh api graphql": {
				{Output: []byte(`{
  "data": {
    "viewer": {"login": "vairdict[bot]"},
    "repository": {"pullRequest": {"reviewThreads": {
      "pageInfo": {"hasNextPage": false, "endCursor": null},
      "nodes": [
        {"id": "T1", "isResolved": false, "comments": {"nodes": [{"author": {"login": "vairdict[bot]"}}]}},
        {"id": "T2", "isResolved": false, "comments": {"nodes": [{"author": {"login": "vairdict[bot]"}}]}}
      ]
    }}}
  }
}`)},
				{Output: []byte(`{"data":{"resolveReviewThread":{"thread":{"id":"T1"}}}}`)},
				{Output: []byte(`{"data":{"resolveReviewThread":{"thread":{"id":"T2"}}}}`)},
			},
		},
	}
	client := New(runner)

	client.resolveSelfReviewThreads(context.Background(), 138)

	// Should have called: 1 list + 2 resolve = 3 graphql calls.
	graphqlCalls := 0
	resolveIDs := []string{}
	for _, c := range runner.Calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "api" && c.Args[1] == "graphql" {
			graphqlCalls++
			for _, a := range c.Args {
				if strings.HasPrefix(a, "id=") {
					resolveIDs = append(resolveIDs, strings.TrimPrefix(a, "id="))
				}
			}
		}
	}
	if graphqlCalls != 3 {
		t.Errorf("expected 1 list + 2 resolve = 3 graphql calls, got %d", graphqlCalls)
	}
	if len(resolveIDs) != 2 || resolveIDs[0] != "T1" || resolveIDs[1] != "T2" {
		t.Errorf("expected resolve(T1) then resolve(T2), got %v", resolveIDs)
	}
}

func TestResolveSelfReviewThreads_ListErrorIsSwallowed(t *testing.T) {
	// Best-effort: a list error must NOT block subsequent posting flow.
	// We assert by simply checking the call returns without panicking
	// and no resolve mutation runs.
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"gh repo": {Err: errors.New("not in a git repo")},
		},
	}
	client := New(runner)

	// Should not panic, should not return.
	client.resolveSelfReviewThreads(context.Background(), 138)

	for _, c := range runner.Calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "api" && c.Args[1] == "graphql" {
			t.Errorf("unexpected graphql call after list error: %+v", c)
		}
	}
}

func TestPostVerdictWithDiff_ResolvesPriorThreadsBeforePosting(t *testing.T) {
	// End-to-end check: the post path resolves stale viewer-authored
	// threads BEFORE posting a new review so users only see the latest
	// review's findings.
	diff := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,3 +1,4 @@
 a
 b
+c
 d
`
	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"git rev-parse": {Output: []byte(".git")},
			"git remote":    {Output: []byte("https://github.com/vairdict/vairdict")},
			"gh auth":       {Output: []byte("ok")},
			"gh pr review":  {Output: []byte("ok")},
			"gh pr comment": {Output: []byte("ok")},
			"gh repo":       {Output: []byte("vairdict/vairdict\n")},
		},
		Sequence: map[string][]fakeResponse{
			// First graphql call = list. Returns one stale unresolved thread.
			// Second graphql call = the resolveReviewThread mutation for T-stale.
			"gh api graphql": {
				{Output: []byte(`{
  "data": {
    "viewer": {"login": "vairdict[bot]"},
    "repository": {"pullRequest": {"reviewThreads": {
      "pageInfo": {"hasNextPage": false, "endCursor": null},
      "nodes": [
        {"id": "T-stale", "isResolved": false, "comments": {"nodes": [{"author": {"login": "vairdict[bot]"}}]}}
      ]
    }}}
  }
}`)},
				{Output: []byte(`{"data":{"resolveReviewThread":{"thread":{"id":"T-stale"}}}}`)},
			},
		},
	}
	client := New(runner)

	verdict := &state.Verdict{
		Score: 80,
		Pass:  true,
		// Critical so it's inline-eligible under the new dispatch and
		// the review POST path is exercised. (Pre-dispatch this test
		// used a Medium gap, but only Critical/High/Standards reach the
		// inline surface now — see TestBuildInlineReview_Dispatch...)
		Gaps: []state.Gap{
			{Severity: state.SeverityCritical, Description: "blocking issue", Blocking: true, File: "foo.go", Line: 3},
		},
	}

	if err := client.PostVerdictWithDiff(context.Background(), 7, verdict, state.PhaseQuality, 1, diff); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Find the index of the resolveReviewThread call (id=T-stale) and the
	// review-post call (gh api .../reviews). Resolve must happen before post.
	resolveIdx, postIdx := -1, -1
	for i, c := range runner.Calls {
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "api" && c.Args[1] == "graphql" {
			for _, a := range c.Args {
				if a == "id=T-stale" {
					resolveIdx = i
				}
			}
		}
		if c.Name == "gh" && len(c.Args) >= 2 && c.Args[0] == "api" {
			for _, a := range c.Args {
				if strings.Contains(a, "/reviews") {
					postIdx = i
				}
			}
		}
	}
	if resolveIdx < 0 {
		t.Fatalf("expected a resolveReviewThread mutation; calls: %+v", runner.Calls)
	}
	if postIdx < 0 {
		t.Fatalf("expected a review POST call; calls: %+v", runner.Calls)
	}
	if resolveIdx >= postIdx {
		t.Errorf("resolveReviewThread (idx %d) must run BEFORE review POST (idx %d)", resolveIdx, postIdx)
	}
}

// TestBuildInlineReview_DispatchByInlineEligibility encodes the new
// dispatch rule: only Critical and High gaps surface as inline review
// comments. Medium and Low go into the unanchored review body so they
// stop cluttering the PR diff with non-blocking nits the author can't
// apply with one click — the user-facing complaint that "every PR
// gets 2-3 inline comments of varying noise."
//
// Fixture: one diff position; one gap of each tier all anchored at
// that line. The Critical and High gaps must end up as Comments;
// Medium and Low must end up in the unanchored body — even though
// they have a valid file/line that would have made them inline-
// eligible under the old "any gap with file:line goes inline" rule.
func TestBuildInlineReview_DispatchByInlineEligibility(t *testing.T) {
	diff := "diff --git a/x.go b/x.go\n" +
		"--- a/x.go\n" +
		"+++ b/x.go\n" +
		"@@ -1,3 +1,4 @@\n" +
		" package x\n" +
		"+var added = 1\n" +
		" var existing = 2\n"
	verdict := &state.Verdict{
		Gaps: []state.Gap{
			{Severity: state.SeverityCritical, Description: "crit", Blocking: true, File: "x.go", Line: 2},
			{Severity: state.SeverityHigh, Description: "high", Blocking: true, File: "x.go", Line: 2},
			{Severity: state.SeverityMedium, Description: "med", Blocking: false, File: "x.go", Line: 2},
			{Severity: state.SeverityLow, Description: "low", Blocking: false, File: "x.go", Line: 2},
		},
	}

	got := BuildInlineReview(verdict, diff)
	if got == nil || got.Payload == nil {
		t.Fatalf("expected payload, got %+v", got)
	}
	if len(got.Payload.Comments) != 2 {
		t.Errorf("expected 2 inline comments (Critical+High), got %d: %+v",
			len(got.Payload.Comments), got.Payload.Comments)
	}
	bodies := []string{}
	for _, c := range got.Payload.Comments {
		bodies = append(bodies, c.Body)
	}
	joined := strings.Join(bodies, "\n")
	if !contains(joined, "crit") || !contains(joined, "high") {
		t.Errorf("inline bodies should include Critical+High, got: %s", joined)
	}
	if contains(joined, "[Medium]") || contains(joined, "[Low]") {
		t.Errorf("inline bodies must NOT include Medium/Low, got: %s", joined)
	}
	// Medium and Low surface in the review body so the concern is not lost.
	if !contains(got.Payload.Body, "med") || !contains(got.Payload.Body, "low") {
		t.Errorf("Medium/Low must surface in review body, got: %s", got.Payload.Body)
	}

	// inlineIndices reflects only the Critical and High gaps, so the
	// summary-comment renderer keeps Medium/Low in its criteria table.
	if got.InlineGapIndices[2] || got.InlineGapIndices[3] {
		t.Errorf("Medium (idx 2) and Low (idx 3) must not be marked inline: %+v",
			got.InlineGapIndices)
	}
	if !got.InlineGapIndices[0] || !got.InlineGapIndices[1] {
		t.Errorf("Critical (idx 0) and High (idx 1) must be marked inline: %+v",
			got.InlineGapIndices)
	}
}

// TestBuildInlineReview_StandardsAlwaysInline pins that any gap whose
// description carries the Standards marker (added by the post-
// processor for FilterFindings output) is inline regardless of the
// severity ladder — Standards finding are mechanical/easy-fix and
// the user explicitly asked for them to surface inline.
//
// This test goes red until BuildInlineReview learns the marker and
// also flips inline eligibility on it. Standards findings travel as
// state.Gap with Severity left empty (or set to a sentinel) and a
// description prefixed with the standards marker.
func TestBuildInlineReview_StandardsAlwaysInline(t *testing.T) {
	diff := "diff --git a/x.go b/x.go\n" +
		"--- a/x.go\n" +
		"+++ b/x.go\n" +
		"@@ -1,3 +1,4 @@\n" +
		" package x\n" +
		"+var addedNoCamelCase = 1\n" +
		" var existing = 2\n"
	verdict := &state.Verdict{
		Gaps: []state.Gap{
			{
				Severity:    state.Severity(""),
				Description: "STANDARDS:naming use camelCase",
				Blocking:    false,
				File:        "x.go", Line: 2,
				Suggestion: "var addedNoCamelCase = 1",
			},
		},
	}
	got := BuildInlineReview(verdict, diff)
	if got == nil || got.Payload == nil || len(got.Payload.Comments) != 1 {
		t.Fatalf("expected 1 inline Standards comment, got %+v", got)
	}
}

// --- AC checklist rendering tests ---

func TestFormatVerdictComment_AC_AllPassed(t *testing.T) {
	v := &state.Verdict{
		Pass: true, Score: 100,
		Checklist: []state.ChecklistItem{
			{Name: "ac_1", Description: "Add codex completer", Required: true, Passed: true, Reason: "internal/agents/codex/client.go:127"},
			{Name: "ac_2", Description: "Wire into resolver", Required: true, Passed: true, Reason: "cmd/vairdict/completer.go:160"},
		},
	}
	got := FormatVerdictComment(v, state.PhaseQuality, 1, nil)
	if !strings.Contains(got, "Acceptance Criteria (2/2 passed)") {
		t.Errorf("missing tally header\n%s", got)
	}
	for _, want := range []string{
		"✅", "Add codex completer", "internal/agents/codex/client.go:127",
		"Wire into resolver", "cmd/vairdict/completer.go:160",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q\n%s", want, got)
		}
	}
}

func TestFormatVerdictComment_AC_DeferredAndUnmet(t *testing.T) {
	v := &state.Verdict{
		Pass: false, Score: 60,
		Checklist: []state.ChecklistItem{
			{Name: "ac_1", Description: "first done", Required: true, Passed: true, Reason: "file:1"},
			{Name: "ac_2", Description: "deferred item", Required: true, Passed: false, Reason: "blocked on #130"},
			{Name: "ac_3", Description: "missed", Required: true, Passed: false},
		},
	}
	got := FormatVerdictComment(v, state.PhaseQuality, 1, nil)
	// Tally counts only passed (Passed=true) — deferred and missed
	// don't count.
	if !strings.Contains(got, "Acceptance Criteria (1/3 passed)") {
		t.Errorf("tally wrong\n%s", got)
	}
	if !strings.Contains(got, "⏸️") {
		t.Error("missing ⏸️ for deferred-with-reason item")
	}
	if !strings.Contains(got, "❌") {
		t.Error("missing ❌ for unmet-no-reason item")
	}
	if !strings.Contains(got, "blocked on #130") {
		t.Error("missing deferral reason text")
	}
	if !strings.Contains(got, "_(no reason given)_") {
		t.Error("missing placeholder for missing reason")
	}
}

func TestFormatVerdictComment_AC_OptionalItem(t *testing.T) {
	v := &state.Verdict{
		Pass: true, Score: 100,
		Checklist: []state.ChecklistItem{
			{Name: "ac_1", Description: "required pass", Required: true, Passed: true, Reason: "f:1"},
			{Name: "opt_1", Description: "optional, unticked", Required: false, Passed: false},
		},
	}
	got := FormatVerdictComment(v, state.PhaseQuality, 1, nil)
	if !strings.Contains(got, "⚪") {
		t.Errorf("missing ⚪ for optional unticked item\n%s", got)
	}
	if !strings.Contains(got, "(optional)") {
		t.Errorf("missing (optional) prefix on optional item\n%s", got)
	}
}

func TestFormatVerdictComment_AC_OmittedWhenChecklistEmpty(t *testing.T) {
	// Legacy verdict (no AC list) must not emit an empty AC section.
	v := &state.Verdict{Pass: true, Score: 100}
	got := FormatVerdictComment(v, state.PhaseQuality, 1, nil)
	if strings.Contains(got, "Acceptance Criteria") {
		t.Errorf("AC section must not appear when checklist is empty\n%s", got)
	}
}

func TestFormatVerdictComment_AC_EscapesPipesInDescription(t *testing.T) {
	// Markdown table cells break if a literal | leaks through. The
	// renderer must escape pipes in both Description and Reason.
	v := &state.Verdict{
		Pass: true, Score: 100,
		Checklist: []state.ChecklistItem{
			{Name: "ac_1", Description: "match foo|bar pattern", Required: true, Passed: true, Reason: "see grep|sed pipeline"},
		},
	}
	got := FormatVerdictComment(v, state.PhaseQuality, 1, nil)
	if !strings.Contains(got, `match foo\|bar pattern`) {
		t.Errorf("pipe in description not escaped\n%s", got)
	}
	if !strings.Contains(got, `see grep\|sed pipeline`) {
		t.Errorf("pipe in reason not escaped\n%s", got)
	}
}
