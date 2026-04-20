// Package plan implements the plan phase judge, which evaluates whether a
// generated plan sufficiently covers the stated intent. It uses the Claude API
// to identify gaps and questions, then computes a deterministic score from the
// gap severities rather than asking the model for a number.
package plan

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
	"github.com/vairdict/vairdict/internal/judges/verdictschema"
)

// Completer is the interface for sending prompts to an LLM. Plan judge uses
// tool-use exclusively so responses conform to a strict schema.
type Completer interface {
	CompleteWithTool(ctx context.Context, system, prompt string, tool claude.Tool, target any) error
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

You respond by invoking the submit_verdict tool. The tool's schema is the
single source of truth for the response shape — do not emit free-form JSON,
markdown fences, or prose outside the tool call.

Severity levels for gaps:
- P0: next steps cannot proceed without resolving this — critical blocker
- P1: required, must be addressed this loop — important but not a total blocker
- P2: ambiguous, agent will document assumption and proceed — not blocking
- P3: nice to have, deferred to future issue — not blocking

Do NOT set "blocking" on gaps and do NOT estimate a score — the orchestrator
computes both deterministically from severities.

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

A concern goes in EXACTLY ONE of "gaps" or "questions". A "question" is only
for genuine uncertainty you cannot resolve from the plan; if you can state it
as a finding, use a gap.

## Examples

### Example 1 — clear pass

Intent: "Add a CLI flag --quiet that suppresses non-error output."
Plan: "Add a BoolP flag 'quiet' to the run command in cmd/vairdict/run.go.
When set, route the renderer constructor through ui.NewQuiet() instead of
ui.NewCLI(). Tests: new test case in run_test.go covering --quiet."

submit_verdict input:
{
  "summary": "## Decided\n- Thread --quiet through the existing renderer factory\n## Files to touch\n- cmd/vairdict/run.go — flag plumbing\n- cmd/vairdict/run_test.go — quiet-mode coverage",
  "gaps": [],
  "questions": []
}

### Example 2 — clear fail

Intent: "Persist task state across restarts."
Plan: "We will store tasks in memory and print them on exit."

submit_verdict input:
{
  "summary": "## Risks\n- Plan does not satisfy the persistence requirement",
  "gaps": [
    {"severity": "P0", "description": "In-memory storage is lost on restart — intent explicitly requires persistence across restarts."},
    {"severity": "P1", "description": "No mention of a storage backend, schema, or migration strategy."}
  ],
  "questions": []
}`

// Judge evaluates a plan against an intent and returns a Verdict.
// Pass is determined by whether the score meets the configured coverage
// threshold AND there are no blocking gaps. Blocking is assigned from the
// configured severity block-on list, not the LLM's opinion.
func (j *PlanJudge) Judge(ctx context.Context, intent string, plan string) (*state.Verdict, error) {
	prompt := fmt.Sprintf("## Intent\n%s\n\n## Plan\n%s", intent, plan)

	var verdict state.Verdict
	tool := verdictschema.VerdictTool("Submit the plan judge verdict as a structured object. Omit score, pass, and blocking — they are computed from the gap severities.")
	if err := j.client.CompleteWithTool(ctx, systemPrompt, prompt, tool, &verdict); err != nil {
		return nil, fmt.Errorf("judging plan: %w", err)
	}

	// Always pass a non-nil map — empty BlockOn must mean "nothing blocks",
	// not "fall back to default P0+P1".
	blockSet := toSet(j.cfg.Severity.BlockOn)
	if blockSet == nil {
		blockSet = map[string]bool{}
	}
	assumeSet := toSet(j.cfg.Severity.AssumeOn)
	deferSet := toSet(j.cfg.Severity.DeferOn)

	verdictschema.ApplyBlocking(verdict.Gaps, blockSet)

	for _, g := range verdict.Gaps {
		sev := string(g.Severity)
		if assumeSet[sev] {
			slog.Info("gap logged as assumption, not blocking",
				"severity", sev,
				"description", g.Description,
			)
		}
		if deferSet[sev] {
			slog.Info("gap deferred to future issue",
				"severity", sev,
				"description", g.Description,
			)
		}
	}

	verdict.Score = verdictschema.ComputeScore(verdict.Gaps)
	verdict.Pass = verdict.Score >= j.cfg.CoverageThreshold && !verdictschema.HasBlockingGap(verdict.Gaps)

	return &verdict, nil
}

func toSet(items []string) map[string]bool {
	if len(items) == 0 {
		return nil
	}
	s := make(map[string]bool, len(items))
	for _, item := range items {
		s[item] = true
	}
	return s
}
