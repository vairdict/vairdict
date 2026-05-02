package standards

import (
	"testing"
)

// TestRuleStateValues pins the three valid states a configurable
// standards rule can be in: off, on, block. "off" disables the rule
// (judge does not flag, post-processor drops findings); "on" emits
// inline non-blocking findings — the easy-fix mode; "block" promotes
// the rule to blocking inline so the verdict gate fails when violated.
func TestRuleStateValues(t *testing.T) {
	cases := []struct {
		got  RuleState
		want string
	}{
		{RuleStateOff, "off"},
		{RuleStateOn, "on"},
		{RuleStateBlock, "block"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("RuleState %q: got %q", c.want, string(c.got))
		}
	}
}

// TestParseRuleState accepts the three canonical strings (case-
// insensitive) and rejects anything else so a typo in vairdict.yaml
// surfaces at config-load time rather than silently disabling a rule.
func TestParseRuleState(t *testing.T) {
	cases := []struct {
		in      string
		want    RuleState
		wantErr bool
	}{
		{"off", RuleStateOff, false},
		{"on", RuleStateOn, false},
		{"block", RuleStateBlock, false},
		{"OFF", RuleStateOff, false},
		{"On", RuleStateOn, false},
		{"BLOCK", RuleStateBlock, false},
		{"", "", true},
		{"yes", "", true},
		{"true", "", true},
	}
	for _, c := range cases {
		got, err := ParseRuleState(c.in)
		if (err != nil) != c.wantErr {
			t.Errorf("ParseRuleState(%q): err = %v, wantErr = %v", c.in, err, c.wantErr)
			continue
		}
		if !c.wantErr && got != c.want {
			t.Errorf("ParseRuleState(%q): got %q, want %q", c.in, got, c.want)
		}
	}
}

// TestKnownRules pins the configurable Standards rule set. Adding a
// rule means every existing config gets it automatically (default
// state is "on") so the post-processor can start filtering against it
// without breaking older deployments. Removing a rule is a breaking
// change — the test guards against accidentally dropping one in a
// refactor.
func TestKnownRules(t *testing.T) {
	want := map[string]bool{
		"naming":                    true,
		"indent":                    true,
		"error_logging":             true,
		"switch_for_multiple_cases": true,
		"class_naming":              true,
		"no_binaries":               true,
		"no_dead_code":              true,
		"no_todo_fixme":             true,
		"no_duplication":            true,
	}
	got := make(map[string]bool, len(KnownRules))
	for _, r := range KnownRules {
		got[r] = true
	}
	for r := range want {
		if !got[r] {
			t.Errorf("KnownRules missing %q", r)
		}
	}
	for r := range got {
		if !want[r] {
			t.Errorf("KnownRules has unexpected entry %q — security-floor rules (no_secrets, handle_errors) must NOT be configurable here", r)
		}
	}
}

// TestDefaultConfig_AllOn — every known rule defaults to "on" so a
// freshly-bootstrapped repo gets the full Standards lint without any
// vairdict.yaml edits, but nothing is promoted to blocking until a team
// explicitly opts in.
func TestDefaultConfig_AllOn(t *testing.T) {
	cfg := Default()
	for _, r := range KnownRules {
		state, ok := cfg.Rule(r)
		if !ok {
			t.Errorf("Default config missing rule %q", r)
			continue
		}
		if state != RuleStateOn {
			t.Errorf("Default config rule %q: got %q, want %q", r, state, RuleStateOn)
		}
	}
}

// TestConfig_IsEnabled — "on" and "block" both count as enabled (the
// rule is in play); "off" and unknown rules are disabled. Used by the
// post-processor to decide whether to keep or drop a finding the judge
// emitted.
func TestConfig_IsEnabled(t *testing.T) {
	cfg := Config{Rules: map[string]RuleState{
		"naming":        RuleStateOn,
		"indent":        RuleStateOff,
		"error_logging": RuleStateBlock,
	}}
	cases := []struct {
		rule string
		want bool
	}{
		{"naming", true},
		{"indent", false},
		{"error_logging", true},
		{"unknown_rule", false},
	}
	for _, c := range cases {
		if got := cfg.IsEnabled(c.rule); got != c.want {
			t.Errorf("IsEnabled(%q) = %v, want %v", c.rule, got, c.want)
		}
	}
}

// TestConfig_IsBlocking — only "block" promotes a rule to blocking.
// "on" findings are inline-but-not-blocking; "off" and unknown rules
// are not blocking (and not even shown — the post-processor drops them).
func TestConfig_IsBlocking(t *testing.T) {
	cfg := Config{Rules: map[string]RuleState{
		"naming":        RuleStateOn,
		"indent":        RuleStateOff,
		"error_logging": RuleStateBlock,
	}}
	cases := []struct {
		rule string
		want bool
	}{
		{"naming", false},
		{"indent", false},
		{"error_logging", true},
		{"unknown_rule", false},
	}
	for _, c := range cases {
		if got := cfg.IsBlocking(c.rule); got != c.want {
			t.Errorf("IsBlocking(%q) = %v, want %v", c.rule, got, c.want)
		}
	}
}

// TestFilterFindings drops findings whose rule tag is disabled or
// unknown — the built-in false-positive guard. A judge that emits a
// "naming" finding when naming is "off" gets that finding silently
// dropped before it reaches the PR comment renderer. Findings for
// "block"-promoted rules carry Blocking=true so the verdict gate sees
// them.
func TestFilterFindings(t *testing.T) {
	cfg := Config{Rules: map[string]RuleState{
		"naming":        RuleStateOn,
		"indent":        RuleStateOff,
		"error_logging": RuleStateBlock,
	}}
	in := []Finding{
		{Rule: "naming", Description: "var x"},
		{Rule: "indent", Description: "tab vs space"},  // dropped — rule off
		{Rule: "error_logging", Description: "no err"}, // kept, blocking
		{Rule: "bogus", Description: "unknown rule"},   // dropped — unknown
	}
	got := FilterFindings(in, cfg)
	if len(got) != 2 {
		t.Fatalf("FilterFindings: got %d findings, want 2: %+v", len(got), got)
	}
	if got[0].Rule != "naming" || got[0].Blocking {
		t.Errorf("first finding should be naming non-blocking, got %+v", got[0])
	}
	if got[1].Rule != "error_logging" || !got[1].Blocking {
		t.Errorf("second finding should be error_logging blocking, got %+v", got[1])
	}
}
