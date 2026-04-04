package claudecode

import (
	"bytes"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"time"

	"github.com/vairdict/vairdict/internal/state"
)

// NotInstalledError is returned when the claude CLI is not found.
type NotInstalledError struct {
	Err error
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("claude CLI not installed: %v", e.Err)
}

func (e *NotInstalledError) Unwrap() error { return e.Err }

// CommandFactory creates exec.Cmd instances. Injected for testing.
type CommandFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

// Runner executes prompts via the Claude Code CLI.
type Runner struct {
	timeout    time.Duration
	cmdFactory CommandFactory
}

// Option configures a Runner.
type Option func(*Runner)

// WithTimeout sets the maximum duration for a single CLI invocation.
func WithTimeout(d time.Duration) Option {
	return func(r *Runner) { r.timeout = d }
}

// WithCommandFactory injects a custom command factory (for testing).
func WithCommandFactory(f CommandFactory) Option {
	return func(r *Runner) { r.cmdFactory = f }
}

// New creates a Runner with the given options.
func New(opts ...Option) *Runner {
	r := &Runner{
		timeout:    10 * time.Minute,
		cmdFactory: exec.CommandContext,
	}
	for _, o := range opts {
		o(r)
	}
	return r
}

// Run executes a prompt via claude -p and returns the result.
// It satisfies the codephase.Coder interface via state.AgentResult.
func (r *Runner) Run(ctx context.Context, prompt string, workDir string) (state.AgentResult, error) {
	start := time.Now()

	// Apply timeout.
	ctx, cancel := context.WithTimeout(ctx, r.timeout)
	defer cancel()

	cmd := r.cmdFactory(ctx, "claude", "-p", prompt)
	cmd.Dir = workDir

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Debug("running claude code", "workDir", workDir, "timeout", r.timeout)

	err := cmd.Run()
	duration := time.Since(start)

	result := state.AgentResult{
		Output: stdout.String(),
	}

	if err != nil {
		// Check if claude is not installed.
		if execErr, ok := err.(*exec.Error); ok {
			return state.AgentResult{}, &NotInstalledError{Err: execErr}
		}

		// Context cancellation or timeout — check before exit error
		// because a killed process also produces an ExitError.
		if ctx.Err() != nil {
			return result, fmt.Errorf("claude code execution cancelled: %w", ctx.Err())
		}

		// Check for exit code.
		if exitErr, ok := err.(*exec.ExitError); ok {
			slog.Warn("claude code exited with error",
				"exitCode", exitErr.ExitCode(),
				"stderr", stderr.String(),
				"duration", duration,
			)
			return result, nil
		}

		return state.AgentResult{}, fmt.Errorf("running claude code: %w", err)
	}

	slog.Info("claude code completed", "duration", duration)
	return result, nil
}
