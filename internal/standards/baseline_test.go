package standards

import (
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/state"
)

func TestBlock_MentionsEveryRuleTag(t *testing.T) {
	// Every rule in AllRules must appear by tag in the baseline Block,
	// otherwise the judge has no way to reference it when flagging
	// violations. Guards against silent drift when new rules are added.
	for _, tag := range AllRules {
		if !strings.Contains(Block, string(tag)) {
			t.Errorf("baseline Block missing rule tag %q — update Block when adding a rule", tag)
		}
	}
}

func TestBlock_MentionsMarker(t *testing.T) {
	// The marker is the anchor the judge post-processor looks for.
	if !strings.Contains(Block, BaselineMarker) {
		t.Errorf("baseline Block must instruct the judge to prefix violations with %q", BaselineMarker)
	}
}

func TestBlock_DeclaresNonNegotiable(t *testing.T) {
	// Prompt wording matters: the phrase "non-negotiable" is the signal
	// to the agent that these rules override team config. Case-insensitive
	// match so a Title-Case heading still counts.
	lower := strings.ToLower(Block)
	if !strings.Contains(lower, "non-negotiable") {
		t.Error("baseline Block must explicitly call the standards non-negotiable")
	}
	if !strings.Contains(lower, "cannot be disabled") {
		t.Error("baseline Block must state rules cannot be disabled by config")
	}
}

func TestForceBaselineBlocking_PromotesP0AndP1(t *testing.T) {
	gaps := []state.Gap{
		{Severity: state.SeverityP0, Description: "BASELINE: no-secrets: API key committed", Blocking: false},
		{Severity: state.SeverityP1, Description: "BASELINE: handle-errors: _ = fn()", Blocking: false},
	}

	n := ForceBaselineBlocking(gaps)
	if n != 2 {
		t.Errorf("expected 2 promotions, got %d", n)
	}
	for i, g := range gaps {
		if !g.Blocking {
			t.Errorf("gap %d (%s) should have been promoted to blocking", i, g.Severity)
		}
	}
}

func TestForceBaselineBlocking_LeavesP2AndP3Alone(t *testing.T) {
	gaps := []state.Gap{
		{Severity: state.SeverityP2, Description: "BASELINE: self-doc-names: tmp is unclear", Blocking: false},
		{Severity: state.SeverityP3, Description: "BASELINE: no-duplication: tiny repetition", Blocking: false},
	}

	n := ForceBaselineBlocking(gaps)
	if n != 0 {
		t.Errorf("expected 0 promotions for P2/P3 baseline gaps, got %d", n)
	}
	for _, g := range gaps {
		if g.Blocking {
			t.Error("P2/P3 baseline gap should remain non-blocking")
		}
	}
}

func TestForceBaselineBlocking_IgnoresNonBaselineGaps(t *testing.T) {
	// Unmarked P1 gaps are governed by team config, not baseline.
	gaps := []state.Gap{
		{Severity: state.SeverityP1, Description: "missing request validation", Blocking: false},
	}

	n := ForceBaselineBlocking(gaps)
	if n != 0 {
		t.Errorf("expected 0 promotions for non-baseline gaps, got %d", n)
	}
	if gaps[0].Blocking {
		t.Error("non-baseline gap should not be force-promoted")
	}
}

func TestForceBaselineBlocking_AlreadyBlockingNotDoubleCounted(t *testing.T) {
	// A config that already blocks P1 leaves the baseline gap already
	// flagged. Force does not need to promote — it should report 0.
	gaps := []state.Gap{
		{Severity: state.SeverityP1, Description: "BASELINE: no-dead-code: unreachable branch", Blocking: true},
	}

	if n := ForceBaselineBlocking(gaps); n != 0 {
		t.Errorf("expected 0 promotions when gap already blocking, got %d", n)
	}
	if !gaps[0].Blocking {
		t.Error("gap should remain blocking")
	}
}

func TestForceBaselineBlocking_OverridesPermissiveConfig(t *testing.T) {
	// Simulate a team config that only blocks P0 (BlockOn=[P0]). Baseline
	// P1 gaps would slip through; ForceBaselineBlocking must catch them.
	// This is the whole point of the override: baseline is non-negotiable
	// regardless of config.
	gaps := []state.Gap{
		// Team config already dropped Blocking=false for the P1 secret.
		{Severity: state.SeverityP1, Description: "BASELINE: no-secrets: literal token in config.go", Blocking: false},
	}

	if n := ForceBaselineBlocking(gaps); n != 1 {
		t.Errorf("expected baseline to override permissive config (1 promotion), got %d", n)
	}
	if !gaps[0].Blocking {
		t.Error("baseline P1 gap must be blocking even under BlockOn=[P0] config")
	}
}

func TestAllRules_Comprehensive(t *testing.T) {
	// The rule list must cover the six minimum categories from the issue
	// description. Catches accidental deletions during refactors.
	wanted := map[RuleTag]bool{
		RuleNoSecrets:     false,
		RuleHandleErrors:  false,
		RuleNoDeadCode:    false,
		RuleSelfDocNames:  false,
		RuleNoDuplication: false,
		RuleNoTodoFixme:   false,
	}
	for _, tag := range AllRules {
		if _, ok := wanted[tag]; !ok {
			t.Errorf("AllRules contains unexpected tag %q", tag)
			continue
		}
		wanted[tag] = true
	}
	for tag, seen := range wanted {
		if !seen {
			t.Errorf("AllRules missing required baseline rule %q", tag)
		}
	}
}
