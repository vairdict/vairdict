// Package main — review.go implements the `vairdict review <pr-number>`
// subcommand: it runs only the quality judge against an existing PR
// (human-written or otherwise) and posts a verdict comment. This lets us
// dogfood the judge on hand-written PRs without going through the full
// plan→code→quality loop.
//
// Flow:
//  1. Fetch the PR via gh (title, body, head/base ref)
//  2. Resolve the linked issue from the body (Closes/Fixes/Resolves #N)
//     and use its title+body as the intent — or fall back to --intent.
//  3. Fetch the PR diff via gh (no checkout — keeps the user's tree clean)
//  4. Run the quality judge with the diff stuffed into the prompt context
//  5. Post the verdict via github.PostVerdict (or print to stdout when
//     --no-comment is set).
//
// Exits non-zero on judge failure so it can gate CI workflows.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"strconv"

	"github.com/spf13/cobra"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/github"
	qualityjudge "github.com/vairdict/vairdict/internal/judges/quality"
	"github.com/vairdict/vairdict/internal/state"
)

var (
	reviewIntentFlag    string
	reviewNoCommentFlag bool
)

var reviewCmd = &cobra.Command{
	Use:   "review <pr-number>",
	Short: "Run the quality judge against an existing PR",
	Long: `Fetch an existing PR and run only the quality judge against it,
posting a structured verdict comment. The intent is derived from the
issue linked in the PR body (Closes/Fixes #N); use --intent to override
or supply one when no issue is linked.

Use --no-comment to print the verdict to stdout instead of posting it
on the PR (useful for local dry-runs).`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		prNumber, err := strconv.Atoi(args[0])
		if err != nil || prNumber <= 0 {
			return fmt.Errorf("invalid PR number %q", args[0])
		}
		return runReview(prNumber)
	},
}

func init() {
	reviewCmd.Flags().StringVar(&reviewIntentFlag, "intent", "", "explicit intent text (required if PR has no linked issue)")
	reviewCmd.Flags().BoolVar(&reviewNoCommentFlag, "no-comment", false, "print verdict to stdout instead of posting on the PR")
	rootCmd.AddCommand(reviewCmd)
}

// reviewDeps bundles the collaborators that runReviewWith needs so the
// command body can be exercised in tests with fakes (no exec, no real
// completer, no PROGRESS-changing side effects).
type reviewDeps struct {
	gh     reviewGH
	judge  reviewJudge
	cfg    *config.Config
	stdout *os.File
}

// reviewGH is the narrow GitHub surface the review command depends on.
// Both *github.Client and a test fake satisfy it.
type reviewGH interface {
	FetchPR(ctx context.Context, n int) (*github.PRDetails, error)
	FetchIssue(ctx context.Context, n int) (*github.IssueDetails, error)
	FetchPRDiff(ctx context.Context, n int) (string, error)
	PostVerdict(ctx context.Context, n int, v *state.Verdict, p state.Phase, loop int) error
}

// reviewJudge is the narrow judge surface; *quality.QualityJudge satisfies
// it. Plan is always empty for review mode (per #48 acceptance criteria).
type reviewJudge interface {
	Judge(ctx context.Context, intent string, plan string, workDir string) (*state.Verdict, error)
}

// runReview is the production entry point: it loads the config, builds
// the real github client + quality judge, and delegates to runReviewWith
// which contains the testable orchestration logic.
func runReview(prNumber int) error {
	overlayPath, err := config.ResolveOverlayPath(envFlag, config.IsCI(), ".", fileExistsFunc)
	if err != nil {
		return fmt.Errorf("resolving env: %w", err)
	}
	cfg, err := config.LoadConfigWithOverlay("vairdict.yaml", overlayPath)
	if err != nil {
		return fmt.Errorf("loading config: %w", err)
	}

	client, _, err := resolveCompleter(cfg)
	if err != nil {
		return err
	}

	workDir, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("resolving working directory: %w", err)
	}
	ghClient := github.New(&github.ExecRunner{Dir: workDir})
	judge := qualityjudge.New(client, &qualityjudge.ExecRunner{}, *cfg)

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	return runReviewWith(ctx, prNumber, reviewDeps{
		gh:     ghClient,
		judge:  judge,
		cfg:    cfg,
		stdout: os.Stdout,
	})
}

// runReviewWith is the orchestration core, factored out so tests can
// inject fakes without touching the filesystem or network. It returns a
// non-nil error on failed verdicts so the caller (cobra) propagates a
// non-zero exit code — review is meant to gate CI.
func runReviewWith(ctx context.Context, prNumber int, deps reviewDeps) error {
	pr, err := deps.gh.FetchPR(ctx, prNumber)
	if err != nil {
		return err
	}

	intent, err := resolveReviewIntent(ctx, deps.gh, pr)
	if err != nil {
		return err
	}

	diff, err := deps.gh.FetchPRDiff(ctx, prNumber)
	if err != nil {
		return err
	}

	// The QualityJudge.Judge signature is locked at (intent, plan, workDir)
	// — see issue #48 acceptance criteria. Review mode has no plan, so we
	// pass an empty plan and embed the diff into the workDir context line
	// of the prompt by stuffing the diff into the intent string under a
	// clearly delimited section. This avoids changing the judge API for
	// a single command. The judge prompt already accepts free-form text.
	enrichedIntent := fmt.Sprintf("%s\n\n## PR Diff (review mode)\n```diff\n%s\n```", intent, diff)

	verdict, err := deps.judge.Judge(ctx, enrichedIntent, "", ".")
	if err != nil {
		return fmt.Errorf("running quality judge: %w", err)
	}

	if reviewNoCommentFlag {
		_, _ = fmt.Fprintln(deps.stdout, github.FormatVerdictComment(verdict, state.PhaseQuality, 1))
	} else {
		if err := deps.gh.PostVerdict(ctx, prNumber, verdict, state.PhaseQuality, 1); err != nil {
			return fmt.Errorf("posting verdict: %w", err)
		}
	}

	if !verdict.Pass {
		return fmt.Errorf("verdict failed: score %.0f%%", verdict.Score)
	}
	return nil
}

// resolveReviewIntent picks the intent for the judge: explicit --intent
// flag wins; otherwise the first linked issue in the PR body is fetched
// and rendered as "title\n\nbody". Errors out cleanly when neither
// source is available so the user gets a clear next step.
func resolveReviewIntent(ctx context.Context, gh reviewGH, pr *github.PRDetails) (string, error) {
	if reviewIntentFlag != "" {
		return reviewIntentFlag, nil
	}
	issueNum := github.ParseLinkedIssue(pr.Body)
	if issueNum == 0 {
		return "", fmt.Errorf("PR #%d has no linked issue (Closes/Fixes/Resolves #N) and --intent was not provided", pr.Number)
	}
	iss, err := gh.FetchIssue(ctx, issueNum)
	if err != nil {
		return "", err
	}
	return iss.Title + "\n\n" + iss.Body, nil
}
