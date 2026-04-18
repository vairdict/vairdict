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

You are given the original intent, the approved plan, and the unified diff
of the changes that were made. Evaluate whether the diff actually
implements the intent and plan. Base every observation on what the diff
shows — never invent file contents that are not in the diff.

You MUST respond with valid JSON only — no markdown, no explanation outside the JSON.

Severity levels for gaps:
- P0: intent mismatch — the code does not solve the stated problem or is fundamentally wrong
- P1: significant gap — major feature or requirement is missing or broken.
  This includes any correctness bug in production code OR test code, such as:
  tautological assertions (e.g. errors.Is(err, err)), unreachable branches,
  tests that can never fail, wrong variable compared, dead code that masks
  missing coverage.
- P2: minor issue — style, naming, docs, minor edge cases that do not affect correctness
- P3: nice to have — deferred to future work

For each gap, set "blocking" to true only for P0 and P1 severity.
A correctness bug is ALWAYS at least P1, never P2 — even if it is in test code.

## Additional checks

In addition to intent/plan alignment, scan the diff for the following.
These are supplementary to the intent/plan check above. Security issues
are blocking (P1). Code-reuse and style issues are non-blocking (P2/P3).

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

Security issues are blocking — set severity to P1 and blocking to true.
Only flag what is actually visible in the diff. Do not speculate about code
outside the diff.

### Code reuse (P2 non-blocking)
Flag duplicated or copy-pasted logic visible in the diff:
- Two or more new functions/methods with near-identical bodies (>5 lines)
- Copy-pasted blocks that differ only in variable names or literals
- Re-implementation of logic that clearly exists in the same diff
  (e.g. a helper is defined but not used, and the same logic is inlined)

Only flag duplication within the diff itself — do not assume what exists
in the rest of the codebase.

### Style & maintainability (P3 non-blocking)
Flag readability and maintainability issues visible in the diff:
- Functions longer than ~80 lines (suggest splitting)
- Magic numbers or string literals that should be named constants
- Confusing or misleading variable/function names
- Deeply nested control flow (>3 levels) that could be simplified
- Missing error handling where errors are silently discarded (e.g. _ = fn())

These are suggestions, not requirements. Use P3 severity.

---

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
      "blocking": <bool>,
      "file": "<path from diff header, e.g. internal/foo/bar.go>",
      "line": <line number in the new file where the issue is>
    }
  ],
  "questions": [
    {
      "text": "<question about the implementation>",
      "priority": "<high|medium|low>"
    }
  ]
}

For each gap, include "file" and "line" when the issue maps to a specific
location in the diff. Use the file path from the diff header (the b/ side)
and the line number from the @@ hunk header (the + side). Omit "file" and
"line" (or set to "" and 0) for gaps that are architectural or span multiple
files.`

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
	aiVerdict, err := j.evaluateIntent(ctx, intent, plan, diff)
	if err != nil {
		return nil, fmt.Errorf("evaluating intent: %w", err)
	}

	// Step 2: Run e2e tests if configured. Run them in the current process
	// working directory — the judge no longer takes a workDir, and the
	// orchestrator always invokes us with the project root as cwd.
	if j.cfg.Phases.Quality.E2ERequired && j.cfg.Commands.E2E != "" {
		e2eGap := j.runE2E(ctx, ".")
		if e2eGap != nil {
			aiVerdict.Gaps = append(aiVerdict.Gaps, *e2eGap)
			// Penalize score for e2e failure: reduce by 30 points, floor at 0.
			aiVerdict.Score = max(0, aiVerdict.Score-30)
		}
	}

	// Enforce pass threshold: score >= 70 AND no blocking gaps.
	hasBlocking := false
	for _, g := range aiVerdict.Gaps {
		if g.Blocking {
			hasBlocking = true
			break
		}
	}
	aiVerdict.Pass = aiVerdict.Score >= 70 && !hasBlocking

	slog.Info("quality judge verdict",
		"score", aiVerdict.Score,
		"pass", aiVerdict.Pass,
		"gaps", len(aiVerdict.Gaps),
	)

	return aiVerdict, nil
}

// evaluateIntent uses the Claude API to assess whether the diff matches the intent.
func (j *QualityJudge) evaluateIntent(ctx context.Context, intent string, plan string, diff string) (*state.Verdict, error) {
	diffSection := diff
	if strings.TrimSpace(diffSection) == "" {
		diffSection = "(no diff provided — judge cannot evaluate code changes)"
	}
	prompt := fmt.Sprintf("## Original Intent\n%s\n\n## Approved Plan\n%s\n\n## Diff (unified format)\n```diff\n%s\n```",
		intent, plan, diffSection)

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
