package main

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/vairdict/vairdict/internal/github"
)

func TestParseComment(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name       string
		body       string
		mentioned  bool
		raw        string
		command    commentCommand
		didYouMean commentCommand
	}{
		{"empty", "", false, "", cmdNone, cmdNone},
		{"no_mention", "looks good to me", false, "", cmdNone, cmdNone},
		{"unrelated_mention", "cc @other", false, "", cmdNone, cmdNone},
		{"review", "@vairdict review", true, "review", cmdReview, cmdNone},
		{"approve", "@vairdict approve please", true, "approve", cmdApprove, cmdNone},
		{"ignore", "hey @vairdict ignore", true, "ignore", cmdIgnore, cmdNone},
		{"case_insensitive", "@VAIrdict Review", true, "review", cmdReview, cmdNone},
		{"punctuation_stripped", "@vairdict review.", true, "review", cmdReview, cmdNone},
		{"mention_only", "@vairdict", true, "", cmdNone, cmdNone},
		{"unknown_command_typo_review", "@vairdict revew", true, "revew", cmdNone, cmdReview},
		{"unknown_command_typo_approve", "@vairdict approv", true, "approv", cmdNone, cmdApprove},
		{"unknown_far", "@vairdict merge-it", true, "merge-it", cmdNone, cmdNone},
		{"not_part_of_longer_handle", "@vairdict-bot please review", false, "", cmdNone, cmdNone},
		{"newline_after_mention", "@vairdict\nreview", true, "review", cmdReview, cmdNone},
		{"leading_text", "please run @vairdict review on this", true, "review", cmdReview, cmdNone},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseComment(tc.body)
			if got.Mentioned != tc.mentioned {
				t.Errorf("Mentioned = %v, want %v", got.Mentioned, tc.mentioned)
			}
			if got.Raw != tc.raw {
				t.Errorf("Raw = %q, want %q", got.Raw, tc.raw)
			}
			if got.Command != tc.command {
				t.Errorf("Command = %q, want %q", got.Command, tc.command)
			}
			if got.DidYouMean != tc.didYouMean {
				t.Errorf("DidYouMean = %q, want %q", got.DidYouMean, tc.didYouMean)
			}
		})
	}
}

func TestAuthorized(t *testing.T) {
	t.Parallel()
	cases := []struct {
		assoc string
		want  bool
	}{
		{"OWNER", true},
		{"MEMBER", true},
		{"COLLABORATOR", true},
		{"owner", true},
		{"  MEMBER ", true},
		{"CONTRIBUTOR", false},
		{"FIRST_TIME_CONTRIBUTOR", false},
		{"NONE", false},
		{"", false},
	}
	for _, tc := range cases {
		t.Run(tc.assoc, func(t *testing.T) {
			if got := authorized(tc.assoc); got != tc.want {
				t.Errorf("authorized(%q) = %v, want %v", tc.assoc, got, tc.want)
			}
		})
	}
}

// fakeHandleGH records every call on the handleCommentGH surface so
// tests can assert handler side-effects without touching network.
type fakeHandleGH struct {
	comments          []string
	pr                *github.PRDetails
	prErr             error
	commentErr        error
	statusSHA         string
	statusState       string
	statusContext     string
	statusDescription string
	statusErr         error
	recent            bool
	recentErr         error
	recentCalls       int
	recentMarkers     []string // tracks which markers were checked
	reactions         []string
	reactionErr       error
	diff              string
	diffErr           error
}

func (f *fakeHandleGH) AddComment(_ context.Context, _ int, body string) error {
	f.comments = append(f.comments, body)
	return f.commentErr
}

func (f *fakeHandleGH) AddReaction(_ context.Context, _ int64, content string) error {
	f.reactions = append(f.reactions, content)
	return f.reactionErr
}

func (f *fakeHandleGH) FetchPR(_ context.Context, _ int) (*github.PRDetails, error) {
	return f.pr, f.prErr
}

func (f *fakeHandleGH) FetchPRDiff(_ context.Context, _ int) (string, error) {
	return f.diff, f.diffErr
}

