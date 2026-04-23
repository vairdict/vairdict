// Package main — handle_comment.go implements the `vairdict handle-comment
// <pr-number>` subcommand invoked by the vairdict-mentions workflow. It
// parses an `@vairdict <command>` mention from an `issue_comment` event
// payload, authorises the commenter against GitHub's author_association,
// and dispatches to the right handler (review / approve / ignore). Every
// execution path posts a short confirmation comment so the audit trail
// lives in the PR thread.
//
// Inputs are read from env vars the workflow sets before invoking the
// binary, so this command is safe to run in CI without extra flags:
//
//	VAIRDICT_COMMENT_BODY         — raw comment body from webhook payload
//	VAIRDICT_COMMENT_AUTHOR       — commenter login
//	VAIRDICT_COMMENT_ASSOCIATION  — author_association (OWNER / MEMBER / …)
//
// Exits non-zero only when the underlying handler reports an error that
// should fail the workflow (a blocked authorisation is not an error —
// the handler posts a reply and exits 0 so the step reports green).
package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"github.com/vairdict/vairdict/internal/agents/claudecode"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/github"
	"github.com/vairdict/vairdict/internal/ui"
)

// commentCommand is the set of recognised `@vairdict <cmd>` keywords.
// Kept as a named string type so the parser returns a typed value and
// callers switch on it with compiler-checked exhaustiveness rather than
// stringly-typed comparisons.
type commentCommand string

const (
	cmdNone    commentCommand = ""
	cmdReview  commentCommand = "review"
	cmdApprove commentCommand = "approve"
	cmdIgnore  commentCommand = "ignore"
	cmdExplain commentCommand = "explain"
	cmdFix     commentCommand = "fix"
	cmdRun     commentCommand = "run"
)

// knownCommands is the canonical list of commands, used both to validate
// parsed tokens and to drive the "did you mean?" suggestion. Declared as
// a slice (not a map) so the order is deterministic in help text.
var knownCommands = []commentCommand{cmdReview, cmdApprove, cmdIgnore, cmdExplain, cmdFix, cmdRun}

// reviewRateLimitWindow is the minimum gap between two accepted
// `@vairdict review` runs on the same PR. A second mention inside the
// window short-circuits with "already running" rather than queuing a
// duplicate judge run.
const reviewRateLimitWindow = 30 * time.Second

// reviewStartMarker is embedded in the "starting review" confirmation
// comment so the rate-limit check can find recent runs without relying
// on comment timestamps of arbitrary human text. HTML comment form keeps
// it invisible in the rendered PR thread.
const reviewStartMarker = "<!-- vairdict-mention-review-start -->"

// overrideMarker is embedded in `approve` override confirmations so an
// auditor can grep a PR thread for every human override in one pass.
const overrideMarker = "<!-- vairdict-mention-override -->"

// ignoreMarker is embedded in `ignore` dismissal confirmations for the
// same reason as overrideMarker.
const ignoreMarker = "<!-- vairdict-mention-ignore -->"

// runStartMarker is embedded in the "starting run" confirmation comment
// so the concurrency guard can detect an in-progress run without external
// state. The run command allows only one active run per PR.
const runStartMarker = "<!-- vairdict-mention-run-start -->"

// runDoneMarker is embedded when a run completes so the concurrency guard
// can tell the difference between a run that is still active and one that
// has finished. Without this, the start marker alone blocks subsequent runs.
const runDoneMarker = "<!-- vairdict-mention-run-done -->"

// explainMarker is embedded in explain replies for auditability.
const explainMarker = "<!-- vairdict-mention-explain -->"

// fixMarker is embedded in fix confirmation comments.
const fixMarker = "<!-- vairdict-mention-fix -->"

// parseResult captures everything the parser learned about a comment
// body. Mentioned=false means "no @vairdict mention at all" and is the
// cue for the workflow to exit 0 silently. Mentioned=true with an empty
// Command means the user mentioned the bot but the token is missing or
// unknown — DidYouMean carries a suggestion when available.
type parseResult struct {
	Mentioned  bool
	Raw        string // token that followed @vairdict, lowercased, punctuation stripped
	Command    commentCommand
	Args       string // everything after the command token (trimmed), for explain/fix/run
	DidYouMean commentCommand
}

