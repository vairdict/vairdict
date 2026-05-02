package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseConfig_ValidFull(t *testing.T) {
	data := []byte(`
project:
  name: myproject
  type: startup
  domain: developer-tooling
  risk_tolerance: medium
agents:
  planner: claude
  coder: claude-code
  judge: claude
  model: claude-sonnet-4-20250514
environment:
  runner: github-actions
commands:
  build: make build
  test: make test
  lint: make lint
  e2e: ""
phases:
  plan:
    coverage_threshold: 85
    max_loops: 3
    severity:
      block_on: [P0, P1]
      assume_on: [P2]
      defer_on: [P3]
  code:
    max_loops: 3
    require_tests: true
    coverage_minimum: 70
  quality:
    max_loops: 3
    e2e_required: false
    pr_review_mode: auto
escalation:
  after_loops: 3
  notify_via: stdout
  channel: ""
conventions:
  language: go
  formatter: gofmt
  linter: golangci-lint
`)

	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Project.Name != "myproject" {
		t.Errorf("project.name = %q, want %q", cfg.Project.Name, "myproject")
	}
	if cfg.Project.RiskTolerance != "medium" {
		t.Errorf("project.risk_tolerance = %q, want %q", cfg.Project.RiskTolerance, "medium")
	}
	if cfg.Agents.Model != "claude-sonnet-4-20250514" {
		t.Errorf("agents.model = %q, want %q", cfg.Agents.Model, "claude-sonnet-4-20250514")
	}
	if cfg.Environment.Runner != "github-actions" {
		t.Errorf("environment.runner = %q, want %q", cfg.Environment.Runner, "github-actions")
	}
	if cfg.Commands.Build != "make build" {
		t.Errorf("commands.build = %q, want %q", cfg.Commands.Build, "make build")
	}
	if cfg.Phases.Plan.CoverageThreshold != 85 {
		t.Errorf("phases.plan.coverage_threshold = %v, want 85", cfg.Phases.Plan.CoverageThreshold)
	}
	if cfg.Phases.Plan.MaxLoops != 3 {
		t.Errorf("phases.plan.max_loops = %d, want 3", cfg.Phases.Plan.MaxLoops)
	}
	if len(cfg.Phases.Plan.Severity.BlockOn) != 2 {
		t.Errorf("phases.plan.severity.block_on len = %d, want 2", len(cfg.Phases.Plan.Severity.BlockOn))
	}
	if cfg.Phases.Code.RequireTests != true {
		t.Error("phases.code.require_tests = false, want true")
	}
	if cfg.Phases.Code.CoverageMinimum != 70 {
		t.Errorf("phases.code.coverage_minimum = %d, want 70", cfg.Phases.Code.CoverageMinimum)
	}
	if cfg.Phases.Quality.PRReviewMode != "auto" {
		t.Errorf("phases.quality.pr_review_mode = %q, want %q", cfg.Phases.Quality.PRReviewMode, "auto")
	}
	if cfg.Escalation.AfterLoops != 3 {
		t.Errorf("escalation.after_loops = %d, want 3", cfg.Escalation.AfterLoops)
	}
	if cfg.Conventions.Language != "go" {
		t.Errorf("conventions.language = %q, want %q", cfg.Conventions.Language, "go")
	}
}

func TestParseConfig_Defaults(t *testing.T) {
	data := []byte(`
project:
  name: minimal
`)

	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if cfg.Agents.Planner != "claude" {
		t.Errorf("default agents.planner = %q, want %q", cfg.Agents.Planner, "claude")
	}
	if cfg.Agents.Coder != "claude-code" {
		t.Errorf("default agents.coder = %q, want %q", cfg.Agents.Coder, "claude-code")
	}
	if cfg.Environment.Runner != "local" {
		t.Errorf("default environment.runner = %q, want %q", cfg.Environment.Runner, "local")
	}
	if cfg.Phases.Plan.MaxLoops != 3 {
		t.Errorf("default phases.plan.max_loops = %d, want 3", cfg.Phases.Plan.MaxLoops)
	}
	if cfg.Phases.Plan.CoverageThreshold != 80 {
		t.Errorf("default phases.plan.coverage_threshold = %v, want 80", cfg.Phases.Plan.CoverageThreshold)
	}
	if cfg.Phases.Code.RequireTests != true {
		t.Error("default phases.code.require_tests = false, want true")
	}
	if cfg.Phases.Quality.PRReviewMode != "auto" {
		t.Errorf("default phases.quality.pr_review_mode = %q, want %q", cfg.Phases.Quality.PRReviewMode, "auto")
	}
	if cfg.Escalation.AfterLoops != 3 {
		t.Errorf("default escalation.after_loops = %d, want 3", cfg.Escalation.AfterLoops)
	}
	if cfg.Escalation.NotifyVia != "stdout" {
		t.Errorf("default escalation.notify_via = %q, want %q", cfg.Escalation.NotifyVia, "stdout")
	}
}