func (f *fakeHandleGH) SetCommitStatus(_ context.Context, sha, state, statusContext, description string) error {
	f.statusSHA = sha
	f.statusState = state
	f.statusContext = statusContext
	f.statusDescription = description
	return f.statusErr
}

func (f *fakeHandleGH) RecentCommentExists(_ context.Context, _ int, marker string, _ time.Duration) (bool, error) {
	f.recentCalls++
	f.recentMarkers = append(f.recentMarkers, marker)
	return f.recent, f.recentErr
}

func baseHandleDeps(gh handleCommentGH) handleCommentDeps {
	return handleCommentDeps{
		gh:         gh,
		stdout:     io.Discard,
		runReview:  func(int) error { return nil },
		rateWindow: 30 * time.Second,
	}
}

func TestRunHandleComment_NoMention_NoOp(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "just a regular comment"
	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.comments) != 0 {
		t.Errorf("no reply expected, got %d comments", len(gh.comments))
	}
}

func TestRunHandleComment_UnknownCommand_Replies(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict revew"
	deps.assoc = "MEMBER"
	if err := runHandleCommentWith(context.Background(), 7, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "Did you mean `@vairdict review`") {
		t.Errorf("expected did-you-mean suggestion, got %q", gh.comments[0])
	}
}

func TestRunHandleComment_MentionOnly_HelpReply(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict"
	if err := runHandleCommentWith(context.Background(), 7, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 reply, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "review") || !strings.Contains(gh.comments[0], "approve") {
		t.Errorf("expected help listing commands, got %q", gh.comments[0])
	}
}

func TestRunHandleComment_Unauthorized_Replies_NoAction(t *testing.T) {
	t.Parallel()
	cases := []string{"CONTRIBUTOR", "FIRST_TIME_CONTRIBUTOR", "NONE", ""}
	for _, assoc := range cases {
		t.Run(assoc, func(t *testing.T) {
			gh := &fakeHandleGH{}
			deps := baseHandleDeps(gh)
			deps.body = "@vairdict review"
			deps.author = "mallory"
			deps.assoc = assoc
			// runReview must NOT be called.
			deps.runReview = func(int) error {
				t.Fatal("runReview should not be called for unauthorized commenter")
				return nil
			}
			if err := runHandleCommentWith(context.Background(), 3, deps); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if len(gh.comments) != 1 {
				t.Fatalf("expected 1 denial comment, got %d", len(gh.comments))
			}
			if !strings.Contains(gh.comments[0], "only available to repository owners") {
				t.Errorf("expected denial message, got %q", gh.comments[0])
			}
		})
	}
}

func TestRunHandleComment_Authorized_AllAssocs(t *testing.T) {
	t.Parallel()
	for _, assoc := range []string{"OWNER", "MEMBER", "COLLABORATOR"} {
		t.Run(assoc, func(t *testing.T) {
			gh := &fakeHandleGH{}
			deps := baseHandleDeps(gh)
			deps.body = "@vairdict review"
			deps.author = "alice"
			deps.assoc = assoc
			called := false
			deps.runReview = func(int) error { called = true; return nil }
			if err := runHandleCommentWith(context.Background(), 10, deps); err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if !called {
				t.Error("expected runReview to be called for authorized user")
			}
		})
	}
}

func TestRunHandleComment_Review_PostsStartMarker_CallsReview(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict review"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	var reviewedPR int
	deps.runReview = func(n int) error { reviewedPR = n; return nil }

	if err := runHandleCommentWith(context.Background(), 42, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reviewedPR != 42 {
		t.Errorf("runReview called with PR %d, want 42", reviewedPR)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 start comment, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], reviewStartMarker) {
		t.Errorf("expected start marker in comment, got %q", gh.comments[0])
	}
	if !strings.Contains(gh.comments[0], "@alice") {
		t.Errorf("expected commenter attribution, got %q", gh.comments[0])
	}
}

