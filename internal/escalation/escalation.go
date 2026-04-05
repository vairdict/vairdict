// Package escalation handles notification and state management when a task
// exhausts its retry loops in any phase without passing.
package escalation

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// GitHubCommenter posts comments on pull requests.
type GitHubCommenter interface {
	AddComment(ctx context.Context, prNumber int, body string) error
}

// EscalationInfo holds the details needed to escalate a task.
type EscalationInfo struct {
	Task      *state.Task
	Phase     state.Phase
	Loops     int
	LastScore float64
	Verdict   *state.Verdict
	PRNumber  int
}

// Escalate transitions the task to the escalated state and sends notifications
// according to the escalation config. It returns an error only if the state
// transition fails; notification failures are logged but not fatal.
func Escalate(ctx context.Context, info EscalationInfo, cfg config.EscalationConfig, w io.Writer, gh GitHubCommenter) error {
	// Transition task to escalated state if not already there.
	if info.Task.State != state.StateEscalated {
		if err := info.Task.Transition(state.StateEscalated); err != nil {
			return fmt.Errorf("transitioning to escalated: %w", err)
		}
	}

	summary := FormatSummary(info)

	// Send notifications based on config.
	channels := strings.Split(cfg.NotifyVia, ",")
	for _, ch := range channels {
		ch = strings.TrimSpace(ch)
		switch ch {
		case "stdout", "":
			notifyStdout(w, summary)
		case "github":
			notifyGitHub(ctx, gh, info.PRNumber, summary)
		default:
			slog.Warn("unknown escalation channel", "channel", ch)
		}
	}

	return nil
}

// FormatSummary builds a human-readable escalation summary.
func FormatSummary(info EscalationInfo) string {
	var b strings.Builder

	fmt.Fprintf(&b, "## Escalation: %s phase failed\n\n", info.Phase)
	fmt.Fprintf(&b, "**Task:** %s\n", info.Task.ID)
	fmt.Fprintf(&b, "**Intent:** %s\n", info.Task.Intent)
	fmt.Fprintf(&b, "**Phase:** %s\n", info.Phase)
	fmt.Fprintf(&b, "**Loops attempted:** %d\n", info.Loops)
	fmt.Fprintf(&b, "**Last score:** %.0f%%\n", info.LastScore)
	fmt.Fprintf(&b, "**Time:** %s\n", time.Now().UTC().Format(time.RFC3339))

	if info.Verdict != nil && len(info.Verdict.Gaps) > 0 {
		b.WriteString("\n### Blocking gaps\n")
		for _, gap := range info.Verdict.Gaps {
			if gap.Blocking {
				fmt.Fprintf(&b, "- [%s] %s\n", gap.Severity, gap.Description)
			}
		}

		hasNonBlocking := false
		for _, gap := range info.Verdict.Gaps {
			if !gap.Blocking {
				if !hasNonBlocking {
					b.WriteString("\n### Non-blocking gaps\n")
					hasNonBlocking = true
				}
				fmt.Fprintf(&b, "- [%s] %s\n", gap.Severity, gap.Description)
			}
		}
	}

	if info.Verdict != nil && len(info.Verdict.Questions) > 0 {
		b.WriteString("\n### Open questions\n")
		for _, q := range info.Verdict.Questions {
			fmt.Fprintf(&b, "- [%s] %s\n", q.Priority, q.Text)
		}
	}

	b.WriteString("\nHuman intervention required to proceed.\n")

	return b.String()
}

func notifyStdout(w io.Writer, summary string) {
	if _, err := fmt.Fprintf(w, "\n%s", summary); err != nil {
		slog.Error("failed to write escalation to stdout", "error", err)
	}
}

func notifyGitHub(ctx context.Context, gh GitHubCommenter, prNumber int, summary string) {
	if gh == nil {
		slog.Warn("github escalation skipped: no github client provided")
		return
	}
	if prNumber == 0 {
		slog.Warn("github escalation skipped: no PR number available")
		return
	}
	if err := gh.AddComment(ctx, prNumber, summary); err != nil {
		slog.Error("failed to post escalation comment", "pr", prNumber, "error", err)
	}
}
