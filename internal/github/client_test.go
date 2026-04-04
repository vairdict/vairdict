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
		},
	}
}

func TestCreateBranch(t *testing.T) {
	runner := successRunner()
	client := New(runner)

	branch, err := client.CreateBranch(context.Background(), "abc123")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if branch != "vairdict/abc123" {
		t.Errorf("branch = %q, want %q", branch, "vairdict/abc123")
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