func TestRunHandleComment_Review_RateLimited_ShortCircuits(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{recent: true}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict review"
	deps.author = "alice"
	deps.assoc = "OWNER"
	reviewCalled := false
	deps.runReview = func(int) error { reviewCalled = true; return nil }

	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if reviewCalled {
		t.Error("runReview should NOT be called when rate-limited")
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 rate-limit reply, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "already running") {
		t.Errorf("expected 'already running' message, got %q", gh.comments[0])
	}
}

func TestRunHandleComment_Review_RateLimitErrorContinues(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{recentErr: errors.New("gh api down")}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict review"
	deps.author = "alice"
	deps.assoc = "OWNER"
	called := false
	deps.runReview = func(int) error { called = true; return nil }

	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !called {
		t.Error("expected review to run even when rate-limit check fails")
	}
}

func TestRunHandleComment_Review_PropagatesReviewError(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict review"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.runReview = func(int) error { return errors.New("judge blew up") }

	err := runHandleCommentWith(context.Background(), 1, deps)
	if err == nil {
		t.Fatal("expected error to propagate")
	}
	if !strings.Contains(err.Error(), "re-running review") {
		t.Errorf("error should mention re-running review, got: %v", err)
	}
}

func TestRunHandleComment_Approve_SetsStatus_PostsOverride(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{
		pr: &github.PRDetails{Number: 17, HeadRefOid: "deadbeef1234567890"},
	}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict approve"
	deps.author = "alice"
	deps.assoc = "MEMBER"

	if err := runHandleCommentWith(context.Background(), 17, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gh.statusSHA != "deadbeef1234567890" {
		t.Errorf("status SHA = %q, want deadbeef1234567890", gh.statusSHA)
	}
	if gh.statusState != "success" {
		t.Errorf("status state = %q, want success", gh.statusState)
	}
	if gh.statusContext != github.CommitStatusContext {
		t.Errorf("status context = %q, want %q", gh.statusContext, github.CommitStatusContext)
	}
	if !strings.Contains(gh.statusDescription, "Overridden by @alice") {
		t.Errorf("status description missing override attribution, got %q", gh.statusDescription)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 confirmation comment, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], overrideMarker) {
		t.Errorf("expected override marker in comment, got %q", gh.comments[0])
	}
	if !strings.Contains(gh.comments[0], "@alice") {
		t.Errorf("expected commenter attribution, got %q", gh.comments[0])
	}
	if !strings.Contains(gh.comments[0], "deadbee") {
		t.Errorf("expected short sha in comment, got %q", gh.comments[0])
	}
	if !strings.Contains(gh.comments[0], "does not merge") {
		t.Errorf("expected no-merge disclaimer, got %q", gh.comments[0])
	}
}

func TestRunHandleComment_Approve_NoHeadSHA_Errors(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{pr: &github.PRDetails{Number: 1}}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict approve"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	err := runHandleCommentWith(context.Background(), 1, deps)
	if err == nil {
		t.Fatal("expected error when head SHA is empty")
	}
}

func TestRunHandleComment_Approve_StatusErrorPropagates(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{
		pr:        &github.PRDetails{Number: 1, HeadRefOid: "abc123"},
		statusErr: errors.New("api down"),
	}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict approve"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	err := runHandleCommentWith(context.Background(), 1, deps)
	if err == nil {
		t.Fatal("expected error to propagate")
	}
}

func TestRunHandleComment_Ignore_DismissesStatus(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{
		pr: &github.PRDetails{Number: 5, HeadRefOid: "cafef00d"},
	}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict ignore"
	deps.author = "bob"
	deps.assoc = "COLLABORATOR"

	if err := runHandleCommentWith(context.Background(), 5, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gh.statusState != "success" {
		t.Errorf("status state = %q, want success", gh.statusState)
	}
	if !strings.Contains(gh.statusDescription, "Dismissed by @bob") {
		t.Errorf("status description = %q, want dismissal attribution", gh.statusDescription)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 confirmation comment, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], ignoreMarker) {
		t.Errorf("expected ignore marker in comment, got %q", gh.comments[0])
	}
	if !strings.Contains(gh.comments[0], "next push") {
		t.Errorf("expected next-push disclaimer, got %q", gh.comments[0])
	}
}

