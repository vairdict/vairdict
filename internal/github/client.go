package github

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strconv"
	"strings"

	"github.com/vairdict/vairdict/internal/state"
)

// CommandRunner executes shell commands. Injected for testing.
type CommandRunner interface {
	Run(ctx context.Context, name string, args ...string) ([]byte, error)
}

// ExecRunner is the real implementation using os/exec.
type ExecRunner struct {
	Dir string
}

// ExecError wraps exec.Cmd failures with captured stderr so callers (and
// the user) see *why* a gh / git shellout failed instead of a bare
// "exit status 1". Returned by ExecRunner.Run when the command exits
// with a non-zero status.
type ExecError struct {
	Cmd    string
	Stderr string
	Err    error
}

// Error renders the underlying error along with any captured stderr. The
// stderr is trimmed of a trailing newline but otherwise printed verbatim
// so the user gets the original gh / git message.
func (e *ExecError) Error() string {
	stderr := strings.TrimRight(e.Stderr, "\n")
	if stderr == "" {
		return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
	}
	return fmt.Sprintf("%s: %v: %s", e.Cmd, e.Err, stderr)
}

// Unwrap exposes the underlying exec error for errors.Is / errors.As.
func (e *ExecError) Unwrap() error { return e.Err }

// Run executes a command, capturing stdout and stderr into separate
// buffers. On success the stdout bytes are returned. On failure an
// *ExecError is returned that carries the captured stderr so the caller
// can surface a useful diagnostic. The returned byte slice still
// contains stdout in both cases so existing callers that read Run's
// output on error continue to work.
func (e *ExecRunner) Run(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	if e.Dir != "" {
		cmd.Dir = e.Dir
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.Bytes(), &ExecError{
			Cmd:    name + " " + strings.Join(args, " "),
			Stderr: stderr.String(),
			Err:    err,
		}
	}
	return stdout.Bytes(), nil
}

// PR represents a created pull request.
type PR struct {
	URL    string
	Number int
}

// CreatePROpts holds options for creating a pull request.
type CreatePROpts struct {
	Title       string
	Body        string
	BaseBranch  string
	HeadBranch  string
	IssueNumber int
}

// Client wraps GitHub operations via the gh CLI.
type Client struct {
	runner CommandRunner
}

// New creates a Client with the given runner.
func New(runner CommandRunner) *Client {
	return &Client{runner: runner}
}

// CreateBranch creates a new branch named vairdict/<taskID> and checks it out.
func (c *Client) CreateBranch(ctx context.Context, taskID string) (string, error) {
	branch := "vairdict/" + taskID

	if _, err := c.runner.Run(ctx, "git", "checkout", "-b", branch); err != nil {
		return "", fmt.Errorf("creating branch %s: %w", branch, err)
	}

	slog.Info("branch created", "branch", branch)
	return branch, nil
}

// CreatePR creates a pull request using gh pr create.
func (c *Client) CreatePR(ctx context.Context, opts CreatePROpts) (*PR, error) {
	// Verify we're in a git repo.
	if _, err := c.runner.Run(ctx, "git", "rev-parse", "--git-dir"); err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}

	// Verify remote exists.
	if _, err := c.runner.Run(ctx, "git", "remote", "get-url", "origin"); err != nil {
		return nil, fmt.Errorf("no remote 'origin' configured: %w", err)
	}

	// Verify gh auth.
	if _, err := c.runner.Run(ctx, "gh", "auth", "status"); err != nil {
		return nil, fmt.Errorf("gh not authenticated: %w", err)
	}

	// Push the branch.
	if opts.HeadBranch != "" {
		if _, err := c.runner.Run(ctx, "git", "push", "-u", "origin", opts.HeadBranch); err != nil {
			return nil, fmt.Errorf("pushing branch: %w", err)
		}
	}

	// Build gh pr create args.
	args := []string{"pr", "create", "--title", opts.Title, "--body", opts.Body}
	if opts.BaseBranch != "" {
		args = append(args, "--base", opts.BaseBranch)
	}
	if opts.HeadBranch != "" {
		args = append(args, "--head", opts.HeadBranch)
	}

	output, err := c.runner.Run(ctx, "gh", args...)
	if err != nil {
		return nil, fmt.Errorf("creating PR: %w", err)
	}

	url := strings.TrimSpace(string(output))
	slog.Info("PR created", "url", url)

	number := parsePRNumber(url)

	return &PR{URL: url, Number: number}, nil
}

// parsePRNumber extracts the PR number from a GitHub PR URL.
// Returns 0 if the URL doesn't match the expected format.
func parsePRNumber(url string) int {
	// URL format: https://github.com/owner/repo/pull/123
	parts := strings.Split(url, "/")
	if len(parts) < 2 {
		return 0
	}
	last := parts[len(parts)-1]
	n, err := strconv.Atoi(last)
	if err != nil {
		return 0
	}
	return n
}

