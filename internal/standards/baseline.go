// Package standards defines hardcoded, non-negotiable engineering standards
// that every VAIrdict run enforces. The baseline block is injected verbatim
// into the planner, coder, plan judge, and quality judge prompts. It is a
// Go-level constant — not a config knob — so teams cannot silently lower
// the bar by editing vairdict.yaml.
//
// Why a separate package: the same text must reach four different prompt
// builders that otherwise don't share code, and keeping the rule list in
// one place makes it easy to audit what agents are actually being told.
package standards

import "github.com/vairdict/vairdict/internal/state"

// RuleTag is the short, machine-friendly identifier for a baseline rule.
// Gaps the judge emits for baseline violations are expected to prefix the
// Description with the marker plus the tag so the judge post-processor can
// force Blocking=true regardless of team config.
type RuleTag string

const (
	RuleNoSecrets     RuleTag = "no-secrets"
	RuleHandleErrors  RuleTag = "handle-errors"
	RuleNoDeadCode    RuleTag = "no-dead-code"
	RuleSelfDocNames  RuleTag = "self-doc-names"
	RuleNoDuplication RuleTag = "no-duplication"
	RuleNoTodoFixme   RuleTag = "no-todo-fixme"
)

// BaselineMarker is the exact prefix judges must place on the gap
// Description when a baseline rule is violated. The post-processor looks
// for this marker (case-sensitive) to force blocking on P0/P1 baseline
// gaps even when config.BlockOn would have allowed them through.
const BaselineMarker = "BASELINE:"

// Block is the non-negotiable rule list injected into every agent prompt.
// Exported as a bare string constant (not a builder) so tests can assert
// its presence verbatim without any evaluation-order concerns.
//
// The markdown layout mirrors the other sections already used by the
// planner, coder, and judge prompts ("## …" headings, "- " bullets) so
// Claude sees a familiar structure.
const Block = `## Non-negotiable engineering standards

These rules are hardcoded into VAIrdict and cannot be disabled by config.
All three agents (planner, coder, judge) must uphold them. When the judge
finds a violation it MUST prefix the gap description with "BASELINE: <tag>"
so the orchestrator can force the gap blocking regardless of team config.

### P1 (blocking)

- ` + "`" + string(RuleNoSecrets) + "`" + ` — No hardcoded secrets or credentials.
  Examples: API keys, tokens, passwords, private keys, DB connection strings
  containing credentials. Load them from env vars or a secrets manager.
- ` + "`" + string(RuleHandleErrors) + "`" + ` — All errors must be handled and wrapped with context
  (` + "`" + `fmt.Errorf("doing thing: %w", err)` + "`" + `). Never silently discard an error
  with ` + "`" + `_ = fn()` + "`" + ` — if you intentionally drop one, add a one-line comment
  explaining why.
- ` + "`" + string(RuleNoDeadCode) + "`" + ` — No unreachable branches, unused imports, or unused
  symbols. Delete code the moment it stops being referenced; do not keep
  “just in case” paths.
- ` + "`" + string(RuleNoTodoFixme) + "`" + ` — No ` + "`TODO`, `FIXME`, `XXX`" + ` left in code that reaches
  a passing verdict. Either file an issue and link it in a regular comment,
  or fix it before the verdict.

### P2 (non-blocking but required)

- ` + "`" + string(RuleSelfDocNames) + "`" + ` — Names are self-documenting. No single-letter
  or unexplained abbreviations (` + "`" + `tmp` + "`" + `, ` + "`" + `i` + "`" + ` in a loop body that spans more
  than a couple of lines, ad-hoc ` + "`" + `mgr` + "`" + `/` + "`" + `hdlr` + "`" + `/` + "`" + `proc` + "`" + ` abbreviations).
  Prefer full words; if an abbreviation is standard in the domain, that is
  fine.
- ` + "`" + string(RuleNoDuplication) + "`" + ` — No copy-pasted logic. Before writing a new
  helper, check for an existing one; if you find yourself pasting a block
  and tweaking a literal, extract a shared function instead.
`

// AllRules is the canonical list of baseline rules. Tests and the judge
// post-processor iterate it; the baseline Block above must mention each
// tag by name, and the tests enforce that invariant.
var AllRules = []RuleTag{
	RuleNoSecrets,
	RuleHandleErrors,
	RuleNoDeadCode,
	RuleSelfDocNames,
	RuleNoDuplication,
	RuleNoTodoFixme,
}

// ForceBaselineBlocking promotes P0/P1 baseline gaps to Blocking=true even
// when team config (e.g. BlockOn=[P0] only) would have left them
// non-blocking. P2/P3 baseline gaps keep their original Blocking flag so
// naming/duplication advisories don't halt the run. Returns the number of
// gaps that had Blocking promoted, for logging.
func ForceBaselineBlocking(gaps []state.Gap) int {
	promoted := 0
	for i := range gaps {
		if !isBaselineDescription(gaps[i].Description) {
			continue
		}
		if gaps[i].Severity != state.SeverityP0 && gaps[i].Severity != state.SeverityP1 {
			continue
		}
		if !gaps[i].Blocking {
			gaps[i].Blocking = true
			promoted++
		}
	}
	return promoted
}

func isBaselineDescription(desc string) bool {
	return len(desc) >= len(BaselineMarker) && desc[:len(BaselineMarker)] == BaselineMarker
}