func TestRunHandleComment_Ignore_FetchPRError(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{prErr: errors.New("not found")}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict ignore"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	err := runHandleCommentWith(context.Background(), 99, deps)
	if err == nil {
		t.Fatal("expected error when fetching PR fails")
	}
}

func TestRunHandleComment_EyesReaction_OnMention(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict review"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.commentID = 12345
	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.reactions) != 1 || gh.reactions[0] != "eyes" {
		t.Errorf("expected [eyes] reaction, got %v", gh.reactions)
	}
}

func TestRunHandleComment_NoReaction_WithoutCommentID(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict review"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	// commentID is 0 (not set)
	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.reactions) != 0 {
		t.Errorf("expected no reactions without comment ID, got %v", gh.reactions)
	}
}

// --- Parser tests for new commands ---

func TestParseComment_Explain(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		body    string
		command commentCommand
		args    string
	}{
		{"explain_with_question", "@vairdict explain why was this changed?", cmdExplain, "why was this changed?"},
		{"explain_no_args", "@vairdict explain", cmdExplain, ""},
		{"explain_multiword", "@vairdict explain what does the new function do and why", cmdExplain, "what does the new function do and why"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseComment(tc.body)
			if !got.Mentioned {
				t.Fatal("expected Mentioned=true")
			}
			if got.Command != tc.command {
				t.Errorf("Command = %q, want %q", got.Command, tc.command)
			}
			if got.Args != tc.args {
				t.Errorf("Args = %q, want %q", got.Args, tc.args)
			}
		})
	}
}

func TestParseComment_Fix(t *testing.T) {
	t.Parallel()
	got := parseComment("@vairdict fix add nil check before dereferencing config")
	if got.Command != cmdFix {
		t.Fatalf("Command = %q, want fix", got.Command)
	}
	if got.Args != "add nil check before dereferencing config" {
		t.Errorf("Args = %q", got.Args)
	}
}

func TestParseComment_Run(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		body string
		args string
	}{
		{"quoted", `@vairdict run "add input validation"`, "add input validation"},
		{"single_quoted", `@vairdict run 'fix the bug'`, "fix the bug"},
		{"unquoted", `@vairdict run add tests`, "add tests"},
		{"no_args", "@vairdict run", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := parseComment(tc.body)
			if got.Command != cmdRun {
				t.Fatalf("Command = %q, want run", got.Command)
			}
			if got.Args != tc.args {
				t.Errorf("Args = %q, want %q", got.Args, tc.args)
			}
		})
	}
}

func TestStripQuotes(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in, want string
	}{
		{`"hello"`, "hello"},
		{`'hello'`, "hello"},
		{`hello`, "hello"},
		{`"`, `"`},
		{`""`, ""},
		{`"mismatched'`, `"mismatched'`},
	}
	for _, tc := range cases {
		t.Run(tc.in, func(t *testing.T) {
			if got := stripQuotes(tc.in); got != tc.want {
				t.Errorf("stripQuotes(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// --- Explain handler tests ---

func TestHandleComment_Explain_NoArgs_PostsHelp(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict explain"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "Please provide a question") {
		t.Errorf("expected help message, got %q", gh.comments[0])
	}
}

func TestHandleComment_Explain_Success(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{diff: "diff --git a/foo.go b/foo.go\n+added line"}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict explain why was this added?"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.explainQuestion = func(_ context.Context, diff, question string) (string, error) {
		if !strings.Contains(diff, "foo.go") {
			t.Error("expected diff to be passed")
		}
		if question != "why was this added?" {
			t.Errorf("question = %q", question)
		}
		return "Because it fixes a bug.", nil
	}
	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "Because it fixes a bug.") {
		t.Errorf("expected answer in comment, got %q", gh.comments[0])
	}
	if !strings.Contains(gh.comments[0], explainMarker) {
		t.Errorf("expected explain marker, got %q", gh.comments[0])
	}
}

func TestHandleComment_Explain_LLMError_PostsErrorReply(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{diff: "some diff"}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict explain what happened?"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.explainQuestion = func(context.Context, string, string) (string, error) {
		return "", errors.New("LLM down")
	}
	err := runHandleCommentWith(context.Background(), 1, deps)
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "running explain") {
		t.Errorf("error = %v", err)
	}
	// Should still post an error comment.
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 error comment, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "couldn't answer") {
		t.Errorf("expected error in comment, got %q", gh.comments[0])
	}
}