// AddComment adds a comment to a PR.
func (c *Client) AddComment(ctx context.Context, prNumber int, body string) error {
	_, err := c.runner.Run(ctx, "gh", "pr", "comment", fmt.Sprintf("%d", prNumber), "--body", body)
	if err != nil {
		return fmt.Errorf("adding comment to PR #%d: %w", prNumber, err)
	}
	return nil
}

// ApprovePR approves a PR via the GitHub review API using gh pr review.
func (c *Client) ApprovePR(ctx context.Context, prNumber int, body string) error {
	args := []string{"pr", "review", fmt.Sprintf("%d", prNumber), "--approve"}
	if body != "" {
		args = append(args, "--body", body)
	}
	_, err := c.runner.Run(ctx, "gh", args...)
	if err != nil {
		return fmt.Errorf("approving PR #%d: %w", prNumber, err)
	}
	slog.Info("PR approved", "pr", prNumber)
	return nil
}

// PostVerdict posts a structured verdict comment on a PR. On pass, it also
// approves the PR via the review API.
func (c *Client) PostVerdict(ctx context.Context, prNumber int, verdict *state.Verdict, phase state.Phase, loop int) error {
	comment := FormatVerdictComment(verdict, phase, loop)

	if verdict.Pass {
		// Approve with the verdict as the review body.
		if err := c.ApprovePR(ctx, prNumber, comment); err != nil {
			return fmt.Errorf("posting verdict approval: %w", err)
		}
	} else {
		// Post as a regular comment on failure.
		if err := c.AddComment(ctx, prNumber, comment); err != nil {
			return fmt.Errorf("posting verdict comment: %w", err)
		}
	}

	slog.Info("verdict posted", "pr", prNumber, "pass", verdict.Pass, "score", verdict.Score)
	return nil
}

// FormatPRBody generates a PR body from task data.
func FormatPRBody(task *state.Task, issueNumber int, summary string) string {
	var b strings.Builder

	if issueNumber > 0 {
		fmt.Fprintf(&b, "## Issue\nCloses #%d\n\n", issueNumber)
	}

	fmt.Fprintf(&b, "## What was built\n%s\n\n", summary)

	// Assumptions.
	if len(task.Assumptions) > 0 {
		b.WriteString("## Assumptions made\n")
		for _, a := range task.Assumptions {
			fmt.Fprintf(&b, "- [%s] %s\n", a.Severity, a.Description)
		}
		b.WriteString("\n")
	}

	// Verdict from last attempt.
	if len(task.Attempts) > 0 {
		last := task.Attempts[len(task.Attempts)-1]
		if last.Verdict != nil {
			fmt.Fprintf(&b, "## VAIrdict verdict\nScore: %.0f%%\nLoops: %d\n", last.Verdict.Score, last.Loop)
		}
	}

	return b.String()
}

// FormatVerdictComment builds a structured markdown comment from a Verdict.
func FormatVerdictComment(verdict *state.Verdict, phase state.Phase, loop int) string {
	var b strings.Builder

	// Header with pass/fail status.
	if verdict.Pass {
		b.WriteString("## VAIrdict Verdict: PASS\n\n")
	} else {
		b.WriteString("## VAIrdict Verdict: FAIL\n\n")
	}

	// Summary line.
	fmt.Fprintf(&b, "**Score:** %.0f%% | **Phase:** %s | **Loop:** %d\n\n", verdict.Score, phase, loop)

	// Criteria table — build from gaps.
	if len(verdict.Gaps) > 0 {
		b.WriteString("### Criteria\n\n")
		b.WriteString("| Severity | Status | Description |\n")
		b.WriteString("|----------|--------|-------------|\n")
		for _, g := range verdict.Gaps {
			status := "pass"
			if g.Blocking {
				status = "BLOCKING"
			}
			fmt.Fprintf(&b, "| %s | %s | %s |\n", g.Severity, status, g.Description)
		}
		b.WriteString("\n")
	}

	// Gaps section for failures.
	if !verdict.Pass {
		blocking := make([]state.Gap, 0)
		for _, g := range verdict.Gaps {
			if g.Blocking {
				blocking = append(blocking, g)
			}
		}
		if len(blocking) > 0 {
			b.WriteString("### Blocking Gaps\n\n")
			for _, g := range blocking {
				fmt.Fprintf(&b, "- **[%s]** %s\n", g.Severity, g.Description)
			}
			b.WriteString("\n")
		}
	}

	// Questions if any.
	if len(verdict.Questions) > 0 {
		b.WriteString("### Questions\n\n")
		for _, q := range verdict.Questions {
			fmt.Fprintf(&b, "- [%s] %s\n", q.Priority, q.Text)
		}
		b.WriteString("\n")
	}

	b.WriteString("---\n*Posted by @vairdict-judge*\n")

	return b.String()
}

// GeneratePRTitle creates a PR title from the task intent, capped at 70 chars.
func GeneratePRTitle(task *state.Task) string {
	title := task.Intent
	if len(title) > 70 {
		title = title[:67] + "..."
	}
	return title
}
