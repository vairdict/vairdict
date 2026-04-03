package bootstrap

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"gopkg.in/yaml.v3"
)

func TestBuildConfig_Startup(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "myapp",
		ProjectType:   "startup",
		RiskTolerance: "high",
	}
	detected := DetectionResult{
		Language:  "go",
		Formatter: "gofmt",
		Linter:    "golangci-lint",
	}

	cfg := buildConfig(answers, detected)

	if cfg.Project.Name != "myapp" {
		t.Errorf("Project.Name = %q, want %q", cfg.Project.Name, "myapp")
	}
	if cfg.Project.Type != "startup" {
		t.Errorf("Project.Type = %q, want %q", cfg.Project.Type, "startup")
	}
	if cfg.Project.RiskTolerance != "high" {
		t.Errorf("Project.RiskTolerance = %q, want %q", cfg.Project.RiskTolerance, "high")
	}
	if cfg.Conventions.Language != "go" {
		t.Errorf("Conventions.Language = %q, want %q", cfg.Conventions.Language, "go")
	}
	if cfg.Conventions.Formatter != "gofmt" {
		t.Errorf("Conventions.Formatter = %q, want %q", cfg.Conventions.Formatter, "gofmt")
	}
	if cfg.Conventions.Linter != "golangci-lint" {
		t.Errorf("Conventions.Linter = %q, want %q", cfg.Conventions.Linter, "golangci-lint")
	}
	// Startup defaults: lower thresholds.
	if cfg.Phases.Plan.CoverageThreshold != 70 {
		t.Errorf("Plan.CoverageThreshold = %v, want 70", cfg.Phases.Plan.CoverageThreshold)
	}
	if cfg.Phases.Code.CoverageMinimum != 60 {
		t.Errorf("Code.CoverageMinimum = %d, want 60", cfg.Phases.Code.CoverageMinimum)
	}
}

func TestBuildConfig_Regulated(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "bankapp",
		ProjectType:   "regulated",
		RiskTolerance: "low",
	}
	detected := DetectionResult{Language: "java"}

	cfg := buildConfig(answers, detected)

	if cfg.Phases.Plan.CoverageThreshold != 95 {
		t.Errorf("Plan.CoverageThreshold = %v, want 95", cfg.Phases.Plan.CoverageThreshold)
	}
	if cfg.Phases.Code.CoverageMinimum != 90 {
		t.Errorf("Code.CoverageMinimum = %d, want 90", cfg.Phases.Code.CoverageMinimum)
	}
	if !cfg.Phases.Quality.E2ERequired {
		t.Error("Quality.E2ERequired = false, want true")
	}
	if cfg.Phases.Quality.PRReviewMode != "manual" {
		t.Errorf("Quality.PRReviewMode = %q, want %q", cfg.Phases.Quality.PRReviewMode, "manual")
	}
}

func TestBuildConfig_Enterprise(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "corpapp",
		ProjectType:   "enterprise",
		RiskTolerance: "medium",
	}
	detected := DetectionResult{}

	cfg := buildConfig(answers, detected)

	if cfg.Phases.Plan.CoverageThreshold != 85 {
		t.Errorf("Plan.CoverageThreshold = %v, want 85", cfg.Phases.Plan.CoverageThreshold)
	}
	if cfg.Phases.Code.CoverageMinimum != 80 {
		t.Errorf("Code.CoverageMinimum = %d, want 80", cfg.Phases.Code.CoverageMinimum)
	}
}

func TestBuildConfig_Opensource(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "oss-tool",
		ProjectType:   "opensource",
		RiskTolerance: "medium",
	}
	detected := DetectionResult{}

	cfg := buildConfig(answers, detected)

	if cfg.Phases.Plan.CoverageThreshold != 75 {
		t.Errorf("Plan.CoverageThreshold = %v, want 75", cfg.Phases.Plan.CoverageThreshold)
	}
	if cfg.Phases.Code.CoverageMinimum != 70 {
		t.Errorf("Code.CoverageMinimum = %d, want 70", cfg.Phases.Code.CoverageMinimum)
	}
}

func TestBuildConfig_WithNotifyChannel(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "myapp",
		ProjectType:   "startup",
		RiskTolerance: "medium",
		NotifyChannel: "#dev-alerts",
	}
	detected := DetectionResult{}

	cfg := buildConfig(answers, detected)

	if cfg.Escalation.NotifyVia != "slack" {
		t.Errorf("Escalation.NotifyVia = %q, want %q", cfg.Escalation.NotifyVia, "slack")
	}
	if cfg.Escalation.Channel != "#dev-alerts" {
		t.Errorf("Escalation.Channel = %q, want %q", cfg.Escalation.Channel, "#dev-alerts")
	}
}

