package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"

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

// PRDetails carries the fields needed to review an existing PR. It is the
// minimal projection of `gh pr view --json` that the review command needs;
// extending it is cheap if more fields are needed later.
type PRDetails struct {
	Number      int    `json:"number"`
	Title       string `json:"title"`
	Body        string `json:"body"`
	HeadRefName string `json:"headRefName"`
	HeadRefOid  string `json:"headRefOid"`
	BaseRefName string `json:"baseRefName"`
}

// IssueDetails is the minimal projection of `gh issue view --json` that the
// review command uses to derive an intent string from a linked issue.
type IssueDetails struct {
	Number int    `json:"number"`
	Title  string `json:"title"`
	Body   string `json:"body"`
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

// CreateBranch creates a new branch named vairdict/<slug>-<taskID> and
// checks it out. The slug is derived from intent so a glance at `git
// branch` or the PR head ref tells you what each branch is for; the
// taskID suffix keeps branches unique even when intents collide. Empty
// or unsluggable intents fall back to the legacy vairdict/<taskID>.
func (c *Client) CreateBranch(ctx context.Context, taskID, intent string) (string, error) {
	branch := "vairdict/" + taskID
	if slug := slugifyIntent(intent); slug != "" {
		branch = "vairdict/" + slug + "-" + taskID
	}

	if _, err := c.runner.Run(ctx, "git", "checkout", "-b", branch); err != nil {
		return "", fmt.Errorf("creating branch %s: %w", branch, err)
	}

	slog.Info("branch created", "branch", branch)
	return branch, nil
}

// slugifyIntent converts an intent string into a short, git-safe branch
// slug: first non-empty line, lowercased, ASCII alphanumerics joined by
// hyphens, capped at 40 chars, trimmed of leading "ui:" / "fix:" /
// "feat:" style conventional-commit prefixes so the slug describes the
// change rather than the type. Returns empty string if no usable
// characters remain.
func slugifyIntent(intent string) string {
	// First non-empty line — issue bodies often have a title line
	// followed by a blank line and a long description.
	line := ""
	for l := range strings.SplitSeq(intent, "\n") {
		if t := strings.TrimSpace(l); t != "" {
			line = t
			break
		}
	}
	if line == "" {
		return ""
	}
	// Drop a leading conventional-commit prefix like "ui:", "fix:",
	// "feat(scope):" — slug should describe the change, not the type.
	if i := strings.IndexByte(line, ':'); i != -1 && i < 20 {
		line = strings.TrimSpace(line[i+1:])
	}
	line = strings.ToLower(line)
	var b strings.Builder
	prevHyphen := false
	for _, r := range line {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
			prevHyphen = false
		default:
			if !prevHyphen && b.Len() > 0 {
				b.WriteByte('-')
				prevHyphen = true
			}
		}
		if b.Len() >= 40 {
			break
		}
	}
	return strings.Trim(b.String(), "-")
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

// FetchPR loads the metadata for an existing PR via `gh pr view --json`.
// Returns a PRDetails populated from the gh JSON output. The fields fetched
// are deliberately limited to those needed by the review command.
func (c *Client) FetchPR(ctx context.Context, number int) (*PRDetails, error) {
	out, err := c.runner.Run(ctx, "gh", "pr", "view", strconv.Itoa(number),
		"--json", "number,title,body,headRefName,headRefOid,baseRefName")
	if err != nil {
		return nil, fmt.Errorf("fetching PR #%d: %w", number, err)
	}
	var pr PRDetails
	if err := json.Unmarshal(out, &pr); err != nil {
		return nil, fmt.Errorf("parsing gh pr view output: %w", err)
	}
	return &pr, nil
}

// FetchIssue loads the metadata for an existing issue via `gh issue view`.
// Used by the review command to derive an intent string from the issue
// linked in the PR body.
func (c *Client) FetchIssue(ctx context.Context, number int) (*IssueDetails, error) {
	out, err := c.runner.Run(ctx, "gh", "issue", "view", strconv.Itoa(number),
		"--json", "number,title,body")
	if err != nil {
		return nil, fmt.Errorf("fetching issue #%d: %w", number, err)
	}
	var iss IssueDetails
	if err := json.Unmarshal(out, &iss); err != nil {
		return nil, fmt.Errorf("parsing gh issue view output: %w", err)
	}
	return &iss, nil
}

// FetchPRDiff returns the unified diff of the PR via `gh pr diff <n>`.
// Used by the review command to give the quality judge enough context
// without actually checking the branch out.
func (c *Client) FetchPRDiff(ctx context.Context, number int) (string, error) {
	out, err := c.runner.Run(ctx, "gh", "pr", "diff", strconv.Itoa(number))
	if err != nil {
		return "", fmt.Errorf("fetching diff for PR #%d: %w", number, err)
	}
	return string(out), nil
}

// linkedIssueRe matches GitHub's PR-closes-issue keywords. Case-insensitive,
// matches `Closes #12`, `fixes #34`, `Resolves: #56`, etc. Captures the
// issue number into group 1.
var linkedIssueRe = regexp.MustCompile(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\b[^\n#]*#(\d+)`)

// ParseLinkedIssue scans a PR body for the first GitHub closing-keyword
// reference (Closes/Fixes/Resolves) and returns the linked issue number,
// or 0 if no such reference is found.
func ParseLinkedIssue(body string) int {
	m := linkedIssueRe.FindStringSubmatch(body)
	if m == nil {
		return 0
	}
	n, err := strconv.Atoi(m[1])
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

// cannotApprovePRRe matches GitHub API errors when the authenticated
// user/token is not permitted to approve a PR. Two known cases:
//  1. Self-authored PR: "Can not approve your own pull request"
//  2. GitHub Actions token: "GitHub Actions is not permitted to approve pull requests"
//
// We detect these (rather than failing the run) so PostVerdict can
// gracefully fall back to a regular comment.
var cannotApprovePRRe = regexp.MustCompile(`(?i)(can ?not approve your own pull request|is not permitted to approve pull requests)`)

// verdictMarker is the string that appears in every verdict comment.
// Used to identify and clean up previous verdicts before posting a new one.
const verdictMarker = "Posted by @vairdict-judge"

// deletePreviousVerdicts removes any existing verdict comments on the PR
// so only the latest verdict is visible. Best-effort — errors are logged
// but do not block posting the new verdict.
func (c *Client) deletePreviousVerdicts(ctx context.Context, prNumber int) {
	// List all comments on the PR via the GitHub API.
	out, err := c.runner.Run(ctx, "gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/issues/%d/comments", prNumber),
		"--paginate", "--jq",
		fmt.Sprintf(`.[] | select(.body | contains("%s")) | .id`, verdictMarker))
	if err != nil {
		slog.Debug("failed to list previous verdicts", "pr", prNumber, "error", err)
		return
	}

	ids := strings.Fields(strings.TrimSpace(string(out)))
	for _, id := range ids {
		_, delErr := c.runner.Run(ctx, "gh", "api", "-X", "DELETE",
			fmt.Sprintf("repos/{owner}/{repo}/issues/comments/%s", id))
		if delErr != nil {
			slog.Debug("failed to delete old verdict comment", "id", id, "error", delErr)
		} else {
			slog.Debug("deleted old verdict comment", "id", id)
		}
	}
}

// InlineComment represents a single inline review comment on a PR diff.
type InlineComment struct {
	Path     string `json:"path"`
	Position int    `json:"position"`
	Body     string `json:"body"`
}

// PostVerdict posts a structured verdict comment on a PR, optionally with
// inline review comments on specific diff lines. When a diff is provided,
// gaps with file:line info are posted as inline comments in a single
// GitHub review; the summary verdict is still posted as a separate comment.
//
// Before posting, any previous verdict comments on the PR are deleted
// so the PR always shows exactly one verdict.
func (c *Client) PostVerdict(ctx context.Context, prNumber int, verdict *state.Verdict, phase state.Phase, loop int) error {
	return c.PostVerdictWithDiff(ctx, prNumber, verdict, phase, loop, "")
}

// PostVerdictWithDiff is like PostVerdict but accepts a diff string for
// resolving inline comment positions. When diff is non-empty, gaps that
// have File and Line set are posted as inline review comments.
func (c *Client) PostVerdictWithDiff(ctx context.Context, prNumber int, verdict *state.Verdict, phase state.Phase, loop int, diff string) error {
	c.deletePreviousVerdicts(ctx, prNumber)

	// Post inline review comments for gaps with file:line info.
	if diff != "" {
		c.postInlineReview(ctx, prNumber, verdict, diff)
	}

	comment := FormatVerdictComment(verdict, phase, loop)

	if verdict.Pass {
		err := c.ApprovePR(ctx, prNumber, comment)
		if err == nil {
			slog.Info("verdict posted", "pr", prNumber, "pass", true, "score", verdict.Score, "mode", "approval")
			return nil
		}
		if !cannotApprovePRRe.MatchString(err.Error()) {
			return fmt.Errorf("posting verdict approval: %w", err)
		}
		// Approval denied (self-authored PR or Actions token restriction).
		// Fall through to a plain comment so the verdict still gets posted.
		slog.Info("approval rejected, falling back to comment", "pr", prNumber, "reason", err)
	}

	if err := c.AddComment(ctx, prNumber, comment); err != nil {
		return fmt.Errorf("posting verdict comment: %w", err)
	}
	slog.Info("verdict posted", "pr", prNumber, "pass", verdict.Pass, "score", verdict.Score, "mode", "comment")
	return nil
}

// InlineReviewPayload is the JSON body sent to GitHub's
// POST /repos/{owner}/{repo}/pulls/{n}/reviews endpoint. Kept as a named
// type (instead of an anonymous struct inside postInlineReview) so unit
// tests can assert its shape directly, and so operators reading slogs see
// a meaningful type name.
type InlineReviewPayload struct {
	Event    string          `json:"event"`
	Body     string          `json:"body"`
	Comments []InlineComment `json:"comments"`
}

// BuildInlineReview turns a verdict + diff into a review payload whose
// comments point only at lines present in the diff. Gaps without File/Line
// or whose line does not appear in the diff are collected into the review
// body so reviewers still see every concern — previously they were dropped
// silently and only surfaced in the verdict table.
// Returns nil only when the verdict has no gaps at all.
func BuildInlineReview(verdict *state.Verdict, diff string) *InlineReviewPayload {
	positions := ParseDiffPositions(diff)

	var comments []InlineComment
	var unanchored []state.Gap
	for _, g := range verdict.Gaps {
		if g.File == "" || g.Line == 0 {
			unanchored = append(unanchored, g)
			continue
		}
		pos, ok := ResolveDiffPosition(positions, g.File, g.Line)
		if !ok {
			slog.Debug("gap line not in diff, surfacing as unanchored review body entry",
				"file", g.File, "line", g.Line, "severity", g.Severity)
			unanchored = append(unanchored, g)
			continue
		}
		comments = append(comments, InlineComment{
			Path:     g.File,
			Position: pos,
			Body:     formatInlineComment(g),
		})
	}

	if len(comments) == 0 && len(unanchored) == 0 {
		return nil
	}

	return &InlineReviewPayload{
		Event:    "COMMENT",
		Body:     formatReviewBody(len(comments), unanchored),
		Comments: comments,
	}
}

// formatReviewBody builds the top-level review body. When every gap has a
// diff anchor the body is just a summary line; otherwise each unanchored
// gap is rendered as a bullet so no concern silently disappears from the
// PR thread. The non-inline gaps would otherwise only show up in the
// verdict criteria table, which readers rarely scan row-by-row.
func formatReviewBody(inlineCount int, unanchored []state.Gap) string {
	var b strings.Builder
	fmt.Fprintf(&b, "VAIrdict inline review: %d comment(s)", inlineCount)
	if len(unanchored) > 0 {
		b.WriteString("\n\n**Gaps without a diff anchor:**\n")
		for _, g := range unanchored {
			fmt.Fprintf(&b, "- %s\n", formatInlineComment(g))
		}
	}
	return b.String()
}

// postInlineReview creates a single GitHub review with inline comments
// for gaps that have resolvable file:line positions. Best-effort — errors
// are logged but do not block the summary verdict from being posted.
func (c *Client) postInlineReview(ctx context.Context, prNumber int, verdict *state.Verdict, diff string) {
	review := BuildInlineReview(verdict, diff)
	if review == nil {
		return
	}

	payload, err := json.Marshal(review)
	if err != nil {
		slog.Debug("failed to marshal review payload", "error", err)
		return
	}

	// Write payload to a temp file for gh api --input since our
	// CommandRunner doesn't support stdin.
	f, err := os.CreateTemp("", "vairdict-review-*.json")
	if err != nil {
		slog.Debug("failed to create temp file for review", "error", err)
		return
	}
	tmpPath := f.Name()
	defer func() { _ = os.Remove(tmpPath) }()

	if _, err := f.Write(payload); err != nil {
		_ = f.Close()
		slog.Debug("failed to write review payload", "error", err)
		return
	}
	_ = f.Close()

	_, err = c.runner.Run(ctx, "gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/pulls/%d/reviews", prNumber),
		"-X", "POST",
		"--input", tmpPath,
	)
	if err != nil {
		slog.Debug("failed to post inline review", "pr", prNumber, "error", err)
		return
	}
	slog.Info("inline review posted", "pr", prNumber, "comments", len(review.Comments))
}

// formatInlineComment builds the markdown body for a single inline comment.
func formatInlineComment(g state.Gap) string {
	icon := "💡"
	if g.Blocking {
		icon = "🚫"
	}
	return fmt.Sprintf("%s **[%s]** %s", icon, g.Severity, g.Description)
}

// CommitStatusContext is the context string VAIrdict uses when posting
// override commit statuses from PR-mention commands (`approve` / `ignore`).
// A fixed, recognisable context name means the override status is easy
// to spot in the PR's checks list and easy to correlate in audit logs.
const CommitStatusContext = "vairdict/review"

// SetCommitStatus posts a commit status to the given SHA via the GitHub
// statuses API. Used by the PR-mention `approve` / `ignore` handlers to
// unblock a PR whose last VAIrdict verdict failed: a green status with
// context `vairdict/review` overrides the prior failure for branch
// protection rules that only require this context.
//
// state must be one of error|failure|pending|success (GitHub's API values).
// The description is surfaced in the PR's checks list — keep it short.
func (c *Client) SetCommitStatus(ctx context.Context, sha, state, statusContext, description string) error {
	if sha == "" {
		return fmt.Errorf("commit status requires a non-empty SHA")
	}
	args := []string{
		"api", "-X", "POST",
		fmt.Sprintf("repos/{owner}/{repo}/statuses/%s", sha),
		"-f", "state=" + state,
		"-f", "context=" + statusContext,
	}
	if description != "" {
		args = append(args, "-f", "description="+description)
	}
	if _, err := c.runner.Run(ctx, "gh", args...); err != nil {
		return fmt.Errorf("setting commit status for %s: %w", sha, err)
	}
	slog.Info("commit status set", "sha", sha, "state", state, "context", statusContext)
	return nil
}

// RecentCommentExists reports whether any comment on the PR contains the
// given marker and was created within the given window. Used for
// rate-limiting: the `review` handler posts a marker comment when it
// starts a re-run, and subsequent `@vairdict review` mentions within
// the window short-circuit to avoid queuing duplicate runs.
//
// Errors surface to the caller rather than being swallowed — a stale
// gh shellout should not silently disable rate-limiting.
func (c *Client) RecentCommentExists(ctx context.Context, prNumber int, marker string, within time.Duration) (bool, error) {
	cutoff := time.Now().Add(-within).UTC().Format(time.RFC3339)
	// Sort descending so the comments we care about (posted in the last
	// `within`) are on page 1; no --paginate so the --jq filter applies
	// once to a single JSON array and the result is an unambiguous count.
	out, err := c.runner.Run(ctx, "gh", "api",
		fmt.Sprintf("repos/{owner}/{repo}/issues/%d/comments?sort=created&direction=desc&per_page=30", prNumber),
		"--jq",
		fmt.Sprintf(`[.[] | select(.created_at >= "%s") | select(.body | contains("%s"))] | length`, cutoff, marker))
	if err != nil {
		return false, fmt.Errorf("listing PR comments: %w", err)
	}
	count := strings.TrimSpace(string(out))
	return count != "" && count != "0", nil
}

// MergePR merges a PR via `gh pr merge --squash --delete-branch`. The
// squash strategy is hardcoded for now — merge strategy configuration
// is out of scope for M4.
func (c *Client) MergePR(ctx context.Context, prNumber int) error {
	_, err := c.runner.Run(ctx, "gh", "pr", "merge", fmt.Sprintf("%d", prNumber),
		"--squash", "--delete-branch")
	if err != nil {
		return fmt.Errorf("merging PR #%d: %w", prNumber, err)
	}
	slog.Info("PR merged", "pr", prNumber)
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

// logoURL is the raw GitHub URL for the VAIrdict logo asset. Must be a
// PNG, not SVG: GitHub's camo image proxy strips SVGs from user content
// (XSS hardening) so an <img> pointing at a .svg renders as a broken
// image in PR comments. PNG renders fine.
const logoURL = "https://raw.githubusercontent.com/vairdict/vairdict/main/assets/logo.png"

// FormatVerdictComment builds a structured markdown comment from a Verdict.
func FormatVerdictComment(verdict *state.Verdict, phase state.Phase, loop int) string {
	var b strings.Builder

	// Header with logo and pass/fail status.
	if verdict.Pass {
		fmt.Fprintf(&b, "<h2><img src=\"%s\" alt=\"VAIrdict\" height=\"24\"> VAIrdict Verdict: ✅ PASS</h2>\n\n", logoURL)
	} else {
		fmt.Fprintf(&b, "<h2><img src=\"%s\" alt=\"VAIrdict\" height=\"24\"> VAIrdict Verdict: ❌ FAIL</h2>\n\n", logoURL)
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
