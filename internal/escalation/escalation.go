// Package escalation handles loop-limit exhaustion: when a phase fails after
// max attempts, this package transitions the task to the escalated state and
// notifies a human via the configured channel (stdout or GitHub PR comment).
//
// This package intentionally does not import internal/github to avoid coupling.
// Callers pass a PRCommenter (which *github.Client satisfies) when notifying
// via the github channel.
package escalation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// PRCommenter is the minimal interface required to post a comment on a PR.
// *github.Client satisfies this; tests provide a fake implementation.
type PRCommenter interface {
	AddComment(ctx context.Context, prNumber int, body string) error
}

// Result is the data the escalator needs from a failing phase.
type Result struct {
	Phase     state.Phase
	Loops     int
	LastScore float64
	Gaps      []state.Gap
	// PRNumber is optional. When zero and NotifyVia is "github", the
	// escalator falls back to stdout so the message is never lost.
	PRNumber int
}

// Escalate transitions the task to the escalated state, formats a summary,
// and dispatches it via the configured channel.
//
// out is used for stdout-channel writes (and as the github-channel fallback
// when no PR exists). gh is required only when NotifyVia is "github" and
// PRNumber is non-zero; it may otherwise be nil.
func Escalate(
	ctx context.Context,
	task *state.Task,
	result Result,
	cfg config.EscalationConfig,
	out io.Writer,
	gh PRCommenter,
) error {
	if task == nil {
		return fmt.Errorf("escalate: task is nil")
	}
	if out == nil {
		return fmt.Errorf("escalate: out writer is nil")
	}

	if task.State != state.StateEscalated {
		if err := task.Transition(state.StateEscalated); err != nil {
			return fmt.Errorf("transitioning task %s to escalated: %w", task.ID, err)
		}
	}

	summary := FormatSummary(task, result)

	channel := cfg.NotifyVia
	if channel == "" {
		channel = "stdout"
	}

	switch channel {
	case "stdout":
		if _, err := fmt.Fprintln(out, summary); err != nil {
			return fmt.Errorf("writing escalation to stdout: %w", err)
		}
	case "github":
		if result.PRNumber == 0 {
			slog.Warn("github escalation requested but no PR number — falling back to stdout",
				"task", task.ID,
			)
			if _, err := fmt.Fprintln(out, summary); err != nil {
				return fmt.Errorf("writing escalation fallback to stdout: %w", err)
			}
		} else {
			if gh == nil {
				return fmt.Errorf("github escalation requested but no PR commenter provided")
			}
			if err := gh.AddComment(ctx, result.PRNumber, summary); err != nil {
				return fmt.Errorf("posting escalation comment to PR #%d: %w", result.PRNumber, err)
			}
		}
	default:
		return fmt.Errorf("unsupported escalation channel %q (supported: stdout, github)", channel)
	}

	slog.Info("task escalated",
		"task", task.ID,
		"phase", result.Phase,
		"loops", result.Loops,
		"last_score", result.LastScore,
		"channel", channel,
	)
	return nil
}

// FormatSummary builds a human-readable escalation message describing the
// failure: which phase, how many loops, last score, and any blocking gaps.
// Output is plain markdown so it renders nicely as a GitHub comment.
func FormatSummary(task *state.Task, result Result) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## ⚠️ VAIrdict Escalation\n\n")
	fmt.Fprintf(&b, "**Task:** `%s`\n", task.ID)
	if task.Intent != "" {
		fmt.Fprintf(&b, "**Intent:** %s\n", task.Intent)
	}
	if result.Phase != "" {
		fmt.Fprintf(&b, "**Failed phase:** %s\n", result.Phase)
	}
	fmt.Fprintf(&b, "**Loops used:** %d\n", result.Loops)
	fmt.Fprintf(&b, "**Last score:** %.0f%%\n\n", result.LastScore)

	blocking := make([]state.Gap, 0, len(result.Gaps))
	for _, g := range result.Gaps {
		if g.Blocking {
			blocking = append(blocking, g)
		}
	}

	if len(blocking) > 0 {
		b.WriteString("### Blocking gaps\n")
		for _, g := range blocking {
			fmt.Fprintf(&b, "- **[%s]** %s\n", g.Severity, g.Description)
		}
	} else {
		b.WriteString("_No blocking gaps recorded._\n")
	}

	b.WriteString("\nHuman intervention required.\n")
	return b.String()
}