// mentionBoundaryRe matches a literal `@vairdict` that is NOT part of a
// longer handle like `@vairdict-bot`. Character after the match must be
// whitespace, punctuation, or end of string.
var mentionBoundaryRe = regexp.MustCompile(`(?i)@vairdict(?:$|[^\w-])`)

// parseComment extracts the first well-formed `@vairdict <command>`
// mention from a comment body. The matching is case-insensitive;
// trailing punctuation on the command token (`@vairdict review.`) is
// stripped. Tokens not in knownCommands trigger a Levenshtein-based
// "did you mean?" suggestion when the distance is small.
func parseComment(body string) parseResult {
	loc := mentionBoundaryRe.FindStringIndex(body)
	if loc == nil {
		return parseResult{}
	}
	// The regex consumes one trailing separator — step back by one if it
	// wasn't end-of-string so we don't eat the first char of the token.
	after := loc[1]
	if after <= len(body) && loc[1]-loc[0] > len("@vairdict") {
		after = loc[0] + len("@vairdict")
	}
	res := parseResult{Mentioned: true}
	rest := strings.TrimLeft(body[after:], " \t\r\n")
	// Read next whitespace-terminated token.
	end := 0
	for end < len(rest) {
		r := rest[end]
		if r == ' ' || r == '\t' || r == '\n' || r == '\r' {
			break
		}
		end++
	}
	token := strings.Trim(rest[:end], ".,;:!?)(")
	token = strings.ToLower(token)
	if token == "" {
		return res
	}
	res.Raw = token
	for _, c := range knownCommands {
		if token == string(c) {
			res.Command = c
			// Capture the remaining text after the command token as args.
			// Used by explain, fix, and run which take arguments.
			if end < len(rest) {
				args := strings.TrimSpace(rest[end:])
				// For `run`, strip surrounding quotes from the intent.
				if c == cmdRun {
					args = stripQuotes(args)
				}
				res.Args = args
			}
			return res
		}
	}
	res.DidYouMean = suggestCommand(token)
	return res
}

// suggestCommand returns the knownCommand closest to raw by Levenshtein
// distance if that distance is <= 2; otherwise the empty command. The
// threshold of 2 keeps suggestions helpful for single-char typos
// (`revew`, `approv`) without false-matching unrelated tokens.
func suggestCommand(raw string) commentCommand {
	best := cmdNone
	bestDist := -1
	for _, c := range knownCommands {
		d := levenshtein(raw, string(c))
		if bestDist == -1 || d < bestDist {
			best = c
			bestDist = d
		}
	}
	if bestDist >= 0 && bestDist <= 2 {
		return best
	}
	return cmdNone
}

// levenshtein computes the edit distance between a and b. Standard
// two-row dynamic programming — kept local so the package has no
// external dependency for this one call site.
func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	curr := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		curr[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			del := prev[j] + 1
			ins := curr[j-1] + 1
			sub := prev[j-1] + cost
			m := del
			if ins < m {
				m = ins
			}
			if sub < m {
				m = sub
			}
			curr[j] = m
		}
		prev, curr = curr, prev
	}
	return prev[len(rb)]
}

// stripQuotes removes surrounding double or single quotes from a string.
// Used to extract the intent from `@vairdict run "add login"`.
func stripQuotes(s string) string {
	if len(s) >= 2 {
		if (s[0] == '"' && s[len(s)-1] == '"') || (s[0] == '\'' && s[len(s)-1] == '\'') {
			return s[1 : len(s)-1]
		}
	}
	return s
}

// authorized reports whether a GitHub author_association string grants
// permission to run VAIrdict mention commands. Case-insensitive so we
// tolerate lowercase payloads from third-party webhook bridges.
func authorized(association string) bool {
	switch strings.ToUpper(strings.TrimSpace(association)) {
	case "OWNER", "MEMBER", "COLLABORATOR":
		return true
	}
	return false
}

