package bootstrap

import (
	"os"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
	"gopkg.in/yaml.v3"
)

const repoRoot = "../.."

func skipIfNotRepo(t *testing.T) {
	t.Helper()
	if _, err := os.Stat(repoRoot + "/go.mod"); err != nil {
		t.Skip("not running from repo root")
	}
}

func TestDogfoodDetection(t *testing.T) {
	skipIfNotRepo(t)

	detected := Detect(repoRoot)

	if detected.Language != "go" {
		t.Errorf("language = %q, want go", detected.Language)
	}
	if detected.Formatter != "gofmt" {
		t.Errorf("formatter = %q, want gofmt", detected.Formatter)
	}
	if detected.Linter != "golangci-lint" {
		t.Errorf("linter = %q, want golangci-lint", detected.Linter)
	}
}

func TestDogfoodGeneration(t *testing.T) {
	skipIfNotRepo(t)

	detected := Detect(repoRoot)

	cfg := buildConfig(promptAnswers{
		ProjectName:   "vairdict",
		ProjectType:   "startup",
		RiskTolerance: "medium",
	}, detected)

	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	parsed, err := config.ParseConfig(yamlBytes)
	if err != nil {
		t.Fatalf("generated config failed validation: %v", err)
	}

	if parsed.Project.Name != "vairdict" {
		t.Errorf("project.name = %q", parsed.Project.Name)
	}
	if parsed.Conventions.Language != "go" {
		t.Errorf("conventions.language = %q", parsed.Conventions.Language)
	}
	if parsed.Conventions.Formatter != "gofmt" {
		t.Errorf("conventions.formatter = %q", parsed.Conventions.Formatter)
	}
	if parsed.Conventions.Linter != "golangci-lint" {
		t.Errorf("conventions.linter = %q", parsed.Conventions.Linter)
	}
}

func TestDogfoodExistingConfigValid(t *testing.T) {
	skipIfNotRepo(t)

	cfg, err := config.LoadConfig(repoRoot + "/vairdict.yaml")
	if err != nil {
		t.Fatalf("existing vairdict.yaml is not valid: %v", err)
	}

	if cfg.Project.Name != "vairdict" {
		t.Errorf("project.name = %q", cfg.Project.Name)
	}
	if cfg.Conventions.Language != "go" {
		t.Errorf("conventions.language = %q", cfg.Conventions.Language)
	}
}
