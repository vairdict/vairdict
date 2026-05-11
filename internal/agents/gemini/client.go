// Package gemini implements a Completer that shells out to Google's
// Gemini CLI (`gemini -p ...`) instead of calling an Anthropic HTTP
// API.
//
// This is the third completer family for VAIrdict — joining
// claude-{cli,api} and codex. Lets users with the Gemini CLI installed
// run vairdict end-to-end without Anthropic or OpenAI credentials.
//
// Wire shape (verified against github.com/google-gemini/gemini-cli
// headless mode docs):
//
//	gemini --output-format json [-m <model>] [<extras>...] -p <prompt>
//
// Output is a single JSON object on stdout (the "envelope"):
//
//	{"response": "<final agent text>", "stats": {...}, "error": null}
//
// We use `--output-format json` and NOT `--output-format stream-json`:
// the streaming form emits JSONL telemetry events, not a single
// structured result. Same reasoning as why codex avoids `--json`.
//
// Tool use: Gemini's CLI currently has no native `--output-schema`
// flag (tracked at google-gemini/gemini-cli#5021). The tool's
// InputSchema is therefore injected into the system prompt, and a
// single recovery round-trip mirrors codex when the model returns
// prose anyway.
//
// The Client is safe to share across goroutines. Each
// CompleteWithSystem call spawns a fresh `gemini` subprocess and
// blocks until it finishes.
package gemini

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os/exec"
	"strings"
	"time"

	"github.com/vairdict/vairdict/internal/agents/claude"
)

// NotInstalledError is returned when the `gemini` binary cannot be
// found on PATH. Callers can use errors.As to surface a friendlier
// install-me message.
type NotInstalledError struct {
	Err error
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("gemini CLI not installed: %v", e.Err)
}

func (e *NotInstalledError) Unwrap() error { return e.Err }

// ParseError is returned when gemini's stdout could not be decoded
// into the envelope, the envelope reported an error, or the response
// text could not be decoded into the caller's target. Raw carries the
// original (possibly truncated) content so operators can see what
// gemini actually said.
type ParseError struct {
	Raw string
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("gemini cli parse error: %v (raw: %.200s)", e.Err, e.Raw)
}

func (e *ParseError) Unwrap() error { return e.Err }

// ExitError is returned when `gemini` exits with a non-zero status.
type ExitError struct {
	ExitCode int
	Stderr   string
	Err      error
}

func (e *ExitError) Error() string {
	stderr := strings.TrimRight(e.Stderr, "\n")
	if stderr == "" {
		return fmt.Sprintf("gemini cli exited with status %d", e.ExitCode)
	}
	return fmt.Sprintf("gemini cli exited with status %d: %s", e.ExitCode, stderr)
}

func (e *ExitError) Unwrap() error { return e.Err }

// CommandFactory constructs exec.Cmd instances. The production default
// is exec.CommandContext; tests inject a factory that returns a fake
// binary.
type CommandFactory func(ctx context.Context, name string, args ...string) *exec.Cmd

// Client is a Completer backed by the Gemini CLI.
type Client struct {
	timeout    time.Duration
	cmdFactory CommandFactory
	extraArgs  []string
	model      string
}

// Option configures a Client.
type Option func(*Client)

// WithTimeout caps how long a single Gemini CLI call may run.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithCommandFactory overrides how subprocesses are constructed. Tests
// use this to intercept exec without touching the real `gemini` binary.
func WithCommandFactory(f CommandFactory) Option {
	return func(c *Client) { c.cmdFactory = f }
}

// WithExtraArgs appends additional flags after the core flags and
// before `-p <prompt>`. Useful for `--yolo`, sandbox toggles, etc.
// Core flags (`--output-format json`, `-p`) are always set.
func WithExtraArgs(args ...string) Option {
	return func(c *Client) { c.extraArgs = append(c.extraArgs, args...) }
}

// WithModel pins the Gemini CLI subprocess to a specific model via
// the `-m` flag. Empty string is a no-op.
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

// IsAvailable reports whether the `gemini` binary is on PATH.
func IsAvailable() bool {
	_, err := exec.LookPath("gemini")
	return err == nil
}

// Model returns the model the client is pinned to via WithModel.
func (c *Client) Model() string { return c.model }

// Complete is a convenience wrapper for CompleteWithSystem with an
// empty system prompt.
func (c *Client) Complete(ctx context.Context, prompt string, target any) error {
	return c.CompleteWithSystem(ctx, "", prompt, target)
}

// CompleteWithSystem runs `gemini` and unmarshals the envelope's
// `.response` field into target. The system prompt is bundled into
// the `-p` arg; gemini-cli's non-interactive mode has no separate
// system-prompt flag.
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string, target any) error {
	return c.run(ctx, system, prompt, target)
}