func TestParseConfig_MissingRequiredName(t *testing.T) {
	data := []byte(`
project:
  type: startup
`)

	_, err := ParseConfig(data)
	if err == nil {
		t.Fatal("expected error for missing project.name, got nil")
	}
}

func TestParseConfig_InvalidMaxLoops(t *testing.T) {
	data := []byte(`
project:
  name: test
phases:
  plan:
    max_loops: 0
`)

	_, err := ParseConfig(data)
	if err == nil {
		t.Fatal("expected error for max_loops=0, got nil")
	}
}

func TestParseConfig_InvalidYAML(t *testing.T) {
	data := []byte(`{{{not yaml`)

	_, err := ParseConfig(data)
	if err == nil {
		t.Fatal("expected error for invalid yaml, got nil")
	}
}

func TestParseConfig_UnknownFieldsNoError(t *testing.T) {
	data := []byte(`
project:
  name: test
unknown_section:
  foo: bar
extra_field: 123
`)

	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("unknown fields should not cause error, got: %v", err)
	}
	if cfg.Project.Name != "test" {
		t.Errorf("project.name = %q, want %q", cfg.Project.Name, "test")
	}
}

func TestLoadConfig_File(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "vairdict.yaml")

	data := []byte(`
project:
  name: filetest
`)
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatalf("writing test file: %v", err)
	}

	cfg, err := LoadConfig(path)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Project.Name != "filetest" {
		t.Errorf("project.name = %q, want %q", cfg.Project.Name, "filetest")
	}
}

func TestLoadConfig_FileNotFound(t *testing.T) {
	_, err := LoadConfig("/nonexistent/vairdict.yaml")
	if err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}

func TestMerge(t *testing.T) {
	base := Defaults()
	base.Project.Name = "base"
	base.Agents.Model = "claude-sonnet-4-20250514"
	base.Phases.Plan.MaxLoops = 3

	overrides := Config{
		Project: ProjectConfig{
			Name: "overridden",
		},
		Phases: PhasesConfig{
			Plan: PlanPhaseConfig{
				MaxLoops: 5,
			},
		},
	}

	merged := Merge(&base, overrides)

	if merged.Project.Name != "overridden" {
		t.Errorf("merged project.name = %q, want %q", merged.Project.Name, "overridden")
	}
	if merged.Phases.Plan.MaxLoops != 5 {
		t.Errorf("merged phases.plan.max_loops = %d, want 5", merged.Phases.Plan.MaxLoops)
	}
	// Non-overridden fields should keep base values.
	if merged.Agents.Model != "claude-sonnet-4-20250514" {
		t.Errorf("merged agents.model = %q, want base value", merged.Agents.Model)
	}
	if merged.Escalation.AfterLoops != 3 {
		t.Errorf("merged escalation.after_loops = %d, want 3", merged.Escalation.AfterLoops)
	}
}

