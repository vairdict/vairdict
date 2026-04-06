// Package claudecli implements a Completer that shells out to the local
// Claude Code CLI (`claude -p`) instead of calling the Anthropic HTTP API.
//
// This lets contributors run vairdict end-to-end using the Claude subscription
// session attached to their `claude` install — no API key, no separate billing,
// no extra rate limit to manage. CI environments without an interactive login
// still use the HTTP client in internal/agents/claude.
//
// The Client is safe to share across goroutines. Each CompleteWithSystem call
// spawns a fresh `claude` subprocess and blocks until it finishes.
package claudecli

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"
)

// NotInstalledError is returned when the `claude` binary cannot be found on
// PATH. Callers can use errors.As to surface a friendlier install-me message.
type NotInstalledError struct {
	Err error
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("claude CLI not installed: %v", e.Err)
}

func (e *NotInstalledError) Unwrap() error { return e.Err }

// ParseError is returned when the CLI produced output that could not be
// decoded into the envelope or the caller's target struct. Raw carries the
// original (truncated) stdout so operators can see what Claude actually said.
type ParseError struct {
	Raw string
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("claude cli parse error: %v (raw: %.200s)", e.Err, e.Raw)
}

func (e *ParseError) Unwrap() error { return e.Err }

// ExitError is returned when `claude` exits with a non-zero status. Stderr
// carries the truncated stderr output so callers don't see a bare
// "exit status 1".
type ExitError struct {
	ExitCode int
	Stderr   string
	Err      error
}

func (e *ExitError) Error() string {
	stderr := strings.TrimRight(e.Stderr, "\n")
	if stderr == "" {
		return fmt.Sprintf("claude cli exited with status %d", e.ExitCode)
	}
	return fmt.Sprintf("claude cli exited with status %d: %s", e.ExitCode, stderr)
}

func (e *ExitError) Unwrap() error { return e.Err }

// CommandFactory constructs exec.Cmd instances. The production default is
// exec.CommandContext; tests inject a factory that returns a fake binary
// (typically `sh -c` or a TestHelperProcess-style re-invocation).
type CommandFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

// Client is a Completer backed by the Claude Code CLI. It holds no state
// between calls; options only affect argument construction and the exec seam.
type Client struct {
	timeout    time.Duration
	cmdFactory CommandFactory
	extraArgs  []string
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout caps how long a single Claude CLI call may run. Defaults to
// 120s to mirror the API client's http timeout.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithCommandFactory overrides how subprocesses are constructed. Tests use
// this to intercept exec without touching the real `claude` binary.
func WithCommandFactory(f CommandFactory) Option {
	return func(c *Client) { c.cmdFactory = f }
}

// WithExtraArgs appends additional flags before the prompt. Useful for
// `--model`, `--dangerously-skip-permissions`, etc. The core flags (`-p`,
// `--output-format json`, `--append-system-prompt`) are always set.
func WithExtraArgs(args ...string) Option {
	return func(c *Client) { c.extraArgs = append(c.extraArgs, args...) }
}

// New constructs a Client with the given options.
func New(opts ...Option) *Client {
	c := &Client{
		timeout:    120 * time.Second,
		cmdFactory: exec.CommandContext,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// IsAvailable reports whether the `claude` binary is on PATH. Callers use
// this for auto-detect logic before deciding between the CLI and the API
// client. It is cheap (a single LookPath call) and safe to call repeatedly.
func IsAvailable() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// Complete is a convenience wrapper for CompleteWithSystem with an empty
// system prompt. It exists so Client satisfies any narrow Completer-style
// interface that expects both methods.
func (c *Client) Complete(ctx context.Context, prompt string, target any) error {
	return c.CompleteWithSystem(ctx, "", prompt, target)
}

// envelope is the subset of Claude Code's `--output-format json` result that
// we care about. We explicitly decode only the fields we need so the rest
// (session_id, cost, usage, …) are tolerated as extras.
type envelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// CompleteWithSystem runs `claude -p --output-format json` with the given
// system and user prompts and unmarshals the assistant's JSON output into
// target. Structure:
//
//  1. Spawn `claude` with args built from the options.
//  2. Read the envelope and extract envelope.Result.
//  3. Pass envelope.Result through extractJSON (strips markdown fences).
//  4. json.Unmarshal into target.
//
// Errors are typed: NotInstalledError when the binary is missing, ExitError
// when the process exits non-zero, ParseError for any JSON decode failure.
// ctx cancellation and the configured timeout are both honored via
// CommandContext.
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string, target any) error {
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	args := []string{"-p", "--output-format", "json"}
	if system != "" {
		args = append(args, "--append-system-prompt", system)
	}
	args = append(args, c.extraArgs...)
	args = append(args, prompt)

	cmd := c.cmdFactory(ctx, "claude", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Debug("running claude cli", "args", args, "timeout", c.timeout)

	if err := cmd.Run(); err != nil {
		// Binary not found: exec.Error wraps the LookPath failure.
		if execErr, ok := err.(*exec.Error); ok {
			return &NotInstalledError{Err: execErr}
		}
		// ctx.Err() check must come before ExitError: a killed process
		// also surfaces as an ExitError but the root cause is the
		// cancellation / timeout.
		if ctx.Err() != nil {
			return fmt.Errorf("claude cli cancelled: %w", ctx.Err())
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &ExitError{
				ExitCode: exitErr.ExitCode(),
				Stderr:   stderr.String(),
				Err:      err,
			}
		}
		return fmt.Errorf("running claude cli: %w", err)
	}

	slog.Debug("claude cli completed", "duration", time.Since(start))

	var env envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return &ParseError{Raw: stdout.String(), Err: fmt.Errorf("decoding envelope: %w", err)}
	}
	if env.IsError {
		return &ParseError{Raw: env.Result, Err: fmt.Errorf("claude cli returned is_error=true (subtype=%s)", env.Subtype)}
	}
	if env.Result == "" {
		return &ParseError{Raw: stdout.String(), Err: fmt.Errorf("empty result field in envelope")}
	}

	cleaned := extractJSON(env.Result)
	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		return &ParseError{Raw: env.Result, Err: fmt.Errorf("decoding result into target: %w", err)}
	}
	return nil
}

// extractJSON strips a leading ```json / ``` code fence if present and
// returns the content between the fences. Claude sometimes wraps structured
// output in markdown even when the system prompt says not to; this mirrors
// the tolerance built into internal/agents/claude.parseResponse.
func extractJSON(text string) string {
	const fence = "```"
	start := -1
	for i := 0; i <= len(text)-len(fence); i++ {
		if text[i:i+len(fence)] == fence {
			if start == -1 {
				for j := i + len(fence); j < len(text); j++ {
					if text[j] == '\n' {
						start = j + 1
						break
					}
				}
			} else {
				return text[start:i]
			}
		}
	}
	return strings.TrimSpace(text)
}