// handleCommentGH is the narrow GitHub surface handle-comment needs.
// *github.Client satisfies it; a hand-rolled fake in the test file
// does too, which keeps the orchestration core free of exec/network.
type handleCommentGH interface {
	AddComment(ctx context.Context, prNumber int, body string) error
	AddReaction(ctx context.Context, commentID int64, content string) error
	FetchPR(ctx context.Context, number int) (*github.PRDetails, error)
	FetchPRDiff(ctx context.Context, number int) (string, error)
	SetCommitStatus(ctx context.Context, sha, state, statusContext, description string) error
	RecentCommentExists(ctx context.Context, prNumber int, marker string, within time.Duration) (bool, error)
}

// handleCommentDeps bundles the collaborators and inputs that
// runHandleCommentWith needs. Built once from env vars / real clients
// by runHandleComment, or constructed directly by tests with fakes.
type handleCommentDeps struct {
	gh         handleCommentGH
	stdout     io.Writer
	body       string
	author     string
	assoc      string
	commentID  int64
	runReview  func(prNumber int) error
	rateWindow time.Duration

	// explainQuestion calls an LLM with the PR diff and question, returns
	// the answer text. Injected so tests can fake the LLM call.
	explainQuestion func(ctx context.Context, diff, question string) (string, error)

	// fixCode checks out the PR branch, runs Claude Code with the
	// description scoped to the diff, commits, pushes, and returns the
	// new commit SHA. Injected for testability.
	fixCode func(ctx context.Context, pr *github.PRDetails, description, diff string) (string, error)

	// runOrchestration triggers the full plan→code→quality loop against
	// the PR branch. Progress and the final verdict are posted by the
	// implementation itself; this hook returns nil on success.
	runOrchestration func(ctx context.Context, pr *github.PRDetails, intent string) error

	// runConcurrencyWindow is how long an active `run` blocks subsequent
	// runs on the same PR. Zero means use the default (60 min).
	runConcurrencyWindow time.Duration
}

var handleCommentCmd = &cobra.Command{
	Use:   "handle-comment <pr-number>",
	Short: "Dispatch an @vairdict PR comment mention (review/approve/ignore)",
	Long: `Parse a GitHub issue_comment webhook payload, authorise the commenter
against author_association, and run the matching VAIrdict handler.

Inputs are read from environment variables set by the workflow:
  VAIRDICT_COMMENT_BODY         — comment body (raw text)
  VAIRDICT_COMMENT_AUTHOR       — commenter login
  VAIRDICT_COMMENT_ASSOCIATION  — GitHub author_association

Every execution posts a confirmation comment on the PR so the audit
trail lives in the thread. Unauthorised commenters receive a single
reply explaining the restriction; unrelated comments are ignored
silently.`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prNumber, err := strconv.Atoi(args[0])
		if err != nil || prNumber <= 0 {
			return fmt.Errorf("invalid PR number %q", args[0])
		}
		return runHandleComment(prNumber)
	},
}

func init() {
	rootCmd.AddCommand(handleCommentCmd)
}

// runHandleComment wires up the real github client and delegates to
// runHandleCommentWith. The review handler re-uses the existing
// `runReview` package function so a PR-mention review runs the exact
// same quality-judge flow as a push-triggered review.
func runHandleComment(prNumber int) error {
	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}
	ghClient := github.New(&github.ExecRunner{Dir: workDir})

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	commentID, _ := strconv.ParseInt(os.Getenv("VAIRDICT_COMMENT_ID"), 10, 64)

	return runHandleCommentWith(ctx, prNumber, handleCommentDeps{
		gh:         ghClient,
		stdout:     os.Stdout,
		body:       os.Getenv("VAIRDICT_COMMENT_BODY"),
		author:     os.Getenv("VAIRDICT_COMMENT_AUTHOR"),
		assoc:      os.Getenv("VAIRDICT_COMMENT_ASSOCIATION"),
		commentID:  commentID,
		runReview:  runReview,
		rateWindow: reviewRateLimitWindow,
		explainQuestion: func(ctx context.Context, diff, question string) (string, error) {
			return runExplainQuestion(ctx, diff, question)
		},
		fixCode: func(ctx context.Context, pr *github.PRDetails, description, diff string) (string, error) {
			return runFixCode(ctx, workDir, pr, description, diff)
		},
		runOrchestration: func(ctx context.Context, pr *github.PRDetails, intent string) error {
			return runMentionOrchestration(ctx, workDir, pr, intent)
		},
	})
}

