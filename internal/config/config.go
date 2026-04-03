package config

import (
	"fmt"
	"log/slog"
	"os"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Project     ProjectConfig     `yaml:"project"`
	Agents      AgentsConfig      `yaml:"agents"`
	Environment EnvironmentConfig `yaml:"environment"`
	Commands    CommandsConfig    `yaml:"commands"`
	Phases      PhasesConfig      `yaml:"phases"`
	Escalation  EscalationConfig  `yaml:"escalation"`
	Conventions ConventionsConfig `yaml:"conventions"`
}

type ProjectConfig struct {
	Name          string `yaml:"name"`
	Type          string `yaml:"type"`
	Domain        string `yaml:"domain"`
	RiskTolerance string `yaml:"risk_tolerance"`
}

type AgentsConfig struct {
	Planner string `yaml:"planner"`
	Coder   string `yaml:"coder"`
	Judge   string `yaml:"judge"`
	Model   string `yaml:"model"`
}

type EnvironmentConfig struct {
	Runner string `yaml:"runner"`
}

type CommandsConfig struct {
	Build string `yaml:"build"`
	Test  string `yaml:"test"`
	Lint  string `yaml:"lint"`
	E2E   string `yaml:"e2e"`
}

type PhasesConfig struct {
	Plan    PlanPhaseConfig    `yaml:"plan"`
	Code    CodePhaseConfig    `yaml:"code"`
	Quality QualityPhaseConfig `yaml:"quality"`
}

type PlanPhaseConfig struct {
	CoverageThreshold float64        `yaml:"coverage_threshold"`
	MaxLoops          int            `yaml:"max_loops"`
	Severity          SeverityConfig `yaml:"severity"`
}

type CodePhaseConfig struct {
	MaxLoops        int  `yaml:"max_loops"`
	RequireTests    bool `yaml:"require_tests"`
	CoverageMinimum int  `yaml:"coverage_minimum"`
}

type QualityPhaseConfig struct {
	MaxLoops     int    `yaml:"max_loops"`
	E2ERequired  bool   `yaml:"e2e_required"`
	PRReviewMode string `yaml:"pr_review_mode"`
}

type SeverityConfig struct {
	BlockOn  []string `yaml:"block_on"`
	AssumeOn []string `yaml:"assume_on"`
	DeferOn  []string `yaml:"defer_on"`
}

type EscalationConfig struct {
	AfterLoops int    `yaml:"after_loops"`
	NotifyVia  string `yaml:"notify_via"`
	Channel    string `yaml:"channel"`
}

type ConventionsConfig struct {
	Language  string `yaml:"language"`
	Formatter string `yaml:"formatter"`
	Linter    string `yaml:"linter"`
}

// Defaults returns a Config with sensible defaults for all optional fields.
func Defaults() Config {
	return Config{
		Agents: AgentsConfig{
			Planner: "claude",
			Coder:   "claude-code",
			Judge:   "claude",
			Model:   "claude-sonnet-4-20250514",
		},
		Environment: EnvironmentConfig{
			Runner: "local",
		},
		Phases: PhasesConfig{
			Plan: PlanPhaseConfig{
				CoverageThreshold: 80,
				MaxLoops:          3,
				Severity: SeverityConfig{
					BlockOn:  []string{"P0", "P1"},
					AssumeOn: []string{"P2"},
					DeferOn:  []string{"P3"},
				},
			},
			Code: CodePhaseConfig{
				MaxLoops:        3,
				RequireTests:    true,
				CoverageMinimum: 70,
			},
			Quality: QualityPhaseConfig{
				MaxLoops:     3,
				E2ERequired:  false,
				PRReviewMode: "auto",
			},
		},
		Escalation: EscalationConfig{
			AfterLoops: 3,
			NotifyVia:  "stdout",
		},
	}
}

// LoadConfig reads a vairdict.yaml file and returns a validated Config.
// It applies defaults for missing optional fields and warns on unknown fields.
func LoadConfig(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading config: %w", err)
	}

	return ParseConfig(data)
}

// ParseConfig parses raw YAML bytes into a validated Config.
func ParseConfig(data []byte) (*Config, error) {
	cfg := Defaults()

	// First pass: decode into the typed struct.
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return nil, fmt.Errorf("parsing config: %w", err)
	}

	// Second pass: check for unknown fields.
	warnUnknownFields(data)

	if err := validate(&cfg); err != nil {
		return nil, err
	}

	return &cfg, nil
}

