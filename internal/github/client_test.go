package github

import (
	"context"
	"errors"
	"testing"

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
			{Severity: state.SeverityP3, Description: "minor style", Blocking: false},
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

func TestPostVerdict_Pass_OtherApprovalError_Propagates(t *testing.T) {
	runner := successRunner()
	runner.Responses["gh pr review"] = fakeResponse{Err: errors.New("network error")}
	client := New(runner)

	verdict := &state.Verdict{Score: 92, Pass: true}
	err := client.PostVerdict(context.Background(), 7, verdict, state.PhaseQuality, 1)
	if err == nil {
		t.Fatal("expected non-self-PR approval errors to propagate")
	}
}

func TestPostVerdict_Fail_CommentsOnly(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	verdict := &state.Verdict{
		Score: 40,
		Pass:  false,
		Gaps: []state.Gap{
			{Severity: state.SeverityP0, Description: "build fails", Blocking: true},
			{Severity: state.SeverityP1, Description: "tests fail", Blocking: true},
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
	if !errors.Is(err, err) {
		t.Errorf("error not wrapped properly: %v", err)
	}
}

func TestFormatVerdictComment_Pass(t *testing.T) {
	verdict := &state.Verdict{
		Score: 95,
		Pass:  true,
		Gaps: []state.Gap{
			{Severity: state.SeverityP3, Description: "minor style nit", Blocking: false},
		},
	}

	comment := FormatVerdictComment(verdict, state.PhaseQuality, 1)

	checks := []struct {
		name string
		want string
	}{
		{"header", "VAIrdict Verdict: ✅ PASS</h2>"},
		{"logo", `<img src="`},
		{"logo alt", `alt="VAIrdict"`},
		{"logo height", `height="24"`},
		{"score", "**Score:** 95%"},
		{"phase", "**Phase:** quality"},
		{"loop", "**Loop:** 1"},
		{"gap severity", "| P3 |"},
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
			{Severity: state.SeverityP0, Description: "build broken", Blocking: true},
			{Severity: state.SeverityP1, Description: "tests fail", Blocking: true},
			{Severity: state.SeverityP3, Description: "docs missing", Blocking: false},
		},
		Questions: []state.Question{
			{Text: "Is the API stable?", Priority: "high"},
		},
	}

	comment := FormatVerdictComment(verdict, state.PhaseCode, 2)

	checks := []struct {
		name string
		want string
	}{
		{"header", "VAIrdict Verdict: ❌ FAIL</h2>"},
		{"logo", `<img src="`},
		{"logo alt", `alt="VAIrdict"`},
		{"logo height", `height="24"`},
		{"score", "**Score:** 40%"},
		{"loop", "**Loop:** 2"},
		{"blocking section", "### Blocking Gaps"},
		{"P0 gap", "**[P0]** build broken"},
		{"P1 gap", "**[P1]** tests fail"},
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

	comment := FormatVerdictComment(verdict, state.PhasePlan, 1)

	if contains(comment, "### Criteria") {
		t.Error("should not have criteria table when no gaps")
	}
	if !contains(comment, "PASS") {
		t.Error("should contain PASS")
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
			{Severity: state.SeverityP2, Description: "assumed Y"},
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
		body string
		want int
	}{
		{"Closes #42", 42},
		{"closes #42", 42},
		{"Fixes #7\n\nlots of context", 7},
		{"Resolves #123 — done", 123},
		{"fixed #5", 5},
		{"resolved #5", 5},
		{"some text Closes: #99", 99},
		{"## Issue\nCloses #48\n", 48},
		{"no linked issue here", 0},
		{"#42 alone is not enough", 0},
		{"see #42 for context", 0},
		{"", 0},
	}
	for _, tc := range cases {
		if got := ParseLinkedIssue(tc.body); got != tc.want {
			t.Errorf("ParseLinkedIssue(%q) = %d, want %d", tc.body, got, tc.want)
		}
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