// CompleteWithTool injects the tool's InputSchema into the system
// prompt (gemini-cli has no native --output-schema; tracked at
// google-gemini/gemini-cli#5021) and unmarshals the response into
// target. On parse failure the client makes a single recovery call
// asking the model to reformat the original prose as JSON.
func (c *Client) CompleteWithTool(ctx context.Context, system, prompt string, tool claude.Tool, target any) error {
	systemWithSchema := bundleSchema(system, tool)

	err := c.run(ctx, systemWithSchema, prompt, target)
	if err == nil {
		return nil
	}

	parseErr, ok := err.(*ParseError)
	if !ok || parseErr.Raw == "" {
		return err
	}

	slog.Info("gemini cli tool call failed, attempting recovery", "original_err", err)

	recoveryPrompt := fmt.Sprintf(
		"The following text was supposed to be a JSON object conforming to this schema:\n%s\n\n"+
			"But it was returned as prose. Extract the information and return ONLY the JSON object. "+
			"No markdown, no explanation, no fences — just the JSON.\n\n"+
			"Original text:\n%s",
		string(tool.InputSchema), truncate(parseErr.Raw, 4000),
	)
	return c.run(ctx, "", recoveryPrompt, target)
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

// run is the core invocation path. Spawns gemini, reads stdout,
// decodes the envelope, extracts JSON from the .response field, and
// unmarshals into target.
func (c *Client) run(ctx context.Context, system, prompt string, target any) error {
	stdout, err := c.runOnce(ctx, system, prompt)
	if err != nil {
		return err
	}

	if len(bytes.TrimSpace(stdout)) == 0 {
		return &ParseError{Raw: "", Err: fmt.Errorf("gemini wrote empty stdout")}
	}

	var envelope struct {
		Response string          `json:"response"`
		Error    json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(stdout, &envelope); err != nil {
		return &ParseError{Raw: string(stdout), Err: fmt.Errorf("decoding envelope: %w", err)}
	}

	if hasEnvelopeError(envelope.Error) {
		return &ParseError{
			Raw: string(stdout),
			Err: fmt.Errorf("gemini reported error: %s", string(envelope.Error)),
		}
	}

	if envelope.Response == "" {
		return &ParseError{Raw: string(stdout), Err: fmt.Errorf("envelope has empty .response")}
	}

	cleaned := extractJSON(envelope.Response)
	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		slog.Debug("gemini cli parse failed",
			"err", err,
			"raw_len", len(envelope.Response),
			"cleaned_len", len(cleaned),
			"raw_head", truncate(envelope.Response, 500),
			"cleaned_head", truncate(cleaned, 500),
		)
		return &ParseError{Raw: envelope.Response, Err: fmt.Errorf("decoding response into target: %w", err)}
	}
	return nil
}

// runOnce executes a single gemini subprocess. Returns typed errors
// (NotInstalledError, ExitError) on failure. Returns the captured
// stdout on success.
func (c *Client) runOnce(ctx context.Context, system, prompt string) ([]byte, error) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	bundled := bundlePrompt(system, prompt)
	args := buildArgs(c.model, c.extraArgs, bundled)
	cmd := c.cmdFactory(ctx, "gemini", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Debug("running gemini cli", "args", args, "timeout", c.timeout)

	if err := cmd.Run(); err != nil {
		if execErr, ok := err.(*exec.Error); ok {
			return nil, &NotInstalledError{Err: execErr}
		}
		if ctx.Err() != nil {
			return nil, fmt.Errorf("gemini cli cancelled: %w", ctx.Err())
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return nil, &ExitError{
				ExitCode: exitErr.ExitCode(),
				Stderr:   stderr.String(),
				Err:      err,
			}
		}
		return nil, fmt.Errorf("running gemini cli: %w", err)
	}

	slog.Debug("gemini cli completed", "duration", time.Since(start))
	return stdout.Bytes(), nil
}

// hasEnvelopeError reports whether the envelope's `.error` field is
// present and non-null. The field is JSON-typed (could be null or a
// string), so a raw-message check is more robust than coercing to a
// concrete type.
func hasEnvelopeError(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 {
		return false
	}
	return !bytes.Equal(trimmed, []byte("null"))
}

// bundlePrompt prepends the system prompt to the user prompt with a
// blank-line separator. No role markers — gemini has no convention for
// parsing them, and inventing `[system]/[user]` would be cargo-culting
// from chat formats the model wasn't trained to honor.
func bundlePrompt(system, prompt string) string {
	if system == "" {
		return prompt
	}
	return system + "\n\n" + prompt
}

// bundleSchema appends a "respond with JSON conforming to this schema"
// instruction (carrying the tool's InputSchema verbatim) to the
// caller's system prompt. Used by CompleteWithTool because gemini-cli
// has no native schema-enforcement flag.
func bundleSchema(system string, tool claude.Tool) string {
	schemaBlock := fmt.Sprintf(
		"Respond with a single JSON object conforming to this schema:\n%s\n"+
			"Return ONLY the JSON object — no markdown fences, no prose, no commentary.",
		string(tool.InputSchema),
	)
	if system == "" {
		return schemaBlock
	}
	return system + "\n\n" + schemaBlock
}

// buildArgs returns the argv after the binary name for one `gemini`
// invocation. Pulled out as a free function so it's trivially
// unit-testable.
//
// model is optional ("" omits -m). extras and prompt are appended
// last so the prompt always lands as the final positional value
// (after `-p`).
func buildArgs(model string, extra []string, prompt string) []string {
	args := make([]string, 0, 6+len(extra))
	args = append(args, "--output-format", "json")
	if model != "" {
		args = append(args, "-m", model)
	}
	args = append(args, extra...)
	args = append(args, "-p", prompt)
	return args
}

// truncate returns the first n characters of s.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// extractJSON pulls a JSON object out of gemini's `.response` text,
// tolerating prose preambles and markdown fences. The CLI has no
// native schema enforcement (tracked at google-gemini/gemini-cli#5021)
// so models commonly wrap output in ```json fences or ramble before
// emitting the object.
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
