package main

import (
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
)

func TestChooseBackend(t *testing.T) {
	cases := []struct {
		name         string
		setting      string
		cliAvailable bool
		want         backendKind
		wantErr      bool
	}{
		// Default (empty / "claude"): try cli, fall back to api.
		{"default cli available", "", true, backendClaudeCLI, false},
		{"default no cli", "", false, backendClaudeAPI, false},
		{"claude cli available", "claude", true, backendClaudeCLI, false},
		{"claude no cli", "claude", false, backendClaudeAPI, false},
		// "auto" is a deprecated alias for "claude" — accepted silently.
		{"auto cli available", "auto", true, backendClaudeCLI, false},
		{"auto no cli", "auto", false, backendClaudeAPI, false},
		// Strict modes — chooseBackend itself doesn't gate on availability.
		// claude-cli failing on a host without claude is reported by the
		// callsite when the subprocess actually runs.
		{"strict cli", "claude-cli", false, backendClaudeCLI, false},
		{"strict cli with cli", "claude-cli", true, backendClaudeCLI, false},
		{"strict api", "claude-api", true, backendClaudeAPI, false},
		{"strict api no cli", "claude-api", false, backendClaudeAPI, false},
		// Typos surface as a hard error so users don't silently get the
		// wrong backend.
		{"unknown value", "openai", true, "", true},
		{"old http alias rejected", "claude-http", true, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseBackend(tc.setting, tc.cliAvailable)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func TestBackendForRole(t *testing.T) {
	// Flat-only — every judge role inherits agents.judge.
	flat := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude",
		Coder:   "claude-code",
		Judge:   "claude-cli",
	}}
	if got := backendForRole(flat, rolePlanner); got != "claude" {
		t.Errorf("rolePlanner = %q, want claude", got)
	}
	if got := backendForRole(flat, rolePlanJudge); got != "claude-cli" {
		t.Errorf("rolePlanJudge (flat) = %q, want claude-cli", got)
	}
	if got := backendForRole(flat, roleCodeJudge); got != "claude-cli" {
		t.Errorf("roleCodeJudge (flat) = %q, want claude-cli", got)
	}
	if got := backendForRole(flat, roleQualityJudge); got != "claude-cli" {
		t.Errorf("roleQualityJudge (flat) = %q, want claude-cli", got)
	}

	// Mixed — one phase override, others inherit.
	mixed := &config.Config{Agents: config.AgentsConfig{
		Planner:      "claude",
		Judge:        "claude-cli",
		QualityJudge: "claude-api",
	}}
	if got := backendForRole(mixed, rolePlanJudge); got != "claude-cli" {
		t.Errorf("rolePlanJudge (mixed) = %q, want claude-cli", got)
	}
	if got := backendForRole(mixed, roleQualityJudge); got != "claude-api" {
		t.Errorf("roleQualityJudge (mixed) = %q, want claude-api", got)
	}
}

// TestModelForRole: the planner shares agents.model unchanged so swapping
// the judge model never accidentally rewrites the producer; each judge
// applies the {plan,code,quality}_judge_model → judge_model → model
// fallback chain so a single agents.judge_model swaps every judge slot.
func TestModelForRole(t *testing.T) {
	// Legacy config: only Model is set. Every role resolves to it.
	flat := &config.Config{Agents: config.AgentsConfig{
		Model: "claude-sonnet-4-20250514",
	}}
	cases := []struct {
		role completerRole
		want string
	}{
		{rolePlanner, "claude-sonnet-4-20250514"},
		{rolePlanJudge, "claude-sonnet-4-20250514"},
		{roleCodeJudge, "claude-sonnet-4-20250514"},
		{roleQualityJudge, "claude-sonnet-4-20250514"},
	}
	for _, tc := range cases {
		if got := modelForRole(flat, tc.role); got != tc.want {
			t.Errorf("flat modelForRole(%s) = %q, want %q", tc.role, got, tc.want)
		}
	}

	// judge_model set: judges swap, planner stays on Model. This is the
	// canonical use case from the issue — judge with a stricter model
	// while the cheap producer keeps the original.
	swap := &config.Config{Agents: config.AgentsConfig{
		Model:      "claude-haiku-4-5",
		JudgeModel: "claude-opus-4-7",
	}}
	if got := modelForRole(swap, rolePlanner); got != "claude-haiku-4-5" {
		t.Errorf("swap planner = %q, want claude-haiku-4-5 (Model preserved)", got)
	}
	if got := modelForRole(swap, rolePlanJudge); got != "claude-opus-4-7" {
		t.Errorf("swap plan judge = %q, want claude-opus-4-7 (judge_model)", got)
	}
	if got := modelForRole(swap, roleQualityJudge); got != "claude-opus-4-7" {
		t.Errorf("swap quality judge = %q, want claude-opus-4-7 (judge_model)", got)
	}

	// Per-phase override wins over the global judge_model.
	mixed := &config.Config{Agents: config.AgentsConfig{
		Model:             "claude-haiku-4-5",
		JudgeModel:        "claude-sonnet-4-6",
		QualityJudgeModel: "claude-opus-4-7",
	}}
	if got := modelForRole(mixed, rolePlanJudge); got != "claude-sonnet-4-6" {
		t.Errorf("mixed plan judge = %q, want claude-sonnet-4-6 (judge_model)", got)
	}
	if got := modelForRole(mixed, roleQualityJudge); got != "claude-opus-4-7" {
		t.Errorf("mixed quality judge = %q, want claude-opus-4-7 (per-phase)", got)
	}
}

// TestResolveCompleter_JudgeModelOverride: when judge_model is set, the
// API client returned for the judge slots is pinned to that model while
// the planner stays on agents.model. Hits the AC: WithModel reaches the
// API call, completer calls do not get the judge model.
func TestResolveCompleter_JudgeModelOverride(t *testing.T) {
	// API key must be present for claude-api to construct.
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-judgemodel")

	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner:    "claude-api",
		Judge:      "claude-api",
		Model:      "claude-haiku-4-5",
		JudgeModel: "claude-opus-4-7",
	}}

	planner, _, err := resolveCompleter(cfg, rolePlanner)
	if err != nil {
		t.Fatalf("resolveCompleter(planner): %v", err)
	}
	if got := planner.Model(); got != "claude-haiku-4-5" {
		t.Errorf("planner client model = %q, want claude-haiku-4-5 (Model untouched)", got)
	}

	for _, role := range []completerRole{rolePlanJudge, roleQualityJudge, roleCodeJudge} {
		c, _, err := resolveCompleter(cfg, role)
		if err != nil {
			t.Fatalf("resolveCompleter(%s): %v", role, err)
		}
		if got := c.Model(); got != "claude-opus-4-7" {
			t.Errorf("%s client model = %q, want claude-opus-4-7 (judge_model)", role, got)
		}
	}
}