// runHandleCommentWith is the testable orchestration core. The order is:
//  1. Parse the comment — no mention → exit silently.
//  2. Unknown command with a typo hint → reply and exit 0.
//  3. Authorisation check — denied → reply and exit 0 (never an error).
//  4. Dispatch to the matching handler.
//
// Handler errors propagate so the workflow step fails visibly; the
// authorisation and parse paths succeed so the workflow run stays green
// even for comments that don't do anything.
func runHandleCommentWith(ctx context.Context, prNumber int, deps handleCommentDeps) error {
	res := parseComment(deps.body)
	if !res.Mentioned {
		slog.Debug("no @vairdict mention, nothing to do", "pr", prNumber)
		return nil
	}

	// Acknowledge the mention immediately with an :eyes: reaction so the
	// commenter sees visual feedback before any processing starts.
	if deps.commentID != 0 {
		if err := deps.gh.AddReaction(ctx, deps.commentID, "eyes"); err != nil {
			slog.Warn("failed to add eyes reaction", "pr", prNumber, "error", err)
		}
	}

	if res.Command == cmdNone {
		return replyUnknown(ctx, deps, prNumber, res)
	}

	if !authorized(deps.assoc) {
		return replyUnauthorized(ctx, deps, prNumber, res.Command)
	}

	switch res.Command {
	case cmdReview:
		return handleReviewMention(ctx, deps, prNumber)
	case cmdApprove:
		return handleApproveMention(ctx, deps, prNumber)
	case cmdIgnore:
		return handleIgnoreMention(ctx, deps, prNumber)
	case cmdExplain:
		return handleExplainMention(ctx, deps, prNumber, res.Args)
	case cmdFix:
		return handleFixMention(ctx, deps, prNumber, res.Args)
	case cmdRun:
		return handleRunMention(ctx, deps, prNumber, res.Args)
	}
	return nil
}

// replyUnknown posts a helpful reply for `@vairdict` mentions whose
// command token is missing, unknown, or a near-miss of a real command.
// Returns nil so the workflow run reports green — a typo is a user
// error, not a system failure.
func replyUnknown(ctx context.Context, deps handleCommentDeps, prNumber int, res parseResult) error {
	var body strings.Builder
	fmt.Fprintf(&body, "<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> ", github.LogoURL)
	body.WriteString("Hi — I didn't recognise that command.\n\n")
	if res.Raw == "" {
		body.WriteString("Usage: mention `@vairdict` followed by one of: ")
	} else if res.DidYouMean != cmdNone {
		fmt.Fprintf(&body, "Did you mean `@vairdict %s`?\n\n", res.DidYouMean)
		body.WriteString("Supported commands: ")
	} else {
		fmt.Fprintf(&body, "`%s` isn't a command I know.\n\n", res.Raw)
		body.WriteString("Supported commands: ")
	}
	for i, c := range knownCommands {
		if i > 0 {
			body.WriteString(", ")
		}
		fmt.Fprintf(&body, "`%s`", c)
	}
	body.WriteString(".\n")
	if err := deps.gh.AddComment(ctx, prNumber, body.String()); err != nil {
		return fmt.Errorf("posting help reply: %w", err)
	}
	return nil
}

// replyUnauthorized posts the "you don't have permission" explanation
// and returns nil. Explicitly replying (rather than silently dropping)
// is a requirement of #100 — the audit trail must record every attempt.
func replyUnauthorized(ctx context.Context, deps handleCommentDeps, prNumber int, cmd commentCommand) error {
	body := fmt.Sprintf(
		"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> `@vairdict %s` is only available to repository owners, members, "+
			"or collaborators. If you need the judge re-run, please ask a "+
			"maintainer to comment for you.\n", github.LogoURL, cmd)
	if err := deps.gh.AddComment(ctx, prNumber, body); err != nil {
		return fmt.Errorf("posting auth denial reply: %w", err)
	}
	slog.Info("denied unauthorized mention",
		"pr", prNumber, "command", cmd, "author", deps.author, "association", deps.assoc)
	return nil
}

