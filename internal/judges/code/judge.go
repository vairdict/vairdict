package code

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"

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

// CodeJudge evaluates code by running spm exec ship.
type CodeJudge struct {
	executor CommandExecutor
}

// New creates a CodeJudge with the given executor.
func New(executor CommandExecutor) *CodeJudge {
	return &CodeJudge{executor: executor}
}

// check represents a single quality check result.
type check struct {
	Name     string
	Passed   bool
	Output   string
	Severity state.Severity
}

// Judge runs spm exec ship and returns a Verdict.
func (j *CodeJudge) Judge(ctx context.Context, workDir string) (*state.Verdict, error) {
	// Verify spm is installed.
	if _, err := j.executor.Run(ctx, workDir, "spm", "--version"); err != nil {
		return nil, fmt.Errorf("spm not installed or not in PATH: %w", err)
	}

	// Run spm exec ship.
	output, err := j.executor.Run(ctx, workDir, "spm", "exec", "ship")
	outStr := string(output)

	slog.Debug("spm exec ship output", "output", outStr)

	checks := parseShipOutput(outStr, err)

	// Build verdict.
	var gaps []state.Gap
	passed := 0
	for _, c := range checks {
		if c.Passed {
			passed++
			continue
		}
		gaps = append(gaps, state.Gap{
			Severity:    c.Severity,
			Description: fmt.Sprintf("%s failed: %s", c.Name, strings.TrimSpace(c.Output)),
			Blocking:    c.Severity == state.SeverityP0 || c.Severity == state.SeverityP1,
		})
	}

	total := len(checks)
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

// parseShipOutput extracts check results from spm exec ship output.
func parseShipOutput(output string, execErr error) []check {
	checks := []check{
		{Name: "format", Passed: true, Severity: state.SeverityP2},
		{Name: "lint", Passed: true, Severity: state.SeverityP2},
		{Name: "test", Passed: true, Severity: state.SeverityP1},
		{Name: "build", Passed: true, Severity: state.SeverityP0},
	}

	// If ship failed, parse which checks failed from output.
	if execErr != nil {
		lower := strings.ToLower(output)
		for i := range checks {
			// Look for failure indicators per check.
			name := checks[i].Name
			if strings.Contains(lower, name+" failed") ||
				strings.Contains(lower, name+": fail") ||
				strings.Contains(lower, name+" error") ||
				(strings.Contains(lower, "x "+name) || strings.Contains(lower, "✗ "+name)) {
				checks[i].Passed = false
				checks[i].Output = extractSection(output, name)
			}
		}

		// If nothing specific matched but we had an error, mark build as failed.
		allPassed := true
		for _, c := range checks {
			if !c.Passed {
				allPassed = false
				break
			}
		}
		if allPassed {
			// Generic failure — assume build failed.
			checks[3].Passed = false
			checks[3].Output = output
		}
	}

	return checks
}

// extractSection tries to find output related to a specific check name.
func extractSection(output string, name string) string {
	lines := strings.Split(output, "\n")
	var section []string
	capturing := false
	for _, line := range lines {
		lower := strings.ToLower(line)
		if strings.Contains(lower, name) {
			capturing = true
		}
		if capturing {
			section = append(section, line)
			if len(section) > 10 {
				break
			}
		}
	}
	if len(section) > 0 {
		return strings.Join(section, "\n")
	}
	return output
}
