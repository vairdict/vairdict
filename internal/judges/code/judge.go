package code

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// CommandExecutor runs a command and returns combined output and error.
// Injected for testing.
type CommandExecutor interface {
	Run(ctx context.Context, workDir string, name string, args ...string) ([]byte, error)
}

// ExecExecutor is the real implementation using os/exec.
type ExecExecutor struct{}

// Run executes a command in the given directory.
func (e *ExecExecutor) Run(ctx context.Context, workDir string, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir = workDir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// CodeJudge evaluates code by running the project's configured quality commands.
type CodeJudge struct {
	executor CommandExecutor
	cfg      config.Config
}

// New creates a CodeJudge with the given executor and config.
func New(executor CommandExecutor, cfg config.Config) *CodeJudge {
	return &CodeJudge{executor: executor, cfg: cfg}
}

// check represents a single quality check result.
type check struct {
	Name     string
	Command  string
	Passed   bool
	Output   string
	Severity state.Severity
}

// Judge runs the configured quality commands and returns a Verdict.
func (j *CodeJudge) Judge(ctx context.Context, workDir string) (*state.Verdict, error) {
	checks := j.buildChecks()

	for i, c := range checks {
		if c.Command == "" {
			// No command configured — skip and count as passed.
			checks[i].Passed = true
			continue
		}

		slog.Debug("running quality check", "name", c.Name, "command", c.Command)

		parts := strings.Fields(c.Command)
		output, err := j.executor.Run(ctx, workDir, parts[0], parts[1:]...)
		outStr := string(output)

		if err != nil {
			checks[i].Passed = false
			checks[i].Output = outStr
			slog.Info("quality check failed", "name", c.Name, "output", truncate(outStr, 200))
		} else {
			checks[i].Passed = true
			slog.Debug("quality check passed", "name", c.Name)
		}
	}

	// Build verdict.
	var gaps []state.Gap
	passed := 0
	total := 0
	for _, c := range checks {
		if c.Command == "" {
			continue
		}
		total++
		if c.Passed {
			passed++
			continue
		}
		gaps = append(gaps, state.Gap{
			Severity:    c.Severity,
			Description: fmt.Sprintf("%s failed: %s", c.Name, truncate(strings.TrimSpace(c.Output), 500)),
			Blocking:    c.Severity == state.SeverityP0 || c.Severity == state.SeverityP1,
		})
	}

	score := 0.0
	if total > 0 {
		score = float64(passed) / float64(total) * 100
	}

	verdict := &state.Verdict{
		Score: score,
		Pass:  passed == total,
		Gaps:  gaps,
	}

	slog.Info("code judge verdict", "score", score, "pass", verdict.Pass, "gaps", len(gaps))
	return verdict, nil
}

// buildChecks creates the check list from config commands.
// Severity: build=P0, test=P1, lint/format=P2.
func (j *CodeJudge) buildChecks() []check {
	return []check{
		{Name: "format", Command: j.formatCommand(), Severity: state.SeverityP2},
		{Name: "lint", Command: j.cfg.Commands.Lint, Severity: state.SeverityP2},
		{Name: "test", Command: j.cfg.Commands.Test, Severity: state.SeverityP1},
		{Name: "build", Command: j.cfg.Commands.Build, Severity: state.SeverityP0},
	}
}

// formatCommand returns the format check command based on conventions.
func (j *CodeJudge) formatCommand() string {
	switch j.cfg.Conventions.Formatter {
	case "gofmt":
		return "gofmt -l ."
	case "prettier":
		return "npx prettier --check ."
	case "black":
		return "black --check ."
	case "rustfmt":
		return "rustfmt --check"
	default:
		return ""
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
}