// --- Fix handler tests ---

func TestHandleComment_Fix_NoArgs_PostsHelp(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict fix"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "Please describe the fix") {
		t.Errorf("expected help message, got %q", gh.comments[0])
	}
}

func TestHandleComment_Fix_Success(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{
		pr:   &github.PRDetails{Number: 5, HeadRefName: "fix-branch", HeadRefOid: "abc"},
		diff: "diff content",
	}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict fix add nil check"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.fixCode = func(_ context.Context, pr *github.PRDetails, desc, diff string) (string, error) {
		if pr.HeadRefName != "fix-branch" {
			t.Errorf("branch = %q", pr.HeadRefName)
		}
		if desc != "add nil check" {
			t.Errorf("desc = %q", desc)
		}
		return "deadbeef1234567890", nil
	}
	if err := runHandleCommentWith(context.Background(), 5, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Expect 2 comments: start + confirmation.
	if len(gh.comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "Working on fix") {
		t.Errorf("expected start comment, got %q", gh.comments[0])
	}
	if !strings.Contains(gh.comments[1], "deadbee") {
		t.Errorf("expected short SHA in confirmation, got %q", gh.comments[1])
	}
	if !strings.Contains(gh.comments[1], fixMarker) {
		t.Errorf("expected fix marker, got %q", gh.comments[1])
	}
}

func TestHandleComment_Fix_Error_PostsFailure(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{
		pr:   &github.PRDetails{Number: 5, HeadRefName: "fix-branch"},
		diff: "diff",
	}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict fix something"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.fixCode = func(context.Context, *github.PRDetails, string, string) (string, error) {
		return "", errors.New("no changes produced")
	}
	err := runHandleCommentWith(context.Background(), 5, deps)
	if err == nil {
		t.Fatal("expected error")
	}
	// Expect 2 comments: start + error.
	if len(gh.comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[1], "Fix failed") {
		t.Errorf("expected failure comment, got %q", gh.comments[1])
	}
}

// --- Run handler tests ---

func TestHandleComment_Run_NoArgs_PostsHelp(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = "@vairdict run"
	deps.author = "alice"
	deps.assoc = "MEMBER"
	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(gh.comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], "Please provide an intent") {
		t.Errorf("expected help message, got %q", gh.comments[0])
	}
}

