// Package codex implements a Completer that shells out to the local
// OpenAI Codex CLI (`codex exec`) instead of calling an Anthropic
// HTTP API.
//
// This is the first non-Anthropic completer for VAIrdict — it lets
// users with the Codex CLI installed run vairdict end-to-end without
// an Anthropic key, and proves the completer interface generalises
// across CLI families.
//
// Wire shape (verified against codex-rs/exec/src/cli.rs in
// github.com/openai/codex):
//
//	codex exec [--model <m>] [--output-schema <schemafile>] \
//	    --output-last-message <tmpfile> [<extras>...] <prompt>
//
// We deliberately do NOT pass `--json`. That flag emits a stream of
// JSONL telemetry events (one per state change) — useful for
// observability, not for getting back a single structured result.
// Instead `--output-last-message` writes only the agent's final
// message to a file, which is exactly the shape the planner and
// judges consume.
//
// Tool use uses native enforcement via `--output-schema <file>`: the
// tool's InputSchema is written to a tempfile and codex constrains
// the model to a JSON response conforming to it. Falls back to a
// single recovery round-trip if the model returns prose anyway.
//
// System prompt handling is deliberately simple: the system text is
// prepended to the prompt arg with a blank-line separator. Codex's
// non-interactive mode has no `--system` flag; the proper alternative
// (`-c base_instructions=<toml-escaped>`) needs verification against a
// real binary before we commit to it. Tracking that as a follow-up.
//
// The Client is safe to share across goroutines. Each
// CompleteWithSystem call spawns a fresh `codex` subprocess and blocks
// until it finishes.
package codex

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/vairdict/vairdict/internal/agents/claude"
)

// NotInstalledError is returned when the `codex` binary cannot be
// found on PATH. Callers can use errors.As to surface a friendlier
// install-me message.
type NotInstalledError struct {
	Err error
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("codex CLI not installed: %v", e.Err)
}

func (e *NotInstalledError) Unwrap() error { return e.Err }

// ParseError is returned when codex's output file was missing, empty,
// or could not be decoded into the caller's target struct. Raw
// carries the original (truncated) file content so operators can see
// what codex actually said.
type ParseError struct {
	Raw string
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("codex cli parse error: %v (raw: %.200s)", e.Err, e.Raw)
}

func (e *ParseError) Unwrap() error { return e.Err }

// ExitError is returned when `codex` exits with a non-zero status.
type ExitError struct {
	ExitCode int
	Stderr   string
	Err      error
}

func (e *ExitError) Error() string {
	stderr := strings.TrimRight(e.Stderr, "\n")
	if stderr == "" {
		return fmt.Sprintf("codex cli exited with status %d", e.ExitCode)
	}
	return fmt.Sprintf("codex cli exited with status %d: %s", e.ExitCode, stderr)
}

func (e *ExitError) Unwrap() error { return e.Err }

// CommandFactory constructs exec.Cmd instances. The production default
// is exec.CommandContext; tests inject a factory that returns a fake
// binary.
type CommandFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

