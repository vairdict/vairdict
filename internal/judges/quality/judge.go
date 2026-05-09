// Package quality implements the quality phase judge, which evaluates whether
// completed code fulfills the original task intent and optionally runs e2e tests.
// It uses the Claude API for intent verification via tool-use and produces a
// typed Verdict with a deterministic score computed from gap severities.
package quality

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/judges/verdictschema"
	"github.com/vairdict/vairdict/internal/standards"
	"github.com/vairdict/vairdict/internal/state"
)

// Completer is the interface for sending prompts to an LLM. Quality judge uses
// multi-turn tool-use so the model can call auxiliary tools (like check_path)
// before submitting the final verdict. Model() reports the model the client
// routes calls to so the verdict can be stamped with the model that produced it.
type Completer interface {
	CompleteWithTools(ctx context.Context, system, prompt string, tools []claude.Tool, finalTool string, handlers map[string]claude.ToolHandler, target any) error
	Model() string
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

// checkPathSchema is the JSON Schema for the check_path auxiliary tool.
const checkPathSchema = `{
  "type": "object",
  "properties": {
    "path": {
      "type": "string",
      "description": "Relative path from the project root to check (e.g. 'cmd/vairdict', 'internal/config/config.go')."
    }
  },
  "required": ["path"],
  "additionalProperties": false
}`

// checkPathTool returns the tool definition for the check_path auxiliary tool.
func checkPathTool() claude.Tool {
	return claude.Tool{
		Name:        "check_path",
		Description: "Check whether a file or directory exists in the project repository. Returns existence status and type (file or directory).",
		InputSchema: json.RawMessage(checkPathSchema),
	}
}

// checkPathHandler resolves a check_path tool call by stat-ing the path
// relative to the current working directory (project root).
func checkPathHandler(_ context.Context, input json.RawMessage) (string, error) {
	var req struct {
		Path string `json:"path"`
	}
	if err := json.Unmarshal(input, &req); err != nil {
		return "", fmt.Errorf("parsing check_path input: %w", err)
	}
	cleaned := filepath.Clean(req.Path)
	if filepath.IsAbs(cleaned) || strings.HasPrefix(cleaned, "..") {
		return "error: path must be relative and within the project", nil
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return "exists: false", nil
	}
	if info.IsDir() {
		return "exists: true, type: directory", nil
	}
	return "exists: true, type: file", nil
}

const systemPromptCore = `You are an experienced senior code reviewer acting as a quality judge
for a software development process engine. Your job is to evaluate
whether the implemented code fulfills the original task intent.

## Reviewer mindset — no prior context

Treat this as a fresh review. You are NOT the author of this change and
you do NOT carry any prior reasoning about why specific choices were
made. The implementation was produced by a different agent; do not
defer to its implicit justifications. Only the intent, plan, facts,
and diff below exist — everything else is something you must infer
from the diff itself or flag as a question.

You care about correctness, clarity, and future maintenance pain. You are
considered and deliberate — every observation earns its place. A thoughtful
review surfaces design decisions, risks, and follow-ups — not just bugs.

## Substantive-diff rule (HARD)

A diff is "substantive" when it changes >200 lines OR touches >3 files.
Before submitting a verdict on a substantive diff, perform a
severity-ordered scan of the added code:

1. Correctness bugs visible in added code (wrong logic, broken
   invariants, tautological asserts, dead branches) → critical/high
2. Security or authorisation gaps in new code paths → critical/high
3. Non-obvious trade-offs or assumptions the author should name
   explicitly so future maintainers see them → medium
4. Concrete follow-ups worth filing so the work isn't lost → low

The number of gaps is whatever this scan actually surfaces. Severity,
not count, drives the verdict:
- Zero gaps is the correct verdict on a mechanical, well-tested
  change where the scan finds nothing real.
- One critical plus nothing else is correct when there is one real bug
  and no other concerns.
- Many gaps is correct when there are many real concerns.

Do NOT calibrate to a target count. Do NOT pad with generic advice
("consider extracting a helper", "add more tests"), speculative
worries, or observations about code that is not in the diff. Every
gap must name a concern a senior reviewer would actually raise on
this specific code.

An empty gaps array on a substantive diff is valid ONLY when the
severity-ordered scan above genuinely finds nothing concrete. Going
silent without performing the scan is a failure mode — most
substantive changes have at least one trade-off worth naming, and
quietly waving them through hides risk from the author.

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

## Before flagging Medium / Low / Standards: check for an existing fix

Before emitting a Medium-, Low-, or Standards-severity gap, search the
surrounding file(s) for an existing handler, guard, helper, or
convention that already addresses the concern. If you find one,
do not flag — the issue is not a real gap, it is a documentation
miss in your read. Examples:

- About to flag "no error handling" on a returned err — scan the
  function: is there a defer that wraps or a wrapper at the call
  site that already handles it? If yes, do not flag.
- About to flag "missing input validation" — check whether the
  caller validates, or whether a typed wrapper has already narrowed
  the input.
- About to flag a Standards naming nit — check whether the file
  already establishes a convention for that identifier kind.

Critical and High findings are exempt from this rubric: a real
correctness or security bug stands on its own and you must flag it
even if a partial mitigation exists elsewhere. The rubric exists to
suppress the lower-severity false positives where "I noticed X" turns
out to be "X is already handled, I just didn't read far enough."

## Read the whole hunk before flagging "missing X"

Before raising any gap of the form "missing doc comment", "missing nil
check", "missing error handling", "field is undocumented", or
"behaviour is unexplained", read the FULL hunk you are anchored in —
the surrounding context lines (no + or - prefix) AND the nearby +
lines, not just the single line you anchored to. In practice this
means scanning roughly 30 lines on either side of your anchor in the
same hunk: comment blocks above declarations, the rest of the
function body, sibling fields in the same struct. If the doc
comment, nil check, error return, or explanation already exists in
the same hunk, drop the gap.

This is the most common false-positive pattern in past reviews: the
judge anchors to a struct field or function declaration and asks for
documentation that already lives in the comment block immediately
above. A single-line glance misses adjacent comment blocks, existing
nil guards, and surrounding error handling. Reading the whole hunk
costs nothing and eliminates the bulk of these false positives.

The same applies to "this duplicates X elsewhere" or "this contradicts
Y" — verify by re-reading the relevant + lines across the whole diff
before raising the gap, not just the line you started from.

## Verifying file or directory existence

If you are genuinely uncertain whether a file or directory referenced in the
diff exists in the project, call the check_path tool with the relative path
BEFORE raising a gap or question about it. Do NOT guess — verify first.
Do NOT call check_path for:
- Code symbols (functions, types, variables) — they exist if referenced in the diff
- Paths that appear in the diff itself — they obviously exist

## Severity levels for gaps

- critical: intent mismatch — the code does not solve the stated problem or is fundamentally wrong
- high:     significant gap — major feature or requirement is missing or broken.
  This includes any correctness bug in production code OR test code, such as:
  tautological assertions (e.g. errors.Is(err, err)), unreachable branches,
  tests that can never fail, wrong variable compared, dead code that masks
  missing coverage.
  NEVER use high for a symbol that is referenced but not defined in the diff —
  that symbol exists in the codebase already.
- medium:   minor issue — style, naming, docs, minor edge cases that do not affect correctness
- low:      nice to have — deferred to future work

Do NOT set "blocking" on gaps and do NOT estimate a score — the orchestrator
computes both deterministically from severities.
A correctness bug is ALWAYS at least high, never medium — even if it is in test code.

## Additional checks

In addition to intent/plan alignment, scan the diff for the following.

### Security (high — blocking)
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

### Code reuse (medium — non-blocking)
Flag duplicated or copy-pasted logic visible in the diff:
- Two or more new functions/methods with near-identical bodies (>5 lines)
- Copy-pasted blocks that differ only in variable names or literals
- Re-implementation of logic that clearly exists in the same diff

### Cross-file consistency (severity follows impact)
When the diff applies the same change pattern in multiple places —
a new flag added at two call sites, a new field threaded through
several constructors, the same conditional bolted onto several
handlers, the same args slice extended in two methods — compare the
sites against each other and pick severity by what the divergence
actually causes:
- Identical bodies in 2+ locations with no behavioural divergence →
  medium: flag for extraction, or note that a future third instance
  should trigger one.
- Cosmetic differences only (argument order, error wording) → medium:
  drift risk worth tightening before bugs hide in it.
- Divergence that produces incorrect behaviour at one of the sites
  (one site missing a guard the others have, one path silently
  skipping the new flag, one branch returning the wrong type) →
  high: this is a correctness bug, not a style issue, and must be
  graded as such.

Anchor the gap on one of the diverging locations and name the other
site explicitly so the author can see both. Do not flag a single
isolated change as cross-file drift — this rule applies only when the
same pattern is genuinely repeated in the diff.

### Style & maintainability (low — non-blocking)
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

## Root-cause diagnosis (return_to)

On a FAILING verdict you MUST set "return_to" so the outer loop can rewind
to the phase that can actually fix it. Diagnose the root cause, not the
symptom:

- "code" — the plan is fine but the code doesn't realise it. Tests fail,
  acceptance criteria aren't met, the diff implements something other
  than what the plan called for, a bug slipped in. Re-running the code
  phase can fix it.
- "plan" — the code faithfully implements the plan, but the plan itself
  was too shallow to catch this class of problem (missing a requirement,
  wrong architecture, no handling for a whole category of input). A
  code retry against the same plan will reproduce the failure. The
  quality failure will be injected as a hard constraint into replanning.
- "escalate" — the task intent is fundamentally ambiguous or requires
  judgement this process cannot make. Neither replanning nor recoding
  can resolve it without human input.

On a PASSING verdict, omit "return_to" or set it to "". Never set
"return_to" to a value that is not one of {code, plan, escalate, ""}.

If a failing verdict has only non-blocking (medium/low) gaps, it may get
another in-phase retry; in that case omit "return_to" or set it to "".
If ANY gap is critical or high (blocking), "return_to" must be one of code/plan/
escalate.

## Output rules

1. Each concern goes in EXACTLY ONE array — either "gaps" or "questions", never both.
2. A "question" is ONLY for genuine uncertainty you cannot resolve from the diff.
3. Never create a gap or question about a symbol not defined in the diff — it exists.
4. For gaps tied to a specific diff line, set "file" (b/ side) and "line" (+ side).
   Always provide file/line when ANY plausible anchor exists — a function,
   a filter condition, a config key. Omit (set to "" / 0) ONLY for genuinely
   repo-wide gaps (e.g. "missing CI workflow", "no README section"). Gaps
   without an anchor cannot be posted as inline PR comments, so defaulting
   to file/line keeps reviewers' feedback visible where it belongs.
   Every added ('+') line in the diff is pre-labelled "+L<n>: ..." where
   <n> is its absolute new-file line number — copy that number verbatim
   into "line". Do NOT count lines yourself from the @@ hunk header; use
   the label. If the line you want to reference is unchanged context
   (not a '+' line), pick the nearest adjacent '+' line and anchor
   there — GitHub review comments can only attach to changed lines.
5. For gaps with file/line, you may also set "suggestion" — the exact replacement
   code for the line(s) at that location. The suggestion is rendered as a GitHub
   suggestion block that the author can apply with one click. Rules:
   - Only set when you can offer a concrete, correct, complete replacement.
   - The suggestion replaces the ENTIRE line referenced by "line". Include
     the full corrected line(s), preserving indentation.
   - Omit for design concerns, architectural observations, or when the fix
     spans many lines or requires changes in multiple locations.
   - Good candidates: renamed variables, added nil checks, fixed format strings,
     corrected function signatures, small refactors (1–3 lines).

## Examples

### Example 1 — pass with texture (substantive change, design observations)

Intent: "Add a multi-tenant namespace layer so requests carry a tenant ID through the pipeline."
Facts: tests pass, lint clean, build ok.
Diff (abridged): adds a Tenant type, threads it through 4 handlers,
  migrates the DB to include tenant_id, updates 6 query helpers, plus
  test coverage. Roughly 450 lines across 9 files.

submit_verdict input:
{
  "summary": "## Reviewed\n- Tenant threading through the 4 handlers\n- Migration adds tenant_id with default ''\n## Notes\n- Design-level follow-ups noted below for post-merge consideration",
  "gaps": [
    {"severity": "low", "description": "Migration defaults tenant_id to empty string; once real tenants arrive, the backfill strategy will need to be decided before the column becomes authoritative.", "file": "migrations/0042.sql", "line": 7},
    {"severity": "medium", "description": "queryHelpers.go now has 6 near-identical 'WHERE tenant_id = ?' clauses — extract a tenantScope() helper once a second table needs the same pattern.", "file": "internal/db/queryHelpers.go", "line": 18},
    {"severity": "low", "description": "Tenant is threaded through function signatures rather than context.Context; fine for now, but as the set of tenant-scoped calls grows, context propagation will reduce parameter churn."}
  ],
  "questions": [],
  "return_to": ""
}

Note: this is what a substantive diff WITH real observations looks
like. The count (3) is incidental — the same diff might warrant 1, 5,
or 0 gaps depending on what the severity-ordered scan surfaces. See
Example 5 for the same scale of diff with nothing concrete to flag.
Severity, not count, drives the verdict; never pad with nits to hit
a target, and never fabricate concerns to balance a substantive
change.

### Example 2 — clear fail (intent mismatch + security)

Intent: "Add basic auth to the admin endpoint."
Facts: tests pass, lint clean, build ok.
Diff (abridged): "+ admin.HandleFunc('/admin', handler) ... + const apiKey = \"sk-live-abc123\""

submit_verdict input:
{
  "summary": "## Reviewed\n- admin route wiring and literal credential\n## Notes\n- Hardcoded key must move to env or config",
  "gaps": [
    {"severity": "critical", "description": "No authentication middleware on /admin — intent requires basic auth."},
    {"severity": "high", "description": "Hardcoded API key in source (apiKey = 'sk-live-...'). Move to environment variable.", "file": "cmd/admin/main.go", "line": 14, "suggestion": "\tapiKey := os.Getenv(\"ADMIN_API_KEY\")"}
  ],
  "questions": [],
  "return_to": "code"
}

Note: return_to is "code" because the plan called for basic auth — the
code just didn't wire it. A code retry against the same plan can fix it.

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
    {"severity": "high", "description": "runSingleTask is called but not defined or imported — compilation error"}
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
  "questions": [],
  "return_to": ""
}

### Example 4 — rewind to plan (root cause is plan-level)

Intent: "Add basic auth to the admin endpoint. Must protect every
admin route."
Plan (abridged): "1. Wrap /admin with a basic-auth handler."
Facts: tests pass, lint clean, build ok.
Diff (abridged): correctly wraps /admin with basic-auth middleware.
But the repo also exposes /admin/users and /admin/logs under separate
handlers that are NOT covered by the wrapper.

submit_verdict input:
{
  "summary": "## Reviewed\n- /admin wrap is correct\n## Notes\n- /admin/users and /admin/logs are unprotected sibling routes",
  "gaps": [
    {"severity": "critical", "description": "Plan only covered /admin, but the intent says 'every admin route' — /admin/users and /admin/logs remain unauthenticated. The plan is too narrow to satisfy the intent."}
  ],
  "questions": [],
  "return_to": "plan"
}

Note: return_to is "plan" — the code correctly implemented the plan, but
the plan itself missed the scope. Re-running code against the same plan
would just re-produce the same gap. Replanning with the intent re-read
will add the missing routes.

### Example 5 — pass clean (substantive change, no concerns to surface)

Intent: "Replace fmt.Printf calls with structured slog logging across the request pipeline."
Facts: tests pass, lint clean, build ok.
Diff (abridged): adds a slog handler at startup, replaces 12 fmt.Printf
  call sites with slog.Info / slog.Debug, threads request_id through
  middleware via context.Context, plus updated tests. Roughly 380 lines
  across 7 files.

submit_verdict input:
{
  "summary": "## Reviewed\n- slog handler initialisation\n- request_id propagation through middleware context\n- replacement of fmt.Printf call sites with structured logging\n## Notes\n- Mechanical, well-tested change; severity scan surfaced no concerns",
  "gaps": [],
  "questions": [],
  "return_to": ""
}

Note: an empty gaps array on a substantive diff is the correct verdict
when the change is mechanical, well-scoped, and the testing facts
confirm the behaviour. The severity-ordered scan finds no real bug,
no security gap, no non-obvious trade-off, and no concrete follow-up.
Do NOT invent design nits to "balance" the verdict against the size
of the diff — staying honest about a clean change is more valuable
than fabricating texture.

### Example 6 — whole-hunk reading prevents a false-positive doc gap

Intent: "Add a reserved CodeJudgeModel config field for future LLM-backed code judges."
Facts: tests pass, lint clean, build ok.
Diff (abridged):
  "internal/config/config.go
    @@ ...
    +   // CodeJudgeModel is reserved for future LLM-backed code judges.
    +   // The current code judge runs deterministic shell checks, so
    +   // this field is parsed but unused today.
    +   CodeJudgeModel string ` + "`yaml:\"code_judge_model\"`" + `"

INCORRECT submit_verdict (do NOT produce this):
{
  "gaps": [
    {"severity": "low", "description": "CodeJudgeModel is undocumented; consider explaining its purpose and why it is currently unused.", "file": "internal/config/config.go", "line": 60}
  ]
}

Why this is wrong: the three lines immediately above the field are a
doc comment that already explains exactly what the gap asks for. The
judge anchored to line 60 (the field declaration) and asked for a
comment without reading the lines above. The whole-hunk rule
eliminates this — read the surrounding context first, see the
comment, drop the gap.

CORRECT submit_verdict for this diff:
{
  "summary": "## Reviewed\n- CodeJudgeModel field reserved for future judges with explicit doc block",
  "gaps": [],
  "questions": [],
  "return_to": ""
}`

// systemPrompt is the quality judge system prompt with the non-negotiable
// engineering standards appended. Baseline rules reach the judge so it
// flags violations regardless of team config.
var systemPrompt = systemPromptCore + "\n\n" + standards.Block

// RenderCrossPushFraming produces the cross-push framing block the
// quality judge prepends to its user prompt when prior verdict gaps
// exist. The framing closes the "judge invents a finding on push N
// that was already there on push N-1" failure mode that produces the
// nagging-comment behaviour the user asked us to fix.
//
// The rules baked into the framing:
//
//   - the prior review's gaps are listed verbatim with severities;
//   - each prior gap must be checked for current applicability and
//     dropped from the new verdict if the latest push fixed it;
//   - new findings are only valid for code introduced or changed in
//     the diff since the prior review;
//   - findings that pre-date the prior review must NOT be introduced
//     now — if the previous round missed them, they were either not
//     real or not the judge's responsibility to flag at this point.
//
// Returns "" for nil/empty input so the framing disappears on the
// first review of a PR (no prior gaps -> no cross-push pressure).
func RenderCrossPushFraming(priorGaps []state.Gap) string {
	if len(priorGaps) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("## Cross-push awareness — prior review gaps\n\n")
	b.WriteString("This PR has been reviewed before. The prior review emitted the following gaps:\n\n")
	for _, g := range priorGaps {
		fmt.Fprintf(&b, "- [%s] %s\n", g.Severity.Display(), g.Description)
	}
	b.WriteString("\nWhen producing this review:\n")
	b.WriteString("\n1. Verify each prior gap above for current applicability in the diff. ")
	b.WriteString("If the latest push has fixed it, drop it; do not re-flag a resolved concern. ")
	b.WriteString("If it is still present, keep it with the same severity.\n")
	b.WriteString("\n2. Scan only the diff since the prior review for new findings. ")
	b.WriteString("Anything outside the most recent push is not a new finding.\n")
	b.WriteString("\n3. Do not introduce findings that pre-dated the prior review. ")
	b.WriteString("If the previous round missed them, they were either not real or not in scope; ")
	b.WriteString("emitting them now would be a nagging-comment failure mode, not a useful review.\n")
	return b.String()
}

// Judge evaluates whether the given diff fulfills the original intent and plan.
// It runs AI-based intent verification (against the diff content, not a
// directory path) and optionally e2e tests, returning a combined Verdict.
//
// `diff` is the full unified diff the LLM is asked to judge. Callers
// (the quality phase orchestrator and `vairdict review`) compute it via
// git before invoking the judge. An empty diff is allowed but will
// produce a low score because the LLM has nothing concrete to evaluate.
//
// `priorGaps` is the gap list from the previous review round (the
// previous phase loop, or the previous push on the same PR). When
// non-empty, the judge prepends the cross-push framing block to the
// user prompt so the model verifies each prior gap is still applicable
// instead of inventing fresh findings for code that pre-dated the
// prior review. nil/empty disables the framing — used for first-round
// reviews and one-shot calls (e.g. `vairdict review`).
func (j *QualityJudge) Judge(ctx context.Context, intent string, plan string, diff string, priorGaps []state.Gap, checklist []state.ChecklistItem) (*state.Verdict, error) {
	// Step 1: AI intent verification.
	verdict, err := j.evaluateIntent(ctx, intent, plan, diff, priorGaps, checklist)
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

	// Step 3: AC tracing. When the task carries a parsed AC checklist
	// from the issue body, merge the model's per-item audit response
	// (verdict.Checklist as returned by the tool) with the source list
	// — the source contributes Description/Required/Name (which the
	// model cannot rewrite), the audit contributes Passed/Reason. For
	// every Required item the gate would fail on (unpassed AND no
	// reason), surface a Critical Blocking gap so the verdict comment
	// explains what's missing rather than just emitting an opaque
	// NEEDS_WORK.
	if len(checklist) > 0 {
		merged := verdictschema.MergeChecklistAudit(checklist, verdict.Checklist)
		verdict.Checklist = merged
		for _, it := range merged {
			if !it.Required || it.Passed {
				continue
			}
			if strings.TrimSpace(it.Reason) != "" {
				continue
			}
			verdict.Gaps = append(verdict.Gaps, state.Gap{
				Severity:    state.SeverityCritical,
				Description: fmt.Sprintf("Acceptance criterion not satisfied: %s", it.Description),
			})
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
	// Pass gate: when the task ships an AC list, use the new
	// mechanical gate (DeriveVerdictState — zero blocking gaps + every
	// Required item Passed-or-deferred-with-reason). Legacy tasks
	// without a checklist keep the threshold-based pass for
	// backwards compatibility; flipping that path is out of scope
	// here.
	if len(verdict.Checklist) > 0 {
		verdict.Pass = state.DeriveVerdictState(verdict.Gaps, verdict.Checklist) == state.VerdictPass
	} else {
		verdict.Pass = verdict.Score >= PassThreshold && !verdictschema.HasBlockingGap(verdict.Gaps)
	}
	verdict.ReturnTo = normaliseReturnTo(verdict.ReturnTo, verdict.Pass, verdict.Gaps)
	verdict.Model = j.client.Model()

	slog.Info("quality judge verdict",
		"score", verdict.Score,
		"pass", verdict.Pass,
		"gaps", len(verdict.Gaps),
		"checklist", len(verdict.Checklist),
		"model", verdict.Model,
	)

	return verdict, nil
}

// evaluateIntent uses the Claude API to assess whether the diff matches the intent.
func (j *QualityJudge) evaluateIntent(ctx context.Context, intent string, plan string, diff string, priorGaps []state.Gap, checklist []state.ChecklistItem) (*state.Verdict, error) {
	diffSection := diff
	if strings.TrimSpace(diffSection) == "" {
		diffSection = "(no diff provided — judge cannot evaluate code changes)"
	} else {
		diffSection = annotateDiff(diffSection)
	}

	var facts string
	if strings.TrimSpace(j.codeFacts) != "" {
		facts = fmt.Sprintf("\n\n## Facts (from code judge)\n%s", strings.TrimSpace(j.codeFacts))
	}

	// Cross-push framing comes BEFORE the intent/plan/diff so the model
	// reads the "verify prior gaps, do not invent old findings"
	// instructions while it's still building its mental model of the
	// review, not after it has already started forming opinions. Empty
	// for first-round reviews (priorGaps==nil).
	framing := RenderCrossPushFraming(priorGaps)
	acSection := renderACSection(checklist)
	prompt := fmt.Sprintf(
		"%s## Original Intent\n%s\n\n## Approved Plan\n%s%s%s\n\n## Diff (unified format)\n```diff\n%s\n```",
		framing, intent, plan, facts, acSection, diffSection,
	)

	var verdict state.Verdict
	verdictTool := verdictschema.VerdictTool("Submit the quality judge verdict as a structured object. Omit score, pass, and blocking — they are computed from the gap severities.")
	tools := []claude.Tool{verdictTool, checkPathTool()}
	handlers := map[string]claude.ToolHandler{
		"check_path": checkPathHandler,
	}
	if err := j.client.CompleteWithTools(ctx, systemPrompt, prompt, tools, verdictschema.ToolName, handlers, &verdict); err != nil {
		return nil, fmt.Errorf("calling completer: %w", err)
	}

	return &verdict, nil
}

// renderACSection produces the "## Acceptance Criteria" prompt
// fragment listing the AC items the judge must tick, with explicit
// per-item-evidence instructions. Empty when the task carries no AC
// checklist; in that case the judge runs in legacy mode (score-based
// pass, no AC enforcement).
//
// The instructions are deliberately repetitive of the schema's
// `checklist` field description — the prompt repeats the contract so
// the model walks it consciously instead of treating it as a side
// effect of the tool call. The negative-space prompt ("which files
// would I expect to change?") is the load-bearing part — without it
// the model tends to mark items met based on plausibility rather
// than diff evidence.
func renderACSection(items []state.ChecklistItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Acceptance Criteria\n\n")
	b.WriteString("For EACH item below, populate one entry in the submit_verdict tool's `checklist` array. Use the exact `name` shown.\n\n")
	b.WriteString("Contract:\n")
	b.WriteString("- `passed=true` requires concrete file:line evidence in `reason`. If you can't cite a hunk in the diff that satisfies the criterion, do not mark it passed.\n")
	b.WriteString("- `passed=false` requires a deferral note in `reason` explaining why this item isn't being completed (e.g. \"blocked on #N\", \"needs upstream X\", \"out of scope per <commit>\"). Empty `reason` on an unpassed item BLOCKS the verdict — the judge cannot quietly skip an AC item.\n\n")
	b.WriteString("Negative-space check, run for each item before deciding `passed`:\n")
	b.WriteString("1. Which files would I expect to change to satisfy this criterion?\n")
	b.WriteString("2. Did those files actually change in the diff?\n")
	b.WriteString("3. If no, this item is NOT done, even if related work is present.\n\n")
	b.WriteString("Items:\n")
	for _, it := range items {
		b.WriteString(fmt.Sprintf("- `%s`: %s\n", it.Name, it.Description))
	}
	return b.String()
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
			Severity:    state.SeverityHigh,
			Description: fmt.Sprintf("e2e tests failed: %s", truncate(strings.TrimSpace(outStr), 500)),
		}
	}

	slog.Debug("e2e tests passed")
	return nil
}

// normaliseReturnTo clamps the LLM-supplied ReturnTo to a sane value given
// the final (deterministic) verdict shape. The LLM emits ReturnTo as a
// best-effort diagnosis, but scoring and blocking are decided here — so
// we enforce the invariants the outer loop relies on:
//
//   - A passing verdict never rewinds; clear ReturnTo.
//   - A failing verdict with a blocking gap (critical/high) must rewind. If the
//     LLM forgot to set ReturnTo we default to ReturnToCode — that matches
//     the pre-ReturnTo heuristic (see the removed needsCodeRework) and is
//     the safer default: code retries are cheaper than replans.
//   - Unknown values collapse to code for the same reason.
//
// Non-blocking failures (score below threshold but no P0/P1) keep an
// empty ReturnTo so the quality phase can retry in-place.
func normaliseReturnTo(in state.ReturnTo, pass bool, gaps []state.Gap) state.ReturnTo {
	if pass {
		return state.ReturnToNone
	}
	switch in {
	case state.ReturnToCode, state.ReturnToPlan, state.ReturnToEscalate:
		return in
	}
	if verdictschema.HasBlockingGap(gaps) {
		return state.ReturnToCode
	}
	return state.ReturnToNone
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
