// Package quality implements the quality phase judge, which evaluates whether
// completed code fulfills the original task intent and optionally runs e2e tests.
// It uses the Claude API for intent verification and produces a typed Verdict.
package quality

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// Completer is the interface for sending prompts to an LLM and receiving
// typed responses. Both claude.Client and claude.FakeClient satisfy this.
type Completer interface {
	CompleteWithSystem(ctx context.Context, system, prompt string, target any) error
}

// CommandRunner executes a command and returns its output and error.
// Injected for testing.
type CommandRunner interface {
	Run(ctx context.Context, workDir string, name string, args ...string) ([]byte, error)
}

// ExecRunner is the real implementation using os/exec.
type ExecRunner struct{}

// Run executes a command in the given directory.
func (e *ExecRunner) Run(ctx context.Context, workDir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// QualityJudge evaluates whether completed code fulfills the original task
// intent and optionally runs e2e tests. It combines AI-based intent
// verification with command-based e2e testing to produce a comprehensive Verdict.
type QualityJudge struct {
	client Completer
	runner CommandRunner
	cfg    config.Config
}

// New creates a QualityJudge with the given client, command runner, and config.
func New(client Completer, runner CommandRunner, cfg config.Config) *QualityJudge {
	return &QualityJudge{
		client: client,
		runner: runner,
		cfg:    cfg,
	}
}

const systemPrompt = `You are a quality judge for a software development process engine.
Your job is to evaluate whether implemented code fulfills the original task intent.

You are given the original intent, the approved plan, and a summary of the work directory.
Evaluate whether the implementation matches the intent and plan.

You MUST respond with valid JSON only — no markdown, no explanation outside the JSON.

Severity levels for gaps:
- P0: intent mismatch — the code does not solve the stated problem or is fundamentally wrong
- P1: significant gap — major feature or requirement is missing or broken
- P2: minor issue — small improvements, edge cases, or polish items
- P3: nice to have — deferred to future work

For each gap, set "blocking" to true only for P0 and P1 severity.

Score is a float from 0 to 100 representing how well the implementation fulfills the intent.
Set pass to true if the implementation adequately fulfills the intent (score >= 70).

The "summary" field is a short human-readable narrative in markdown-ish form
that will be rendered under the quality phase header in the CLI. Use these
exact sub-section headers (omit a section if empty), with "- " bullet items:

## Reviewed
- <what you checked against the intent/plan>

## Notes
- <observation, caveat, or follow-up worth surfacing>

Keep each bullet to one line. Do not include any other sections or prose.

Respond with this exact JSON structure:
{
  "score": <float 0-100>,
  "pass": <bool>,
  "summary": "<markdown-ish narrative as described above>",
  "gaps": [
    {
      "severity": "<P0|P1|P2|P3>",
      "description": "<what is missing or wrong>",
      "blocking": <bool>
    }
  ],
  "questions": [
    {
      "text": "<question about the implementation>",
      "priority": "<high|medium|low>"
    }
  ]
}`

// Judge evaluates whether the code in workDir fulfills the given intent and plan.
// It runs AI-based intent verification and optionally e2e tests, returning a
// combined Verdict.
func (j *QualityJudge) Judge(ctx context.Context, intent string, plan string, workDir string) (*state.Verdict, error) {
	// Step 1: AI intent verification.
	aiVerdict, err := j.evaluateIntent(ctx, intent, plan, workDir)
	if err != nil {
		return nil, fmt.Errorf("evaluating intent: %w", err)
	}

	// Step 2: Run e2e tests if configured.
	if j.cfg.Phases.Quality.E2ERequired && j.cfg.Commands.E2E != "" {
		e2eGap := j.runE2E(ctx, workDir)
		if e2eGap != nil {
			aiVerdict.Gaps = append(aiVerdict.Gaps, *e2eGap)
			// Penalize score for e2e failure: reduce by 30 points, floor at 0.
			aiVerdict.Score = max(0, aiVerdict.Score-30)
		}
	}

	// Enforce pass threshold: score >= 70.
	aiVerdict.Pass = aiVerdict.Score >= 70

	slog.Info("quality judge verdict",
		"score", aiVerdict.Score,
		"pass", aiVerdict.Pass,
		"gaps", len(aiVerdict.Gaps),
	)

	return aiVerdict, nil
}

// evaluateIntent uses the Claude API to assess whether the code matches the intent.
func (j *QualityJudge) evaluateIntent(ctx context.Context, intent string, plan string, workDir string) (*state.Verdict, error) {
	prompt := fmt.Sprintf("## Original Intent\n%s\n\n## Approved Plan\n%s\n\n## Work Directory\n%s",
		intent, plan, workDir)

	var verdict state.Verdict
	if err := j.client.CompleteWithSystem(ctx, systemPrompt, prompt, &verdict); err != nil {
		return nil, fmt.Errorf("calling completer: %w", err)
	}

	return &verdict, nil
}

// runE2E executes the configured e2e command and returns a Gap if it fails, or nil on success.
func (j *QualityJudge) runE2E(ctx context.Context, workDir string) *state.Gap {
	parts := strings.Fields(j.cfg.Commands.E2E)
	if len(parts) == 0 {
		return nil
	}

	slog.Debug("running e2e tests", "command", j.cfg.Commands.E2E)

	output, err := j.runner.Run(ctx, workDir, parts[0], parts[1:]...)
	if err != nil {
		outStr := string(output)
		slog.Info("e2e tests failed", "output", truncate(outStr, 200))
		return &state.Gap{
			Severity:    state.SeverityP1,
			Description: fmt.Sprintf("e2e tests failed: %s", truncate(strings.TrimSpace(outStr), 500)),
			Blocking:    true,
		}
	}

	slog.Debug("e2e tests passed")
	return nil
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}

func max(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