// TestResolveCompleter_NoJudgeModelInheritsModel: with judge_model unset,
// every judge falls back to agents.model so existing configs keep
// working unchanged. AC: configs that only set agents.model keep
// working — purely additive.
func TestResolveCompleter_NoJudgeModelInheritsModel(t *testing.T) {
	t.Setenv("ANTHROPIC_API_KEY", "sk-test-fallback")
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude-api",
		Judge:   "claude-api",
		Model:   "claude-sonnet-4-20250514",
	}}

	for _, role := range []completerRole{rolePlanner, rolePlanJudge, roleQualityJudge} {
		c, _, err := resolveCompleter(cfg, role)
		if err != nil {
			t.Fatalf("resolveCompleter(%s): %v", role, err)
		}
		if got := c.Model(); got != "claude-sonnet-4-20250514" {
			t.Errorf("%s client model = %q, want claude-sonnet (Model fallback)", role, got)
		}
	}
}

// TestValidateBackends_FlatOnlyLegacy: a config that only sets the flat
// fields must validate identically to pre-issue-128 behavior. With CLI
// available and Judge=claude (smart), no error.
func TestValidateBackends_FlatOnlyLegacy(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude",
		Coder:   "claude-code",
		Judge:   "claude",
	}}
	probes := backendProbes{
		cliAvailable:  func(string) bool { return true },
		apiKeyPresent: func() bool { return false },
	}
	if err := validateBackends(cfg, probes); err != nil {
		t.Errorf("flat legacy config should validate, got: %v", err)
	}
}

// TestValidateBackends_PerPhaseOnly: explicit pinned backends per phase
// validate when their requirements are met.
func TestValidateBackends_PerPhaseOnly(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner:      "claude-api",
		Coder:        "claude-code",
		Judge:        "claude-api",
		PlanJudge:    "claude-cli",
		CodeJudge:    "claude-api",
		QualityJudge: "claude-cli",
	}}
	probes := backendProbes{
		cliAvailable:  func(string) bool { return true },
		apiKeyPresent: func() bool { return true },
	}
	if err := validateBackends(cfg, probes); err != nil {
		t.Errorf("per-phase config with all reqs met should validate, got: %v", err)
	}
}