// handleReviewMention re-runs the quality judge via the existing
// `vairdict review` flow. A marker comment is posted first so a second
// mention inside the rate-limit window can short-circuit.
func handleReviewMention(ctx context.Context, deps handleCommentDeps, prNumber int) error {
	recent, err := deps.gh.RecentCommentExists(ctx, prNumber, reviewStartMarker, deps.rateWindow)
	if err != nil {
		slog.Warn("rate-limit check failed, continuing", "pr", prNumber, "error", err)
	} else if recent {
		body := fmt.Sprintf(
			"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> A review is already running for this PR (mention by @%s ignored). "+
				"Please wait for the current verdict before re-requesting.\n",
			github.LogoURL, deps.author)
		if addErr := deps.gh.AddComment(ctx, prNumber, body); addErr != nil {
			return fmt.Errorf("posting rate-limit reply: %w", addErr)
		}
		return nil
	}

	startBody := fmt.Sprintf(
		"🔁 Re-running VAIrdict review on @%s's request…\n\n%s\n",
		deps.author, reviewStartMarker)
	if err := deps.gh.AddComment(ctx, prNumber, startBody); err != nil {
		return fmt.Errorf("posting review start comment: %w", err)
	}

	if deps.runReview == nil {
		return fmt.Errorf("runReview hook not configured")
	}
	if err := deps.runReview(prNumber); err != nil {
		return fmt.Errorf("re-running review: %w", err)
	}
	return nil
}

// handleApproveMention records a signed human override and posts a
// green commit status under `vairdict/review` so branch protection
// unblocks the PR. It deliberately does NOT merge — auto-merge is
// orthogonal (#39) and should remain the only path that hits the
// merge button.
func handleApproveMention(ctx context.Context, deps handleCommentDeps, prNumber int) error {
	pr, err := deps.gh.FetchPR(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("fetching PR for approve override: %w", err)
	}
	if pr.HeadRefOid == "" {
		return fmt.Errorf("PR #%d has no head commit SHA; cannot set override status", prNumber)
	}

	desc := fmt.Sprintf("Overridden by @%s", deps.author)
	if err := deps.gh.SetCommitStatus(ctx, pr.HeadRefOid, "success", github.CommitStatusContext, desc); err != nil {
		return fmt.Errorf("setting override commit status: %w", err)
	}

	body := fmt.Sprintf(
		"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> VAIrdict verdict **overridden by @%s**. "+
			"The `%s` status has been set to success on commit `%s`. "+
			"This does not merge the PR — merge manually or enable `auto-vairdict`.\n\n%s\n",
		github.LogoURL, deps.author, github.CommitStatusContext, shortSHA(pr.HeadRefOid), overrideMarker)
	if err := deps.gh.AddComment(ctx, prNumber, body); err != nil {
		return fmt.Errorf("posting override confirmation: %w", err)
	}
	slog.Info("approve override applied", "pr", prNumber, "author", deps.author, "sha", pr.HeadRefOid)
	return nil
}

// handleIgnoreMention dismisses the current failing verdict without
// claiming human approval. Semantically equivalent to "ignore this run,
// start fresh on the next push" — next push re-runs the judge because
// the commit status is keyed to the current HEAD SHA.
func handleIgnoreMention(ctx context.Context, deps handleCommentDeps, prNumber int) error {
	pr, err := deps.gh.FetchPR(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("fetching PR for ignore dismissal: %w", err)
	}
	if pr.HeadRefOid == "" {
		return fmt.Errorf("PR #%d has no head commit SHA; cannot set dismissal status", prNumber)
	}

	desc := fmt.Sprintf("Dismissed by @%s", deps.author)
	if err := deps.gh.SetCommitStatus(ctx, pr.HeadRefOid, "success", github.CommitStatusContext, desc); err != nil {
		return fmt.Errorf("setting dismissal commit status: %w", err)
	}

	body := fmt.Sprintf(
		"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Current VAIrdict verdict **dismissed by @%s** (no override claimed). "+
			"The next push to this branch will re-run the judge.\n\n%s\n",
		github.LogoURL, deps.author, ignoreMarker)
	if err := deps.gh.AddComment(ctx, prNumber, body); err != nil {
		return fmt.Errorf("posting dismissal confirmation: %w", err)
	}
	slog.Info("verdict dismissed", "pr", prNumber, "author", deps.author, "sha", pr.HeadRefOid)
	return nil
}

