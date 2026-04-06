// Package plan implements the plan phase judge, which evaluates whether a
// generated plan sufficiently covers the stated intent. It uses the Claude API
// to score the plan and identify gaps at varying severity levels.
package plan

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// Completer is the interface for sending prompts to an LLM and receiving
// typed responses. Both claude.Client and claude.FakeClient satisfy this.
type Completer interface {
	CompleteWithSystem(ctx context.Context, system, prompt string, target any) error
}

// PlanJudge evaluates a plan against an intent and returns a typed Verdict.
type PlanJudge struct {
	client Completer
	cfg    config.PlanPhaseConfig
}

// New creates a PlanJudge with the given client and plan phase configuration.
func New(client Completer, cfg config.PlanPhaseConfig) *PlanJudge {
	return &PlanJudge{
		client: client,
		cfg:    cfg,
	}
}

const systemPrompt = `You are a plan judge for a software development process engine.
Your job is to evaluate whether a proposed plan adequately covers the stated intent.

You MUST respond with valid JSON only — no markdown, no explanation outside the JSON.

Severity levels for gaps:
- P0: next steps cannot proceed without resolving this — critical blocker
- P1: required, must be addressed this loop — important but not a total blocker
- P2: ambiguous, agent will document assumption and proceed — not blocking
- P3: nice to have, deferred to future issue — not blocking

For each gap, set "blocking" to true only for P0 and P1 severity.

Score is a float from 0 to 100 representing how well the plan covers the intent.
Higher scores mean better coverage.

The "summary" field is a short human-readable narrative in markdown-ish form
that will be rendered under the plan phase header in the CLI. Use these exact
sub-section headers (omit a section if empty), with "- " bullet items:

## Decided
- <locked-in design decision>

## Risks
- <risk or open question the planner should know>

## Files to touch
- <path/to/file.go — brief reason>

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
      "text": "<question for the planner>",
      "priority": "<high|medium|low>"
    }
  ]
}`

// Judge evaluates a plan against an intent and returns a Verdict.
// Pass is determined by whether the score meets the configured coverage threshold.
// Blocking gaps are set based on the configured severity block-on list.
func (j *PlanJudge) Judge(ctx context.Context, intent string, plan string) (*state.Verdict, error) {
	prompt := fmt.Sprintf("## Intent\n%s\n\n## Plan\n%s", intent, plan)

	var verdict state.Verdict
	if err := j.client.CompleteWithSystem(ctx, systemPrompt, prompt, &verdict); err != nil {
		return nil, fmt.Errorf("judging plan: %w", err)
	}

	// Build lookup sets from config.
	blockSet := toSet(j.cfg.Severity.BlockOn)
	assumeSet := toSet(j.cfg.Severity.AssumeOn)
	deferSet := toSet(j.cfg.Severity.DeferOn)

	// Enforce blocking based on config, not the LLM's opinion.
	for i := range verdict.Gaps {
		sev := string(verdict.Gaps[i].Severity)
		verdict.Gaps[i].Blocking = blockSet[sev]

		if assumeSet[sev] {
			slog.Info("gap logged as assumption, not blocking",
				"severity", sev,
				"description", verdict.Gaps[i].Description,
			)
		}
		if deferSet[sev] {
			slog.Info("gap deferred to future issue",
				"severity", sev,
				"description", verdict.Gaps[i].Description,
			)
		}
	}

	// Enforce pass based on config threshold, not the LLM's opinion.
	verdict.Pass = verdict.Score >= j.cfg.CoverageThreshold

	return &verdict, nil
}

func toSet(items []string) map[string]bool {
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}
