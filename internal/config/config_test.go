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