// TestValidateBackends_MissingBinary: claude-cli pinned but `claude` not
// on PATH → must error and name both the role and the missing binary.
func TestValidateBackends_MissingBinary(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude",
		Coder:   "claude-code",
		Judge:   "claude",
		// PlanJudge is the explicit pinned slot that should fail validation.
		PlanJudge: "claude-cli",
	}}
	probes := backendProbes{
		cliAvailable:  func(string) bool { return false }, // no claude
		apiKeyPresent: func() bool { return true },
	}
	err := validateBackends(cfg, probes)
	if err == nil {
		t.Fatal("expected error for missing claude binary")
	}
	if !strings.Contains(err.Error(), "plan_judge") {
		t.Errorf("error should name the role (plan_judge), got: %v", err)
	}
	if !strings.Contains(err.Error(), "claude") {
		t.Errorf("error should name the missing binary, got: %v", err)
	}
}

// TestValidateBackends_MissingAPIKey: claude-api pinned but no key →
// must error.
func TestValidateBackends_MissingAPIKey(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude",
		Coder:   "claude-code",
		Judge:   "claude",
		// CodeJudge is the explicit pinned slot that should fail validation.
		CodeJudge: "claude-api",
	}}
	probes := backendProbes{
		cliAvailable:  func(string) bool { return true },
		apiKeyPresent: func() bool { return false },
	}
	err := validateBackends(cfg, probes)
	if err == nil {
		t.Fatal("expected error for missing API key")
	}
	if !strings.Contains(err.Error(), "code_judge") {
		t.Errorf("error should name the role (code_judge), got: %v", err)
	}
	if !strings.Contains(err.Error(), "ANTHROPIC_API_KEY") {
		t.Errorf("error should name the missing requirement, got: %v", err)
	}
}

// TestValidateBackends_UnknownBackend: an unknown name on any role must
// error and name the role and the bad value.
func TestValidateBackends_UnknownBackend(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude",
		Coder:   "claude-code",
		Judge:   "claude",
		// Quality judge has a typo — should fail.
		QualityJudge: "openai",
	}}
	probes := backendProbes{
		cliAvailable:  func(string) bool { return true },
		apiKeyPresent: func() bool { return true },
	}
	err := validateBackends(cfg, probes)
	if err == nil {
		t.Fatal("expected error for unknown backend")
	}
	if !strings.Contains(err.Error(), "quality_judge") {
		t.Errorf("error should name the role (quality_judge), got: %v", err)
	}
	if !strings.Contains(err.Error(), "openai") {
		t.Errorf("error should echo the bad value, got: %v", err)
	}
}

// TestValidateBackends_AutoDoesNotErrorWhenFamilyUnavailable: per AC,
// smart defaults ("claude" / "auto" / empty) MUST NOT error even when
// no family is available — they fall through at runtime.
func TestValidateBackends_AutoDoesNotErrorWhenFamilyUnavailable(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude",
		Coder:   "claude-code",
		Judge:   "auto",
	}}
	// Neither the CLI nor an API key — but every completer slot uses
	// the smart default, so validation must still pass. Coder still
	// needs the binary, so we pretend that's available.
	probes := backendProbes{
		cliAvailable:  func(name string) bool { return name == "claude" },
		apiKeyPresent: func() bool { return false },
	}
	if err := validateBackends(cfg, probes); err != nil {
		t.Errorf("smart-default config should not error, got: %v", err)
	}
}

// TestValidateBackends_CoderNeedsBinary: claude-code is the family CLI
// runner — it requires `claude` on PATH. Validating with no binary
// must error and name agents.coder.
func TestValidateBackends_CoderNeedsBinary(t *testing.T) {
	cfg := &config.Config{Agents: config.AgentsConfig{
		Planner: "claude-api",
		Coder:   "claude-code",
		Judge:   "claude-api",
	}}
	probes := backendProbes{
		cliAvailable:  func(string) bool { return false }, // no claude on PATH
		apiKeyPresent: func() bool { return true },
	}
	err := validateBackends(cfg, probes)
	if err == nil {
		t.Fatal("expected error when claude is missing for the coder")
	}
	if !strings.Contains(err.Error(), "agents.coder") {
		t.Errorf("error should name the coder role, got: %v", err)
	}
}