func TestBuildConfig_WithoutNotifyChannel(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "myapp",
		ProjectType:   "startup",
		RiskTolerance: "medium",
	}
	detected := DetectionResult{}

	cfg := buildConfig(answers, detected)

	if cfg.Escalation.NotifyVia != "stdout" {
		t.Errorf("Escalation.NotifyVia = %q, want %q", cfg.Escalation.NotifyVia, "stdout")
	}
}

func TestBuildConfig_GoCommands(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "goapp",
		ProjectType:   "startup",
		RiskTolerance: "medium",
	}
	detected := DetectionResult{Language: "go"}

	cfg := buildConfig(answers, detected)

	if cfg.Commands.Build != "go build ./..." {
		t.Errorf("Commands.Build = %q, want %q", cfg.Commands.Build, "go build ./...")
	}
	if cfg.Commands.Test != "go test ./..." {
		t.Errorf("Commands.Test = %q, want %q", cfg.Commands.Test, "go test ./...")
	}
	if cfg.Commands.Lint != "golangci-lint run ./..." {
		t.Errorf("Commands.Lint = %q, want %q", cfg.Commands.Lint, "golangci-lint run ./...")
	}
}

func TestBuildConfig_JSCommands(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "webapp",
		ProjectType:   "startup",
		RiskTolerance: "medium",
	}
	detected := DetectionResult{Language: "javascript"}

	cfg := buildConfig(answers, detected)

	if cfg.Commands.Build != "npm run build" {
		t.Errorf("Commands.Build = %q, want %q", cfg.Commands.Build, "npm run build")
	}
	if cfg.Commands.Test != "npm test" {
		t.Errorf("Commands.Test = %q, want %q", cfg.Commands.Test, "npm test")
	}
}

func TestBuildConfig_GeneratesValidYAML(t *testing.T) {
	answers := promptAnswers{
		ProjectName:   "testproject",
		ProjectType:   "startup",
		RiskTolerance: "medium",
	}
	detected := DetectionResult{Language: "go", Formatter: "gofmt", Linter: "golangci-lint"}

	cfg := buildConfig(answers, detected)

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	// Validate via config package.
	parsed, err := config.ParseConfig(data)
	if err != nil {
		t.Fatalf("generated config failed validation: %v", err)
	}

	if parsed.Project.Name != "testproject" {
		t.Errorf("round-trip project.name = %q, want %q", parsed.Project.Name, "testproject")
	}
}

func TestBuildConfig_WriteAndReload(t *testing.T) {
	dir := t.TempDir()
	answers := promptAnswers{
		ProjectName:   "roundtrip",
		ProjectType:   "enterprise",
		RiskTolerance: "low",
	}
	detected := DetectionResult{Language: "rust", Formatter: "rustfmt", Linter: "clippy"}

	cfg := buildConfig(answers, detected)

	data, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal error: %v", err)
	}

	outPath := filepath.Join(dir, "vairdict.yaml")
	if err := os.WriteFile(outPath, data, 0644); err != nil {
		t.Fatalf("write error: %v", err)
	}

	loaded, err := config.LoadConfig(outPath)
	if err != nil {
		t.Fatalf("LoadConfig error: %v", err)
	}
	if loaded.Project.Name != "roundtrip" {
		t.Errorf("loaded project.name = %q, want %q", loaded.Project.Name, "roundtrip")
	}
	if loaded.Conventions.Language != "rust" {
		t.Errorf("loaded conventions.language = %q, want %q", loaded.Conventions.Language, "rust")
	}
	if loaded.Conventions.Linter != "clippy" {
		t.Errorf("loaded conventions.linter = %q, want %q", loaded.Conventions.Linter, "clippy")
	}
}

func TestDefaultsForType_UnknownFallsToStartup(t *testing.T) {
	unknown := DefaultsForType("unknown")
	startup := DefaultsForType("startup")

	if unknown.Phases.Plan.CoverageThreshold != startup.Phases.Plan.CoverageThreshold {
		t.Errorf("unknown type threshold = %v, want startup threshold %v",
			unknown.Phases.Plan.CoverageThreshold, startup.Phases.Plan.CoverageThreshold)
	}
}