// defaultRunConcurrencyWindow is the default window for the `run` command's
// concurrency guard. A run that started more than this long ago is assumed
// to have died without posting a done marker.
const defaultRunConcurrencyWindow = 60 * time.Minute

// handleExplainMention answers a question about the PR changes using an LLM.
// The response is posted as a threaded reply. Read-only — no code changes.
func handleExplainMention(ctx context.Context, deps handleCommentDeps, prNumber int, question string) error {
	if strings.TrimSpace(question) == "" {
		body := fmt.Sprintf(
			"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Please provide a question after `@vairdict explain`. "+
				"Example: `@vairdict explain why was this function changed?`\n", github.LogoURL)
		return deps.gh.AddComment(ctx, prNumber, body)
	}

	diff, err := deps.gh.FetchPRDiff(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("fetching PR diff for explain: %w", err)
	}

	if deps.explainQuestion == nil {
		return fmt.Errorf("explainQuestion hook not configured")
	}

	answer, err := deps.explainQuestion(ctx, diff, question)
	if err != nil {
		errBody := fmt.Sprintf(
			"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Sorry, I couldn't answer that question: %v\n\n%s\n",
			github.LogoURL, err, explainMarker)
		if addErr := deps.gh.AddComment(ctx, prNumber, errBody); addErr != nil {
			return fmt.Errorf("posting explain error reply: %w", addErr)
		}
		return fmt.Errorf("running explain: %w", err)
	}

	body := fmt.Sprintf(
		"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> **Answer** (requested by @%s)\n\n%s\n\n%s\n",
		github.LogoURL, deps.author, answer, explainMarker)
	if err := deps.gh.AddComment(ctx, prNumber, body); err != nil {
		return fmt.Errorf("posting explain reply: %w", err)
	}
	slog.Info("explain answered", "pr", prNumber, "author", deps.author)
	return nil
}

// handleFixMention pushes a targeted code fix to the PR branch. Claude Code
// runs with the description scoped to the PR diff, commits the change, and
// pushes. A confirmation reply is posted with the commit SHA.
func handleFixMention(ctx context.Context, deps handleCommentDeps, prNumber int, description string) error {
	if strings.TrimSpace(description) == "" {
		body := fmt.Sprintf(
			"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Please describe the fix after `@vairdict fix`. "+
				"Example: `@vairdict fix add nil check before dereferencing config`\n", github.LogoURL)
		return deps.gh.AddComment(ctx, prNumber, body)
	}

	pr, err := deps.gh.FetchPR(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("fetching PR for fix: %w", err)
	}

	diff, err := deps.gh.FetchPRDiff(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("fetching PR diff for fix: %w", err)
	}

	if deps.fixCode == nil {
		return fmt.Errorf("fixCode hook not configured")
	}

	startBody := fmt.Sprintf(
		"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Working on fix requested by @%s…\n",
		github.LogoURL, deps.author)
	if err := deps.gh.AddComment(ctx, prNumber, startBody); err != nil {
		slog.Warn("failed to post fix start comment", "pr", prNumber, "error", err)
	}

	sha, err := deps.fixCode(ctx, pr, description, diff)
	if err != nil {
		errBody := fmt.Sprintf(
			"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Fix failed: %v\n\n%s\n",
			github.LogoURL, err, fixMarker)
		if addErr := deps.gh.AddComment(ctx, prNumber, errBody); addErr != nil {
			return fmt.Errorf("posting fix error reply: %w", addErr)
		}
		return fmt.Errorf("running fix: %w", err)
	}

	body := fmt.Sprintf(
		"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Fix pushed by @%s: `%s`\n\n%s\n",
		github.LogoURL, deps.author, shortSHA(sha), fixMarker)
	if err := deps.gh.AddComment(ctx, prNumber, body); err != nil {
		return fmt.Errorf("posting fix confirmation: %w", err)
	}
	slog.Info("fix pushed", "pr", prNumber, "author", deps.author, "sha", sha)
	return nil
}