func TestParseConfig_ParallelDefaults(t *testing.T) {
	data := []byte(`
project:
  name: test
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Parallel.MaxTasks != 3 {
		t.Errorf("default parallel.max_tasks = %d, want 3", cfg.Parallel.MaxTasks)
	}
}

func TestParseConfig_ParallelExplicit(t *testing.T) {
	data := []byte(`
project:
  name: test
parallel:
  max_tasks: 5
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Parallel.MaxTasks != 5 {
		t.Errorf("parallel.max_tasks = %d, want 5", cfg.Parallel.MaxTasks)
	}
}

func TestParseConfig_ParallelMaxTasksZeroRejected(t *testing.T) {
	data := []byte(`
project:
  name: test
parallel:
  max_tasks: 0
`)
	_, err := ParseConfig(data)
	if err == nil {
		t.Fatal("expected error for max_tasks=0, got nil")
	}
}

func TestMerge_Parallel(t *testing.T) {
	base := Defaults()
	base.Project.Name = "base"

	overrides := Config{
		Parallel: ParallelConfig{MaxTasks: 8},
	}

	merged := Merge(&base, overrides)
	if merged.Parallel.MaxTasks != 8 {
		t.Errorf("merged parallel.max_tasks = %d, want 8", merged.Parallel.MaxTasks)
	}
}

func TestMerge_ParallelZeroNoOverride(t *testing.T) {
	base := Defaults()
	base.Project.Name = "base"

	overrides := Config{}

	merged := Merge(&base, overrides)
	if merged.Parallel.MaxTasks != 3 {
		t.Errorf("merged parallel.max_tasks = %d, want 3 (base default)", merged.Parallel.MaxTasks)
	}
}

func TestAgentsConfig_PerPhaseFallback(t *testing.T) {
	// Flat-only config: every per-phase getter falls back to Judge.
	// This is the legacy shape — we MUST preserve it.
	flat := AgentsConfig{Planner: "claude", Judge: "claude-cli"}
	if got := flat.PlanJudgeBackend(); got != "claude-cli" {
		t.Errorf("flat PlanJudgeBackend = %q, want claude-cli (fallback to Judge)", got)
	}
	if got := flat.CodeJudgeBackend(); got != "claude-cli" {
		t.Errorf("flat CodeJudgeBackend = %q, want claude-cli (fallback to Judge)", got)
	}
	if got := flat.QualityJudgeBackend(); got != "claude-cli" {
		t.Errorf("flat QualityJudgeBackend = %q, want claude-cli (fallback to Judge)", got)
	}

	// Per-phase only: each override is honored, no Judge to inherit.
	perPhase := AgentsConfig{
		PlanJudge:    "claude-api",
		CodeJudge:    "claude-cli",
		QualityJudge: "claude",
	}
	if got := perPhase.PlanJudgeBackend(); got != "claude-api" {
		t.Errorf("per-phase PlanJudgeBackend = %q, want claude-api", got)
	}
	if got := perPhase.CodeJudgeBackend(); got != "claude-cli" {
		t.Errorf("per-phase CodeJudgeBackend = %q, want claude-cli", got)
	}
	if got := perPhase.QualityJudgeBackend(); got != "claude" {
		t.Errorf("per-phase QualityJudgeBackend = %q, want claude", got)
	}

	// Flat with one phase overridden: the override wins for that phase
	// only; the other phases still inherit from Judge.
	mixed := AgentsConfig{Judge: "claude-cli", QualityJudge: "claude-api"}
	if got := mixed.PlanJudgeBackend(); got != "claude-cli" {
		t.Errorf("mixed PlanJudgeBackend = %q, want claude-cli (inherits Judge)", got)
	}
	if got := mixed.CodeJudgeBackend(); got != "claude-cli" {
		t.Errorf("mixed CodeJudgeBackend = %q, want claude-cli (inherits Judge)", got)
	}
	if got := mixed.QualityJudgeBackend(); got != "claude-api" {
		t.Errorf("mixed QualityJudgeBackend = %q, want claude-api (override)", got)
	}
}

func TestParseConfig_PerPhaseJudges(t *testing.T) {
	data := []byte(`
project:
  name: perphase
agents:
  judge: claude
  plan_judge: claude-api
  code_judge: claude-cli
  quality_judge: claude-api
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents.Judge != "claude" {
		t.Errorf("agents.judge = %q, want claude", cfg.Agents.Judge)
	}
	if cfg.Agents.PlanJudge != "claude-api" {
		t.Errorf("agents.plan_judge = %q, want claude-api", cfg.Agents.PlanJudge)
	}
	if cfg.Agents.CodeJudge != "claude-cli" {
		t.Errorf("agents.code_judge = %q, want claude-cli", cfg.Agents.CodeJudge)
	}
	if cfg.Agents.QualityJudge != "claude-api" {
		t.Errorf("agents.quality_judge = %q, want claude-api", cfg.Agents.QualityJudge)
	}
}

func TestMerge_PerPhaseJudges(t *testing.T) {
	base := Defaults()
	base.Project.Name = "base"
	base.Agents.Judge = "claude"

	overrides := Config{
		Agents: AgentsConfig{
			QualityJudge: "claude-api",
		},
	}
	merged := Merge(&base, overrides)

	if merged.Agents.Judge != "claude" {
		t.Errorf("Judge = %q, want claude (base preserved)", merged.Agents.Judge)
	}
	if merged.Agents.QualityJudge != "claude-api" {
		t.Errorf("QualityJudge = %q, want claude-api (overlay)", merged.Agents.QualityJudge)
	}
	if merged.Agents.PlanJudgeBackend() != "claude" {
		t.Errorf("PlanJudgeBackend = %q, want claude (inherits Judge)", merged.Agents.PlanJudgeBackend())
	}
	if merged.Agents.CodeJudgeBackend() != "claude" {
		t.Errorf("CodeJudgeBackend = %q, want claude (inherits Judge)", merged.Agents.CodeJudgeBackend())
	}
}

func TestAgentsConfig_JudgeModelFallback(t *testing.T) {
	// Existing-config shape: only Model is set. Every judge resolves to
	// it. Guards AC: configs that only set agents.model keep working.
	flat := AgentsConfig{Model: "claude-sonnet-4-20250514"}
	if got := flat.PlanJudgeModelResolved(); got != "claude-sonnet-4-20250514" {
		t.Errorf("flat plan judge model = %q, want claude-sonnet (Model fallback)", got)
	}
	if got := flat.CodeJudgeModelResolved(); got != "claude-sonnet-4-20250514" {
		t.Errorf("flat code judge model = %q, want claude-sonnet (Model fallback)", got)
	}
	if got := flat.QualityJudgeModelResolved(); got != "claude-sonnet-4-20250514" {
		t.Errorf("flat quality judge model = %q, want claude-sonnet (Model fallback)", got)
	}

	// JudgeModel pins all judges; Model stays as the producer model so
	// `agents.judge_model` is the one knob that swaps every judge.
	withGlobalJudge := AgentsConfig{
		Model:      "claude-haiku-4-5",
		JudgeModel: "claude-opus-4-7",
	}
	if got := withGlobalJudge.PlanJudgeModelResolved(); got != "claude-opus-4-7" {
		t.Errorf("plan judge model with judge_model = %q, want claude-opus", got)
	}
	if got := withGlobalJudge.QualityJudgeModelResolved(); got != "claude-opus-4-7" {
		t.Errorf("quality judge model with judge_model = %q, want claude-opus", got)
	}

	// Per-phase wins over global judge_model wins over model — the AC
	// fallback chain. quality_judge_model overrides; plan inherits the
	// global judge_model; the producer model (Model) never leaks into a
	// judge slot when judge_model is set.
	mixed := AgentsConfig{
		Model:             "claude-haiku-4-5",
		JudgeModel:        "claude-sonnet-4-6",
		QualityJudgeModel: "claude-opus-4-7",
	}
	if got := mixed.PlanJudgeModelResolved(); got != "claude-sonnet-4-6" {
		t.Errorf("plan judge model = %q, want claude-sonnet-4-6 (judge_model fallback)", got)
	}
	if got := mixed.QualityJudgeModelResolved(); got != "claude-opus-4-7" {
		t.Errorf("quality judge model = %q, want claude-opus-4-7 (per-phase override)", got)
	}
}

func TestParseConfig_JudgeModelFields(t *testing.T) {
	data := []byte(`
project:
  name: judgemodel
agents:
  model: claude-sonnet-4-20250514
  judge_model: claude-opus-4-7
  plan_judge_model: claude-sonnet-4-6
  quality_judge_model: claude-opus-4-7
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Agents.JudgeModel != "claude-opus-4-7" {
		t.Errorf("judge_model = %q, want claude-opus-4-7", cfg.Agents.JudgeModel)
	}
	if cfg.Agents.PlanJudgeModel != "claude-sonnet-4-6" {
		t.Errorf("plan_judge_model = %q, want claude-sonnet-4-6", cfg.Agents.PlanJudgeModel)
	}
	if cfg.Agents.QualityJudgeModel != "claude-opus-4-7" {
		t.Errorf("quality_judge_model = %q, want claude-opus-4-7", cfg.Agents.QualityJudgeModel)
	}
}

func TestMerge_JudgeModelOverlay(t *testing.T) {
	// Overlay (e.g. vairdict.ci.yaml) introduces a stricter judge model
	// while leaving the producer model alone — the canonical use case
	// from the issue: "swap the judge model in CI without touching the
	// completer's model".
	base := Defaults()
	base.Project.Name = "base"
	base.Agents.Model = "claude-haiku-4-5"

	overrides := Config{
		Agents: AgentsConfig{
			JudgeModel: "claude-opus-4-7",
		},
	}
	merged := Merge(&base, overrides)
	if merged.Agents.Model != "claude-haiku-4-5" {
		t.Errorf("model = %q, want claude-haiku-4-5 (producer preserved)", merged.Agents.Model)
	}
	if merged.Agents.JudgeModel != "claude-opus-4-7" {
		t.Errorf("judge_model = %q, want claude-opus-4-7", merged.Agents.JudgeModel)
	}
	if got := merged.Agents.PlanJudgeModelResolved(); got != "claude-opus-4-7" {
		t.Errorf("plan judge model = %q, want claude-opus-4-7", got)
	}
}

func TestMerge_DoesNotMutateBase(t *testing.T) {
	base := Defaults()
	base.Project.Name = "original"

	overrides := Config{
		Project: ProjectConfig{Name: "changed"},
	}

	Merge(&base, overrides)

	if base.Project.Name != "original" {
		t.Errorf("base was mutated: project.name = %q, want %q", base.Project.Name, "original")
	}
}

// TestParseConfig_StandardsBlock pins the standards: yaml block contract.
// The block parses as a map of rule name -> state ("off"/"on"/"block"),
// rules absent from the file fall through to the default ("on"), and an
// invalid state value surfaces at config-load time rather than silently
// disabling a rule.
func TestParseConfig_StandardsBlock(t *testing.T) {
	data := []byte(`
project:
  name: t
phases:
  plan:
    max_loops: 3
  code:
    max_loops: 3
  quality:
    max_loops: 3
escalation:
  after_loops: 3
parallel:
  max_tasks: 3
standards:
  naming: "on"
  indent: "off"
  error_logging: "block"
`)
	cfg, err := ParseConfig(data)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.Standards["naming"] != "on" {
		t.Errorf("naming = %q, want on", cfg.Standards["naming"])
	}
	if cfg.Standards["indent"] != "off" {
		t.Errorf("indent = %q, want off", cfg.Standards["indent"])
	}
	if cfg.Standards["error_logging"] != "block" {
		t.Errorf("error_logging = %q, want block", cfg.Standards["error_logging"])
	}

	// StandardsConfig() returns a merged Config: defaults for unset
	// rules, parsed states for set rules, with state types from the
	// standards package.
	scfg, err := cfg.StandardsConfig()
	if err != nil {
		t.Fatalf("StandardsConfig: %v", err)
	}
	if state, _ := scfg.Rule("naming"); string(state) != "on" {
		t.Errorf("scfg.naming = %q, want on", state)
	}
	if state, _ := scfg.Rule("indent"); string(state) != "off" {
		t.Errorf("scfg.indent = %q, want off", state)
	}
	if state, _ := scfg.Rule("error_logging"); string(state) != "block" {
		t.Errorf("scfg.error_logging = %q, want block", state)
	}
	// Rules not mentioned in vairdict.yaml fall through to the default.
	if state, _ := scfg.Rule("class_naming"); string(state) != "on" {
		t.Errorf("class_naming default = %q, want on (the implicit default)", state)
	}
}

// TestParseConfig_StandardsInvalidState — a typo or a wrong value in
// the standards: block must fail loudly at parse time. Silent fallback
// would be exactly the kind of "thought I disabled it but it kept
// firing" footgun this category is meant to avoid.
func TestParseConfig_StandardsInvalidState(t *testing.T) {
	data := []byte(`
project:
  name: t
phases:
  plan:
    max_loops: 3
  code:
    max_loops: 3
  quality:
    max_loops: 3
escalation:
  after_loops: 3
parallel:
  max_tasks: 3
standards:
  naming: "yes"
`)
	if _, err := ParseConfig(data); err == nil {
		t.Fatal("expected error for invalid rule state, got nil")
	}
}