// Merge applies task-level overrides on top of an existing config.
// Only non-zero override values are applied.
func Merge(base *Config, overrides Config) *Config {
	merged := *base

	// Project
	if overrides.Project.Name != "" {
		merged.Project.Name = overrides.Project.Name
	}
	if overrides.Project.Type != "" {
		merged.Project.Type = overrides.Project.Type
	}
	if overrides.Project.Domain != "" {
		merged.Project.Domain = overrides.Project.Domain
	}
	if overrides.Project.RiskTolerance != "" {
		merged.Project.RiskTolerance = overrides.Project.RiskTolerance
	}

	// Agents
	if overrides.Agents.Planner != "" {
		merged.Agents.Planner = overrides.Agents.Planner
	}
	if overrides.Agents.Coder != "" {
		merged.Agents.Coder = overrides.Agents.Coder
	}
	if overrides.Agents.Judge != "" {
		merged.Agents.Judge = overrides.Agents.Judge
	}
	if overrides.Agents.Model != "" {
		merged.Agents.Model = overrides.Agents.Model
	}

	// Environment
	if overrides.Environment.Runner != "" {
		merged.Environment.Runner = overrides.Environment.Runner
	}

	// Commands
	if overrides.Commands.Build != "" {
		merged.Commands.Build = overrides.Commands.Build
	}
	if overrides.Commands.Test != "" {
		merged.Commands.Test = overrides.Commands.Test
	}
	if overrides.Commands.Lint != "" {
		merged.Commands.Lint = overrides.Commands.Lint
	}
	if overrides.Commands.E2E != "" {
		merged.Commands.E2E = overrides.Commands.E2E
	}

	// Phases — only override non-zero values
	if overrides.Phases.Plan.MaxLoops > 0 {
		merged.Phases.Plan.MaxLoops = overrides.Phases.Plan.MaxLoops
	}
	if overrides.Phases.Plan.CoverageThreshold > 0 {
		merged.Phases.Plan.CoverageThreshold = overrides.Phases.Plan.CoverageThreshold
	}
	if len(overrides.Phases.Plan.Severity.BlockOn) > 0 {
		merged.Phases.Plan.Severity.BlockOn = overrides.Phases.Plan.Severity.BlockOn
	}
	if len(overrides.Phases.Plan.Severity.AssumeOn) > 0 {
		merged.Phases.Plan.Severity.AssumeOn = overrides.Phases.Plan.Severity.AssumeOn
	}
	if len(overrides.Phases.Plan.Severity.DeferOn) > 0 {
		merged.Phases.Plan.Severity.DeferOn = overrides.Phases.Plan.Severity.DeferOn
	}
	if overrides.Phases.Code.MaxLoops > 0 {
		merged.Phases.Code.MaxLoops = overrides.Phases.Code.MaxLoops
	}
	if overrides.Phases.Code.CoverageMinimum > 0 {
		merged.Phases.Code.CoverageMinimum = overrides.Phases.Code.CoverageMinimum
	}
	if overrides.Phases.Quality.MaxLoops > 0 {
		merged.Phases.Quality.MaxLoops = overrides.Phases.Quality.MaxLoops
	}
	if overrides.Phases.Quality.PRReviewMode != "" {
		merged.Phases.Quality.PRReviewMode = overrides.Phases.Quality.PRReviewMode
	}

	// Escalation
	if overrides.Escalation.AfterLoops > 0 {
		merged.Escalation.AfterLoops = overrides.Escalation.AfterLoops
	}
	if overrides.Escalation.NotifyVia != "" {
		merged.Escalation.NotifyVia = overrides.Escalation.NotifyVia
	}
	if overrides.Escalation.Channel != "" {
		merged.Escalation.Channel = overrides.Escalation.Channel
	}

	// Conventions
	if overrides.Conventions.Language != "" {
		merged.Conventions.Language = overrides.Conventions.Language
	}
	if overrides.Conventions.Formatter != "" {
		merged.Conventions.Formatter = overrides.Conventions.Formatter
	}
	if overrides.Conventions.Linter != "" {
		merged.Conventions.Linter = overrides.Conventions.Linter
	}

	return &merged
}

func validate(cfg *Config) error {
	if cfg.Project.Name == "" {
		return fmt.Errorf("validating config: project.name is required")
	}
	if cfg.Phases.Plan.MaxLoops < 1 {
		return fmt.Errorf("validating config: phases.plan.max_loops must be >= 1")
	}
	if cfg.Phases.Code.MaxLoops < 1 {
		return fmt.Errorf("validating config: phases.code.max_loops must be >= 1")
	}
	if cfg.Phases.Quality.MaxLoops < 1 {
		return fmt.Errorf("validating config: phases.quality.max_loops must be >= 1")
	}
	if cfg.Escalation.AfterLoops < 1 {
		return fmt.Errorf("validating config: escalation.after_loops must be >= 1")
	}
	return nil
}

// warnUnknownFields decodes yaml into a generic map and logs any keys
// not present in the known top-level field set.
func warnUnknownFields(data []byte) {
	known := map[string]bool{
		"project": true, "agents": true, "environment": true,
		"commands": true, "phases": true, "escalation": true,
		"conventions": true,
	}

	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return
	}
	for key := range raw {
		if !known[key] {
			slog.Warn("unknown config field", "field", key)
		}
	}
}