// handleRunMention triggers the full plan→code→quality loop against the
// PR branch. A concurrency guard ensures only one run at a time per PR.
func handleRunMention(ctx context.Context, deps handleCommentDeps, prNumber int, intent string) error {
	if strings.TrimSpace(intent) == "" {
		body := fmt.Sprintf(
			"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Please provide an intent after `@vairdict run`. "+
				"Example: `@vairdict run \"add input validation\"`\n", github.LogoURL)
		return deps.gh.AddComment(ctx, prNumber, body)
	}

	// Concurrency guard: check if a run is already in progress.
	window := deps.runConcurrencyWindow
	if window == 0 {
		window = defaultRunConcurrencyWindow
	}

	// A run is active if we find a start marker but no done marker within
	// the window. Check start first, then done.
	hasStart, err := deps.gh.RecentCommentExists(ctx, prNumber, runStartMarker, window)
	if err != nil {
		slog.Warn("run concurrency check (start) failed, continuing", "pr", prNumber, "error", err)
	} else if hasStart {
		hasDone, doneErr := deps.gh.RecentCommentExists(ctx, prNumber, runDoneMarker, window)
		if doneErr != nil {
			slog.Warn("run concurrency check (done) failed, continuing", "pr", prNumber, "error", doneErr)
		} else if !hasDone {
			body := fmt.Sprintf(
				"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> A run is already in progress for this PR "+
					"(mention by @%s ignored). Please wait for the current run to finish.\n",
				github.LogoURL, deps.author)
			if addErr := deps.gh.AddComment(ctx, prNumber, body); addErr != nil {
				return fmt.Errorf("posting run concurrency reply: %w", addErr)
			}
			return nil
		}
	}

	pr, err := deps.gh.FetchPR(ctx, prNumber)
	if err != nil {
		return fmt.Errorf("fetching PR for run: %w", err)
	}

	if deps.runOrchestration == nil {
		return fmt.Errorf("runOrchestration hook not configured")
	}

	// Post start marker for concurrency guard.
	startBody := fmt.Sprintf(
		"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Starting full VAIrdict run on @%s's request: %s\n\n%s\n",
		github.LogoURL, deps.author, intent, runStartMarker)
	if err := deps.gh.AddComment(ctx, prNumber, startBody); err != nil {
		return fmt.Errorf("posting run start comment: %w", err)
	}

	runErr := deps.runOrchestration(ctx, pr, intent)

	// Post done marker regardless of success/failure so the concurrency
	// guard unblocks subsequent runs.
	var doneBody string
	if runErr != nil {
		doneBody = fmt.Sprintf(
			"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Run failed: %v\n\n%s\n",
			github.LogoURL, runErr, runDoneMarker)
	} else {
		doneBody = fmt.Sprintf(
			"<img src=\"%s\" alt=\"VAIrdict\" height=\"20\"> Run completed successfully.\n\n%s\n",
			github.LogoURL, runDoneMarker)
	}
	if addErr := deps.gh.AddComment(ctx, prNumber, doneBody); addErr != nil {
		slog.Warn("failed to post run done comment", "pr", prNumber, "error", addErr)
	}

	if runErr != nil {
		return fmt.Errorf("running orchestration: %w", runErr)
	}

	slog.Info("run completed", "pr", prNumber, "author", deps.author, "intent", intent)
	return nil
}

// shortSHA trims a commit OID to the conventional 7-char prefix used in
// PR UI. Defensive against short inputs so we never panic on bad data.
func shortSHA(sha string) string {
	if len(sha) <= 7 {
		return sha
	}
	return sha[:7]
}

// --- Production implementations for explain / fix / run hooks ---

