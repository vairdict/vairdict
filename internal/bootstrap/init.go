package bootstrap

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"

	"strings"

	"github.com/charmbracelet/huh"
	"github.com/vairdict/vairdict/internal/config"
	"gopkg.in/yaml.v3"
)

// Run executes the full init flow: detect repo context, prompt the user for
// missing information, generate a config, validate it, and write vairdict.yaml.
func Run(dir string) error {
	outPath := filepath.Join(dir, "vairdict.yaml")

	// Check for existing file.
	if fileExists(outPath) {
		var overwrite bool
		err := huh.NewConfirm().
			Title("vairdict.yaml already exists. Overwrite?").
			Value(&overwrite).
			Run()
		if err != nil {
			return fmt.Errorf("confirming overwrite: %w", err)
		}
		if !overwrite {
			slog.Info("init cancelled, existing vairdict.yaml kept")
			return nil
		}
	}

	// Detect what we can from the repo.
	detected := Detect(dir)

	// Gather user input for fields we cannot detect.
	answers, err := prompt(detected, dir)
	if err != nil {
		return fmt.Errorf("prompting user: %w", err)
	}

	// Build the config.
	cfg := buildConfig(answers, detected)

	// Validate via the config package.
	yamlBytes, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshaling config: %w", err)
	}
	if _, err := config.ParseConfig(yamlBytes); err != nil {
		return fmt.Errorf("validating generated config: %w", err)
	}

	// Write the file.
	if err := os.WriteFile(outPath, yamlBytes, 0644); err != nil {
		return fmt.Errorf("writing vairdict.yaml: %w", err)
	}

	slog.Info("vairdict.yaml written", "path", outPath)

	// Prompt for API key if not already configured.
	if err := promptAPIKey(); err != nil {
		return fmt.Errorf("configuring API key: %w", err)
	}

	return nil
}

// promptAPIKey checks whether an API key is already available and, if not,
// prompts the user to enter one and saves it to the user config.
func promptAPIKey() error {
	if config.ResolveAPIKey() != "" {
		slog.Info("API key already configured")
		return nil
	}

	var apiKey string
	err := huh.NewInput().
		Title("Anthropic API key").
		Description("Required for plan + judge phases. Stored in ~/.config/vairdict/config.yaml").
		Value(&apiKey).
		Validate(func(s string) error {
			s = strings.TrimSpace(s)
			if s == "" {
				return fmt.Errorf("API key is required")
			}
			return nil
		}).
		Run()
	if err != nil {
		return fmt.Errorf("prompting for API key: %w", err)
	}

	apiKey = strings.TrimSpace(apiKey)

	return config.SaveUserConfig(&config.UserConfig{APIKey: apiKey})
}

// promptAnswers collects user responses from the interactive prompt.
type promptAnswers struct {
	ProjectName   string
	ProjectType   string
	RiskTolerance string
	NotifyChannel string
}

func prompt(detected DetectionResult, dir string) (promptAnswers, error) {
	var answers promptAnswers

	// Default project name from directory basename.
	answers.ProjectName = filepath.Base(dir)

	fields := []huh.Field{
		huh.NewInput().
			Title("Project name").
			Value(&answers.ProjectName),

		huh.NewSelect[string]().
			Title("Project type").
			Options(
				huh.NewOption("startup", "startup"),
				huh.NewOption("enterprise", "enterprise"),
				huh.NewOption("regulated", "regulated"),
				huh.NewOption("opensource", "opensource"),
			).
			Value(&answers.ProjectType),

		huh.NewSelect[string]().
			Title("Risk tolerance").
			Options(
				huh.NewOption("low", "low"),
				huh.NewOption("medium", "medium"),
				huh.NewOption("high", "high"),
			).
			Value(&answers.RiskTolerance),

		huh.NewInput().
			Title("Notification channel (leave empty for stdout)").
			Value(&answers.NotifyChannel),
	}

	// Show the user what we detected.
	if detected.Language != "" {
		slog.Info("detected language", "language", detected.Language)
	}
	if detected.Formatter != "" {
		slog.Info("detected formatter", "formatter", detected.Formatter)
	}
	if detected.Linter != "" {
		slog.Info("detected linter", "linter", detected.Linter)
	}

	form := huh.NewForm(huh.NewGroup(fields...))
	if err := form.Run(); err != nil {
		return answers, fmt.Errorf("running form: %w", err)
	}

	return answers, nil
}

func buildConfig(answers promptAnswers, detected DetectionResult) config.Config {
	cfg := DefaultsForType(answers.ProjectType)

	cfg.Project.Name = answers.ProjectName
	cfg.Project.Type = answers.ProjectType
	cfg.Project.RiskTolerance = answers.RiskTolerance

	// Apply detected conventions.
	if detected.Language != "" {
		cfg.Conventions.Language = detected.Language
	}
	if detected.Formatter != "" {
		cfg.Conventions.Formatter = detected.Formatter
	}
	if detected.Linter != "" {
		cfg.Conventions.Linter = detected.Linter
	}

	// Notification channel.
	if answers.NotifyChannel != "" {
		cfg.Escalation.NotifyVia = "slack"
		cfg.Escalation.Channel = answers.NotifyChannel
	}

	// Set language-specific build commands if we know the language.
	applyLanguageCommands(&cfg, detected.Language)

	return cfg
}

func applyLanguageCommands(cfg *config.Config, lang string) {
	switch lang {
	case "go":
		cfg.Commands.Build = "go build ./..."
		cfg.Commands.Test = "go test ./..."
		cfg.Commands.Lint = "golangci-lint run ./..."
	case "javascript":
		cfg.Commands.Build = "npm run build"
		cfg.Commands.Test = "npm test"
		cfg.Commands.Lint = "npm run lint"
	case "rust":
		cfg.Commands.Build = "cargo build"
		cfg.Commands.Test = "cargo test"
		cfg.Commands.Lint = "cargo clippy"
	case "python":
		cfg.Commands.Test = "pytest"
		cfg.Commands.Lint = "ruff check ."
	case "java":
		cfg.Commands.Build = "mvn compile"
		cfg.Commands.Test = "mvn test"
	case "ruby":
		cfg.Commands.Test = "bundle exec rspec"
		cfg.Commands.Lint = "bundle exec rubocop"
	}
}
