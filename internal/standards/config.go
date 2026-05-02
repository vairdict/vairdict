package standards

import (
	"fmt"
	"strings"
)

// RuleState is the per-rule setting the team chooses in vairdict.yaml.
// Three values, deliberately not a free-form string: any other input
// at config-load time is rejected so a typo can't silently disable a
// rule.
type RuleState string

const (
	// RuleStateOff disables the rule entirely. The judge prompt does
	// not list it, and any finding the model emits with this rule tag
	// is dropped by the post-processor.
	RuleStateOff RuleState = "off"
	// RuleStateOn enables the rule as inline-but-non-blocking — the
	// "easy fix" mode. The verdict gate ignores it; the inline comment
	// shows up with a one-click suggestion when the judge supplies one.
	RuleStateOn RuleState = "on"
	// RuleStateBlock promotes the rule to blocking inline. A finding
	// for a "block" rule fails the verdict gate just like a Critical
	// or High severity gap does.
	RuleStateBlock RuleState = "block"
)

// ParseRuleState parses a vairdict.yaml string value into a RuleState.
// Case-insensitive on the three accepted spellings; any other value
// is an error so the user finds out at load time, not after a wasted
// run.
func ParseRuleState(s string) (RuleState, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "off":
		return RuleStateOff, nil
	case "on":
		return RuleStateOn, nil
	case "block":
		return RuleStateBlock, nil
	}
	return "", fmt.Errorf("invalid rule state %q: must be off, on, or block", s)
}

// KnownRules is the canonical list of configurable Standards rules.
// Adding an entry here automatically gives every existing config the
// rule at the default state (on) — see Default. Security-floor rules
// (no_secrets, handle_errors) are NOT in this list: they live in the
// security package, are hardcoded blocking, and are not configurable.
var KnownRules = []string{
	"naming",
	"indent",
	"error_logging",
	"switch_for_multiple_cases",
	"class_naming",
	"no_binaries",
	"no_dead_code",
	"no_todo_fixme",
	"no_duplication",
}

// Config is the team's per-rule Standards setting, parsed from the
// `standards:` block in vairdict.yaml. Rules absent from the map fall
// through to the Default state (on) so configs stay short — a team
// only needs to mention rules whose state they're changing from the
// default.
type Config struct {
	Rules map[string]RuleState `yaml:"-"`
}

// Default returns a Config with every known rule set to RuleStateOn.
// Used as the merge base when loading vairdict.yaml so a fresh repo
// gets the full Standards lint without any explicit config edits.
func Default() Config {
	rules := make(map[string]RuleState, len(KnownRules))
	for _, r := range KnownRules {
		rules[r] = RuleStateOn
	}
	return Config{Rules: rules}
}

// Rule returns the configured state for a rule. The second return
// value is false when the rule is unknown — callers can use it to
// distinguish "rule explicitly off" from "rule never configured".
func (c Config) Rule(name string) (RuleState, bool) {
	state, ok := c.Rules[name]
	return state, ok
}

// IsEnabled reports whether a rule should produce findings — true for
// "on" and "block", false for "off" and unknown rules. The post-
// processor uses this to decide whether to keep a finding the judge
// emitted.
func (c Config) IsEnabled(rule string) bool {
	switch c.Rules[rule] {
	case RuleStateOn, RuleStateBlock:
		return true
	}
	return false
}

// IsBlocking reports whether a rule promotes its findings to blocking.
// Only "block" returns true. The verdict gate consults this when
// deciding whether a Standards finding should fail the PR.
func (c Config) IsBlocking(rule string) bool {
	return c.Rules[rule] == RuleStateBlock
}

// Finding is one Standards observation the judge emitted. It carries
// a rule tag (one of KnownRules), a one-line description, an optional
// file/line anchor, and an optional one-click suggestion block. The
// Blocking flag is set by FilterFindings based on the team's Config —
// the judge never decides on its own whether a Standards finding
// blocks; the team config does.
type Finding struct {
	Rule        string `json:"rule"`
	Description string `json:"description"`
	File        string `json:"file,omitempty"`
	Line        int    `json:"line,omitempty"`
	Suggestion  string `json:"suggestion,omitempty"`
	Blocking    bool   `json:"blocking"`
}

// FilterFindings drops findings whose rule is disabled or unknown,
// and stamps Blocking=true on findings whose rule is in the "block"
// state. This is the single post-processor step that turns the
// judge's raw Standards array into the user-facing Standards list.
//
// The drop step is the built-in false-positive guard: if the judge
// invents a finding for a rule the team turned off — or hallucinates
// a rule name — it never reaches the PR.
func FilterFindings(findings []Finding, cfg Config) []Finding {
	out := make([]Finding, 0, len(findings))
	for _, f := range findings {
		if !cfg.IsEnabled(f.Rule) {
			continue
		}
		f.Blocking = cfg.IsBlocking(f.Rule)
		out = append(out, f)
	}
	return out
}