// Client is a Completer backed by the Codex CLI.
type Client struct {
	timeout    time.Duration
	cmdFactory CommandFactory
	extraArgs  []string
	model      string
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout caps how long a single Codex CLI call may run.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithCommandFactory overrides how subprocesses are constructed. Tests
// use this to intercept exec without touching the real `codex` binary.
func WithCommandFactory(f CommandFactory) Option {
	return func(c *Client) { c.cmdFactory = f }
}

// WithExtraArgs appends additional flags before the prompt. Useful for
// `--dangerously-bypass-approvals-and-sandbox`,
// `--skip-git-repo-check`, etc. The core flags (`exec`,
// `--output-last-message`) are always set.
func WithExtraArgs(args ...string) Option {
	return func(c *Client) { c.extraArgs = append(c.extraArgs, args...) }
}

// WithModel pins the Codex CLI subprocess to a specific model via the
// `--model` flag (`SharedCliOptions.model` upstream). Empty string is a
// no-op.
func WithModel(model string) Option {
	return func(c *Client) { c.model = model }
}

// New constructs a Client with the given options.
func New(opts ...Option) *Client {
	c := &Client{
		timeout:    5 * time.Minute,
		cmdFactory: exec.CommandContext,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// IsAvailable reports whether the `codex` binary is on PATH.
func IsAvailable() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// Model returns the model the client is pinned to via WithModel.
func (c *Client) Model() string { return c.model }

// Complete is a convenience wrapper for CompleteWithSystem with an
// empty system prompt.
func (c *Client) Complete(ctx context.Context, prompt string, target any) error {
	return c.CompleteWithSystem(ctx, "", prompt, target)
}

// CompleteWithSystem runs `codex exec` and unmarshals the agent's
// final message into target. The system prompt is bundled into the
// prompt arg; codex non-interactive mode has no separate
// system-prompt flag.
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string, target any) error {
	return c.run(ctx, system, prompt, "", target)
}

// CompleteWithTool runs `codex exec --output-schema <schemafile>` so
// codex enforces the agent's final message conforms to the tool's
// InputSchema. On parse failure the client makes a single recovery
// call asking the model to reformat the original prose as JSON.
func (c *Client) CompleteWithTool(ctx context.Context, system, prompt string, tool claude.Tool, target any) error {
	schemaPath, cleanup, err := writeTempFile("codex-schema-*.json", tool.InputSchema)
	if err != nil {
		return fmt.Errorf("writing schema tempfile: %w", err)
	}
	defer cleanup()

	err = c.run(ctx, system, prompt, schemaPath, target)
	if err == nil {
		return nil
	}

	parseErr, ok := err.(*ParseError)
	if !ok || parseErr.Raw == "" {
		return err
	}

	slog.Info("codex cli tool call failed, attempting recovery", "original_err", err)

	recoveryPrompt := fmt.Sprintf(
		"The following text was supposed to be a JSON object conforming to this schema:\n%s\n\n"+
			"But it was returned as prose. Extract the information and return ONLY the JSON object. "+
			"No markdown, no explanation, no fences — just the JSON.\n\n"+
			"Original text:\n%s",
		string(tool.InputSchema), truncate(parseErr.Raw, 4000),
	)
	return c.run(ctx, "", recoveryPrompt, schemaPath, target)
}

// CompleteWithTools falls back to single-turn CompleteWithTool using
// only the final tool. The CLI backend cannot do multi-turn tool use,
// so auxiliary tools like check_path are silently unavailable.
func (c *Client) CompleteWithTools(ctx context.Context, system, prompt string, tools []claude.Tool, finalTool string, _ map[string]claude.ToolHandler, target any) error {
	for _, t := range tools {
		if t.Name == finalTool {
			return c.CompleteWithTool(ctx, system, prompt, t, target)
		}
	}
	return fmt.Errorf("final tool %q not found in tools list", finalTool)
}

// run is the core invocation path. Creates an output tempfile, runs
// codex, reads the tempfile, extracts JSON from the model's final
// message, and unmarshals into target. schemaPath is optional — when
// non-empty it's passed via --output-schema for native shape
// enforcement.
func (c *Client) run(ctx context.Context, system, prompt, schemaPath string, target any) error {
	outputPath, cleanup, err := writeTempFile("codex-output-*.txt", nil)
	if err != nil {
		return fmt.Errorf("creating output tempfile: %w", err)
	}
	defer cleanup()

	if err := c.runOnce(ctx, system, prompt, outputPath, schemaPath); err != nil {
		return err
	}

	raw, err := os.ReadFile(outputPath)
	if err != nil {
		return &ParseError{Raw: "", Err: fmt.Errorf("reading output file: %w", err)}
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		return &ParseError{Raw: string(raw), Err: fmt.Errorf("codex wrote empty output file")}
	}

	cleaned := extractJSON(string(raw))
	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		slog.Debug("codex cli parse failed",
			"err", err,
			"raw_len", len(raw),
			"cleaned_len", len(cleaned),
			"raw_head", truncate(string(raw), 500),
			"cleaned_head", truncate(cleaned, 500),
		)
		return &ParseError{Raw: string(raw), Err: fmt.Errorf("decoding output into target: %w", err)}
	}
	return nil
}

// runOnce executes a single codex subprocess. Returns typed errors
// (NotInstalledError, ExitError) on failure. Side effect on success:
// codex writes the agent's final message to outputPath.
func (c *Client) runOnce(ctx context.Context, system, prompt, outputPath, schemaPath string) error {
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	bundled := bundlePrompt(system, prompt)
	args := buildArgs(c.model, outputPath, schemaPath, c.extraArgs, bundled)
	cmd := c.cmdFactory(ctx, "codex", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Debug("running codex cli", "args", args, "timeout", c.timeout)

	if err := cmd.Run(); err != nil {
		if execErr, ok := err.(*exec.Error); ok {
			return &NotInstalledError{Err: execErr}
		}
		if ctx.Err() != nil {
			return fmt.Errorf("codex cli cancelled: %w", ctx.Err())
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return &ExitError{
				ExitCode: exitErr.ExitCode(),
				Stderr:   stderr.String(),
				Err:      err,
			}
		}
		return fmt.Errorf("running codex cli: %w", err)
	}

	slog.Debug("codex cli completed", "duration", time.Since(start))
	return nil
}

// bundlePrompt prepends the system prompt to the user prompt with a
// blank-line separator. No role markers — codex has no convention for
// parsing them, and inventing `[system]/[user]` would be cargo-culting
// from chat formats the model wasn't trained to honor.
func bundlePrompt(system, prompt string) string {
	if system == "" {
		return prompt
	}
	return system + "\n\n" + prompt
}

// buildArgs returns the argv after the binary name for one
// `codex exec` invocation. Pulled out as a free function so it's
// trivially unit-testable.
//
// outputPath is required. schemaPath is optional ("" = omit
// --output-schema). model is optional ("" = omit --model). extras and
// prompt are appended last so the prompt always lands as the final
// positional arg.
func buildArgs(model, outputPath, schemaPath string, extra []string, prompt string) []string {
	args := make([]string, 0, 8+len(extra))
	args = append(args, "exec", "--output-last-message", outputPath)
	if model != "" {
		args = append(args, "--model", model)
	}
	if schemaPath != "" {
		args = append(args, "--output-schema", schemaPath)
	}
	args = append(args, extra...)
	args = append(args, prompt)
	return args
}

// writeTempFile creates a temp file with the given pattern, writes
// content to it (when non-nil), closes it, and returns its path plus a
// cleanup func. Cleanup is safe to call multiple times.
func writeTempFile(pattern string, content []byte) (string, func(), error) {
	f, err := os.CreateTemp("", pattern)
	if err != nil {
		return "", func() {}, err
	}
	path := f.Name()
	if content != nil {
		if _, err := f.Write(content); err != nil {
			f.Close()
			os.Remove(path)
			return "", func() {}, err
		}
	}
	if err := f.Close(); err != nil {
		os.Remove(path)
		return "", func() {}, err
	}
	return path, func() { os.Remove(path) }, nil
}

// truncate returns the first n characters of s.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// extractJSON pulls a JSON object out of codex's output text,
// tolerating prose preambles and markdown fences. Even with
// --output-schema active, models occasionally still wrap their final
// response in ```json fences; this lets us recover instead of
// failing parse.
func extractJSON(text string) string {
	if obj := extractBraceObject(text); obj != "" {
		return obj
	}
	if fenced := extractFencedBlock(text); fenced != "" {
		return fenced
	}
	return strings.TrimSpace(text)
}

// extractFencedBlock returns the content between the first pair of ```
// fences, or empty string if no complete pair is found.
func extractFencedBlock(text string) string {
	const fence = "```"
	open := strings.Index(text, fence)
	if open == -1 {
		return ""
	}
	bodyStart := open + len(fence)
	if nl := strings.IndexByte(text[bodyStart:], '\n'); nl != -1 {
		bodyStart += nl + 1
	}
	end := strings.Index(text[bodyStart:], fence)
	if end == -1 {
		return ""
	}
	return text[bodyStart : bodyStart+end]
}

// extractBraceObject scans for the first `{` and walks forward
// counting braces (respecting JSON string literals) until it finds the
// matching closing `}`.
func extractBraceObject(text string) string {
	start := strings.IndexByte(text, '{')
	if start == -1 {
		return ""
	}
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		c := text[i]
		if escaped {
			escaped = false
			continue
		}
		if c == '\\' {
			escaped = true
			continue
		}
		if c == '"' {
			inString = !inString
			continue
		}
		if inString {
			continue
		}
		switch c {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return text[start : i+1]
			}
		}
	}
	return ""
}