func TestHandleComment_Run_Success(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{
		pr: &github.PRDetails{Number: 10, HeadRefName: "feat-branch"},
	}
	deps := baseHandleDeps(gh)
	deps.body = `@vairdict run "add validation"`
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.runConcurrencyWindow = time.Minute
	orchCalled := false
	deps.runOrchestration = func(_ context.Context, pr *github.PRDetails, intent string) error {
		orchCalled = true
		if pr.HeadRefName != "feat-branch" {
			t.Errorf("branch = %q", pr.HeadRefName)
		}
		if intent != "add validation" {
			t.Errorf("intent = %q", intent)
		}
		return nil
	}
	if err := runHandleCommentWith(context.Background(), 10, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !orchCalled {
		t.Error("expected runOrchestration to be called")
	}
	// Expect 2 comments: start + done.
	if len(gh.comments) != 2 {
		t.Fatalf("expected 2 comments, got %d", len(gh.comments))
	}
	if !strings.Contains(gh.comments[0], runStartMarker) {
		t.Errorf("expected start marker, got %q", gh.comments[0])
	}
	if !strings.Contains(gh.comments[1], runDoneMarker) {
		t.Errorf("expected done marker, got %q", gh.comments[1])
	}
	if !strings.Contains(gh.comments[1], "completed successfully") {
		t.Errorf("expected success message, got %q", gh.comments[1])
	}
}

func TestHandleComment_Run_ConcurrencyGuard_BlocksDuplicate(t *testing.T) {
	t.Parallel()
	// Simulate: start marker exists, done marker does NOT → run is active.
	callCount := 0
	gh := &fakeHandleGH{}
	deps := baseHandleDeps(gh)
	deps.body = `@vairdict run "something"`
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.runConcurrencyWindow = time.Minute
	// Override RecentCommentExists behavior: first call (start) → true,
	// second call (done) → false.
	originalGH := gh
	deps.gh = &fakeHandleGHWithRecentFunc{
		fakeHandleGH: originalGH,
		recentFunc: func(marker string) (bool, error) {
			callCount++
			if strings.Contains(marker, "run-start") {
				return true, nil // start marker found
			}
			return false, nil // done marker NOT found
		},
	}
	deps.runOrchestration = func(context.Context, *github.PRDetails, string) error {
		t.Fatal("runOrchestration should NOT be called when run is active")
		return nil
	}
	if err := runHandleCommentWith(context.Background(), 1, deps); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(originalGH.comments) != 1 {
		t.Fatalf("expected 1 comment, got %d", len(originalGH.comments))
	}
	if !strings.Contains(originalGH.comments[0], "already in progress") {
		t.Errorf("expected concurrency message, got %q", originalGH.comments[0])
	}
}

func TestHandleComment_Run_Error_PostsDoneMarker(t *testing.T) {
	t.Parallel()
	gh := &fakeHandleGH{
		pr: &github.PRDetails{Number: 10, HeadRefName: "feat-branch"},
	}
	deps := baseHandleDeps(gh)
	deps.body = `@vairdict run "broken thing"`
	deps.author = "alice"
	deps.assoc = "MEMBER"
	deps.runConcurrencyWindow = time.Minute
	deps.runOrchestration = func(context.Context, *github.PRDetails, string) error {
		return errors.New("orchestration failed")
	}
	err := runHandleCommentWith(context.Background(), 10, deps)
	if err == nil {
		t.Fatal("expected error")
	}
	// Should still post done marker so next run isn't blocked.
	found := false
	for _, c := range gh.comments {
		if strings.Contains(c, runDoneMarker) {
			found = true
			if !strings.Contains(c, "Run failed") {
				t.Errorf("expected failure in done comment, got %q", c)
			}
		}
	}
	if !found {
		t.Error("expected done marker to be posted even on failure")
	}
}

// fakeHandleGHWithRecentFunc wraps fakeHandleGH but overrides
// RecentCommentExists with a custom function for concurrency tests.
type fakeHandleGHWithRecentFunc struct {
	*fakeHandleGH
	recentFunc func(marker string) (bool, error)
}

func (f *fakeHandleGHWithRecentFunc) RecentCommentExists(_ context.Context, _ int, marker string, _ time.Duration) (bool, error) {
	return f.recentFunc(marker)
}

func TestLevenshtein(t *testing.T) {
	t.Parallel()
	cases := []struct {
		a, b string
		want int
	}{
		{"", "", 0},
		{"abc", "", 3},
		{"", "abc", 3},
		{"abc", "abc", 0},
		{"abc", "abd", 1},
		{"kitten", "sitting", 3},
		{"revew", "review", 1},
		{"approv", "approve", 1},
	}
	for _, tc := range cases {
		t.Run(tc.a+"_"+tc.b, func(t *testing.T) {
			if got := levenshtein(tc.a, tc.b); got != tc.want {
				t.Errorf("levenshtein(%q,%q) = %d, want %d", tc.a, tc.b, got, tc.want)
			}
		})
	}
}