// runExplainQuestion calls the resolved completer with the PR diff and
// question, returning a plain-text answer. The completer is resolved fresh
// from config so handle-comment doesn't need the full runTask setup.
func runExplainQuestion(ctx context.Context, diff, question string) (string, error) {
	cfg, err := config.LoadConfig("vairdict.yaml")
	if err != nil {
		return "", fmt.Errorf("loading config: %w", err)
	}
	client, _, err := resolveCompleter(cfg)
	if err != nil {
		return "", fmt.Errorf("resolving completer: %w", err)
	}
	system := "You are VAIrdict, an AI code review assistant. Answer the user's question " +
		"about the following PR diff concisely and accurately. Focus on the specific " +
		"changes in the diff.\n\n## PR Diff\n```\n" + diff + "\n```"
	var answer string
	if err := client.CompleteWithSystem(ctx, system, question, &answer); err != nil {
		return "", fmt.Errorf("LLM completion: %w", err)
	}
	return answer, nil
}

// runFixCode checks out the PR branch, runs Claude Code with the fix
// description, commits, pushes, and returns the new commit SHA.
func runFixCode(ctx context.Context, workDir string, pr *github.PRDetails, description, diff string) (string, error) {
	// Checkout the PR branch.
	if _, err := execCommandInDir(workDir, "git", "fetch", "origin", pr.HeadRefName); err != nil {
		return "", fmt.Errorf("fetching branch %s: %w", pr.HeadRefName, err)
	}
	if _, err := execCommandInDir(workDir, "git", "checkout", pr.HeadRefName); err != nil {
		return "", fmt.Errorf("checking out branch %s: %w", pr.HeadRefName, err)
	}

	// Build a prompt scoped to the diff.
	prompt := fmt.Sprintf(
		"You are working on a PR. Apply the following fix to the codebase.\n\n"+
			"## Fix Description\n%s\n\n## Current PR Diff (for context)\n```\n%s\n```\n\n"+
			"Make the minimal change needed. Do not refactor unrelated code.",
		description, diff)

	runner := claudecode.New()
	result, err := runner.Run(ctx, prompt, workDir)
	if err != nil {
		return "", fmt.Errorf("running claude code: %w", err)
	}
	if result.ExitCode != 0 {
		return "", fmt.Errorf("claude code exited with status %d: %s", result.ExitCode, result.Stderr)
	}

	// Stage, commit, push.
	if _, err := execCommandInDir(workDir, "git", "add", "-A"); err != nil {
		return "", fmt.Errorf("staging changes: %w", err)
	}
	status, err := execCommandInDir(workDir, "git", "status", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("checking status: %w", err)
	}
	if strings.TrimSpace(string(status)) == "" {
		return "", fmt.Errorf("no changes produced by fix")
	}

	msg := fmt.Sprintf("fix: %s\n\nApplied via @vairdict fix", description)
	if _, err := execCommandInDir(workDir, "git", "commit", "-m", msg); err != nil {
		return "", fmt.Errorf("committing fix: %w", err)
	}

	// Get the commit SHA.
	shaOut, err := execCommandInDir(workDir, "git", "rev-parse", "HEAD")
	if err != nil {
		return "", fmt.Errorf("reading commit SHA: %w", err)
	}
	sha := strings.TrimSpace(string(shaOut))

	if _, err := execCommandInDir(workDir, "git", "push", "origin", pr.HeadRefName); err != nil {
		return "", fmt.Errorf("pushing fix: %w", err)
	}

	return sha, nil
}

// runMentionOrchestration triggers the full plan→code→quality loop on the
// PR branch. It reuses the existing runTask infrastructure with the PR's
// branch already checked out.
func runMentionOrchestration(ctx context.Context, workDir string, pr *github.PRDetails, intent string) error {
	// Checkout the PR branch.
	if _, err := execCommandInDir(workDir, "git", "fetch", "origin", pr.HeadRefName); err != nil {
		return fmt.Errorf("fetching branch %s: %w", pr.HeadRefName, err)
	}
	if _, err := execCommandInDir(workDir, "git", "checkout", pr.HeadRefName); err != nil {
		return fmt.Errorf("checking out branch %s: %w", pr.HeadRefName, err)
	}

	// Run the task using the existing single-task path. The PR branch is
	// already checked out in workDir, so the orchestration produces code
	// on top of the existing PR.
	return runTask(intent, 0, ui.ModeCI, ui.ColorsNone, true, nil, "")
}
