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
}

func (f *fakeHandleGH) AddComment(_ context.Context, _ int, body string) error {
	f.comments = append(f.comments, body)
	return f.commentErr
}

func (f *fakeHandleGH) FetchPR(_ context.Context, _ int) (*github.PRDetails, error) {
	return f.pr, f.prErr
}

func (f *fakeHandleGH) SetCommitStatus(_ context.Context, sha, state, statusContext, description string) error {
	f.statusSHA = sha
	f.statusState = state
	f.statusContext = statusContext
	f.statusDescription = description
	return f.statusErr
}

func (f *fakeHandleGH) RecentCommentExists(_ context.Context, _ int, _ string, _ time.Duration) (bool, error) {
	f.recentCalls++
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
