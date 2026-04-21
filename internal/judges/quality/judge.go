// Package quality implements the quality phase judge, which evaluates whether
// completed code fulfills the original task intent and optionally runs e2e tests.
// It uses the Claude API for intent verification via tool-use and produces a
// typed Verdict with a deterministic score computed from gap severities.
package quality

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/judges/verdictschema"
	"github.com/vairdict/vairdict/internal/standards"
	"github.com/vairdict/vairdict/internal/state"
)

// Completer is the interface for sending prompts to an LLM. Quality judge uses
// tool-use exclusively so responses conform to a strict schema.
type Completer interface {
	CompleteWithTool(ctx context.Context, system, prompt string, tool claude.Tool, target any) error
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

// PassThreshold is the minimum score a quality verdict must reach to pass.
// Because scores are computed deterministically from gap severities, this is
// a fixed tuning knob rather than a config value.
const PassThreshold = 70.0

// QualityJudge evaluates whether completed code fulfills the original task
// intent and optionally runs e2e tests. It combines AI-based intent
// verification with command-based e2e testing to produce a comprehensive Verdict.
type QualityJudge struct {
	client    Completer
	runner    CommandRunner
	cfg       config.Config
	codeFacts string
}

// New creates a QualityJudge with the given client, command runner, and config.
func New(client Completer, runner CommandRunner, cfg config.Config) *QualityJudge {
	return &QualityJudge{
		client: client,
		runner: runner,
		cfg:    cfg,
	}
}

// WithCodeFacts returns a judge that will inject the given facts block into
// the prompt. Facts come from the preceding code phase (lint/test/build via
// spm ship) so the LLM does not re-evaluate objective checks.
func (j *QualityJudge) WithCodeFacts(facts string) *QualityJudge {
	cp := *j
	cp.codeFacts = facts
	return &cp
}

const systemPromptCore = `You are an experienced senior code reviewer acting as a quality judge
for a software development process engine. Your job is to evaluate
whether the implemented code fulfills the original task intent.

You care about correctness, clarity, and future maintenance pain. You are
considered and deliberate — you comment when it matters and stay quiet
when it does not. Silence on trivia is a feature, not a bug: you would
rather miss a nit than add noise. Flag things that would cause a bug, a
regression, or real maintenance pain; don't flag things a thoughtful
reviewer would let slide.

You respond by invoking the submit_verdict tool. The tool's schema is the
single source of truth for the response shape — do not emit free-form JSON,
markdown fences, or prose outside the tool call.

You are given the original intent, the approved plan, and the unified diff
of the changes that were made. Evaluate whether the diff actually
implements the intent and plan. Base every observation on what the diff
shows — never invent file contents that are not in the diff.

## Do NOT re-evaluate objective checks

Tests, lint, format, and build have already been verified by the code judge
(spm ship). If a "## Facts (from code judge)" section is provided in the user
message, trust it. Do NOT:
- raise gaps about tests failing / not compiling / formatting
- speculate whether the code builds
- suggest running the test suite

Focus on: intent fulfillment, plan alignment, correctness bugs,
security, code reuse, and style — things the code judge does not check.

## Critical: the diff is PARTIAL

The diff shows ONLY the changed lines, not the entire codebase. Any function,
type, variable, or import that is called/referenced in the diff but NOT
defined in the diff ALREADY EXISTS in the codebase. This is normal — the
diff is a patch, not a complete program.

You MUST NOT:
- Flag a function as "missing" or "undefined" because its definition is not in the diff
- Flag a "compilation error" for a symbol not defined in the diff
- Raise a question asking whether a function "exists elsewhere"
- Treat a missing-from-diff symbol as a gap of ANY severity

These are NOT bugs. They are existing code that was not modified.

## Severity levels for gaps

- P0: intent mismatch — the code does not solve the stated problem or is fundamentally wrong
- P1: significant gap — major feature or requirement is missing or broken.
  This includes any correctness bug in production code OR test code, such as:
  tautological assertions (e.g. errors.Is(err, err)), unreachable branches,
  tests that can never fail, wrong variable compared, dead code that masks
  missing coverage.
  NEVER use P1 for a symbol that is referenced but not defined in the diff —
  that symbol exists in the codebase already.
- P2: minor issue — style, naming, docs, minor edge cases that do not affect correctness
- P3: nice to have — deferred to future work

Do NOT set "blocking" on gaps and do NOT estimate a score — the orchestrator
computes both deterministically from severities.
A correctness bug is ALWAYS at least P1, never P2 — even if it is in test code.

## Additional checks

In addition to intent/plan alignment, scan the diff for the following.

### Security (P1 blocking)
Flag any of these patterns visible in the diff:
- Hardcoded secrets, API keys, tokens, or passwords (look for string literals
  assigned to variables named key, secret, token, password, etc.)
- SQL injection: string concatenation or fmt.Sprintf used to build queries
  instead of parameterised queries
- Command injection: unsanitised user input passed to exec.Command, os/exec,
  subprocess, or shell invocations
- Path traversal: user-controlled input used in file paths without sanitisation
- Broken authentication or missing authorisation checks on new endpoints
- Use of known-insecure crypto (MD5, SHA1 for security purposes, DES, RC4)
- Disabled TLS verification or certificate checks

Only flag what is actually visible in the diff.

### Code reuse (P2 non-blocking)
Flag duplicated or copy-pasted logic visible in the diff:
- Two or more new functions/methods with near-identical bodies (>5 lines)
- Copy-pasted blocks that differ only in variable names or literals
- Re-implementation of logic that clearly exists in the same diff

### Style & maintainability (P3 non-blocking)
Flag readability and maintainability issues visible in the diff:
- Functions longer than ~80 lines (suggest splitting)
- Magic numbers or string literals that should be named constants
- Confusing or misleading variable/function names
- Deeply nested control flow (>3 levels) that could be simplified
- Missing error handling where errors are silently discarded (e.g. _ = fn())

## Summary

The "summary" field is a short human-readable narrative in markdown-ish form
that will be rendered under the quality phase header in the CLI. Use these
exact sub-section headers (omit a section if empty), with "- " bullet items:

## Reviewed
- <what you checked against the intent/plan>

## Notes
- <observation, caveat, or follow-up worth surfacing>

Keep each bullet to one line. Do not include any other sections or prose.

## Output rules

1. Each concern goes in EXACTLY ONE array — either "gaps" or "questions", never both.
2. A "question" is ONLY for genuine uncertainty you cannot resolve from the diff.
3. Never create a gap or question about a symbol not defined in the diff — it exists.
4. For gaps tied to a specific diff line, set "file" (b/ side) and "line" (+ side).
   Omit or set to "" / 0 for architectural gaps that span multiple files.

## Examples

### Example 1 — pass with one considered observation

Intent: "Add a --dry-run flag to vairdict run that skips PR creation."
Facts: tests pass, lint clean, build ok.
Diff (abridged): "+ var dryRun bool ... if !dryRun { openPR(...) }" plus test coverage.

submit_verdict input:
{
  "summary": "## Reviewed\n- --dry-run flag wiring in run.go\n- test covering the dry-run branch",
  "gaps": [
    {"severity": "P3", "description": "The --dry-run logging prints 'would open PR' but no URL preview; a reader running dry-run gets a weaker signal than they could.", "file": "cmd/vairdict/run.go", "line": 588}
  ],
  "questions": []
}

Note: a single, useful P3 observation is preferable to empty gaps when
something genuinely worth mentioning exists — but never pad with nits
just to avoid empty gaps. If a diff is genuinely clean, leave gaps empty.

### Example 2 — clear fail (intent mismatch + security)

Intent: "Add basic auth to the admin endpoint."
Facts: tests pass, lint clean, build ok.
Diff (abridged): "+ admin.HandleFunc('/admin', handler) ... + const apiKey = \"sk-live-abc123\""

submit_verdict input:
{
  "summary": "## Reviewed\n- admin route wiring and literal credential\n## Notes\n- Hardcoded key must move to env or config",
  "gaps": [
    {"severity": "P0", "description": "No authentication middleware on /admin — intent requires basic auth."},
    {"severity": "P1", "description": "Hardcoded API key in source (apiKey = 'sk-live-...'). Move to environment variable.", "file": "cmd/admin/main.go", "line": 14}
  ],
  "questions": []
}

### Example 3 — mistake to avoid: flagging a symbol that is not in the diff

Intent: "Wire the new scheduler through the run command."
Facts: tests pass, lint clean, build ok.
Diff (abridged):
  "cmd/vairdict/run.go
    @@ ...
    +   res := runSingleTask(ctx, cfg, client, t.Intent)
    +   results[id] = res"

INCORRECT submit_verdict (do NOT produce this):
{
  "gaps": [
    {"severity": "P1", "description": "runSingleTask is called but not defined or imported — compilation error"}
  ]
}

Why this is wrong: runSingleTask is an existing function in the same
package, and same-package symbols do not need imports. The diff is a
patch, not a complete program; the definition lives in another file
that was not modified. The build facts above confirm the code compiles.
Treating a missing-from-diff symbol as a gap of ANY severity violates
the partial-diff rule — stay silent on it.

CORRECT submit_verdict for this diff:
{
  "summary": "## Reviewed\n- runSingleTask invocation wired into the new scheduler path",
  "gaps": [],
  "questions": []
}`

// systemPrompt is the quality judge system prompt with the non-negotiable
// engineering standards appended. Baseline rules reach the judge so it
// flags violations regardless of team config.
var systemPrompt = systemPromptCore + "\n\n" + standards.Block

// Judge evaluates whether the given diff fulfills the original intent and plan.
// It runs AI-based intent verification (against the diff content, not a
// directory path) and optionally e2e tests, returning a combined Verdict.
//
// `diff` is the full unified diff the LLM is asked to judge. Callers
// (the quality phase orchestrator and `vairdict review`) compute it via
// git before invoking the judge. An empty diff is allowed but will
// produce a low score because the LLM has nothing concrete to evaluate.
func (j *QualityJudge) Judge(ctx context.Context, intent string, plan string, diff string) (*state.Verdict, error) {
	// Step 1: AI intent verification.
	verdict, err := j.evaluateIntent(ctx, intent, plan, diff)
	if err != nil {
		return nil, fmt.Errorf("evaluating intent: %w", err)
	}

	// Step 2: Run e2e tests if configured. Run them in the current process
	// working directory — the judge no longer takes a workDir, and the
	// orchestrator always invokes us with the project root as cwd.
	if j.cfg.Phases.Quality.E2ERequired && j.cfg.Commands.E2E != "" {
		if e2eGap := j.runE2E(ctx, "."); e2eGap != nil {
			verdict.Gaps = append(verdict.Gaps, *e2eGap)
		}
	}

	// Blocking and score are derived deterministically — the model never
	// sets either.
	verdictschema.ApplyBlocking(verdict.Gaps, nil)
	// Baseline violations (#84) are non-negotiable: promote P0/P1 gaps
	// tagged with the baseline marker to blocking. In the quality judge
	// the default block set already covers P0/P1, so this is belt-and-
	// suspenders — but it stays consistent with the plan judge and
	// guards against a future block set that would exclude P1.
	if promoted := standards.ForceBaselineBlocking(verdict.Gaps); promoted > 0 {
		slog.Info("baseline rule forced blocking", "gaps_promoted", promoted)
	}
	verdict.Score = verdictschema.ComputeScore(verdict.Gaps)
	verdict.Pass = verdict.Score >= PassThreshold && !verdictschema.HasBlockingGap(verdict.Gaps)

	slog.Info("quality judge verdict",
		"score", verdict.Score,
		"pass", verdict.Pass,
		"gaps", len(verdict.Gaps),
	)

	return verdict, nil
}

// evaluateIntent uses the Claude API to assess whether the diff matches the intent.
func (j *QualityJudge) evaluateIntent(ctx context.Context, intent string, plan string, diff string) (*state.Verdict, error) {
	diffSection := diff
	if strings.TrimSpace(diffSection) == "" {
		diffSection = "(no diff provided — judge cannot evaluate code changes)"
	}

	var facts string
	if strings.TrimSpace(j.codeFacts) != "" {
		facts = fmt.Sprintf("\n\n## Facts (from code judge)\n%s", strings.TrimSpace(j.codeFacts))
	}

	prompt := fmt.Sprintf(
		"## Original Intent\n%s\n\n## Approved Plan\n%s%s\n\n## Diff (unified format)\n```diff\n%s\n```",
		intent, plan, facts, diffSection,
	)

	var verdict state.Verdict
	tool := verdictschema.VerdictTool("Submit the quality judge verdict as a structured object. Omit score, pass, and blocking — they are computed from the gap severities.")
	if err := j.client.CompleteWithTool(ctx, systemPrompt, prompt, tool, &verdict); err != nil {
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
