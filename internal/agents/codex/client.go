// Package codex implements a Completer that shells out to the local
// OpenAI Codex CLI (`codex exec --json`) instead of calling an Anthropic
// HTTP API.
//
// This is the first non-Anthropic completer for VAIrdict — it lets users
// with the Codex CLI installed run vairdict end-to-end without an
// Anthropic key, and proves the completer interface generalises across
// CLI families. Mirrors internal/agents/claudecli in shape so the
// follow-up registry work (#130) can register both behind a single
// resolver path.
//
// The Client is safe to share across goroutines. Each CompleteWithSystem
// call spawns a fresh `codex` subprocess and blocks until it finishes.
package codex

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

// NotInstalledError is returned when the `codex` binary cannot be found
// on PATH. Callers can use errors.As to surface a friendlier
// install-me message.
type NotInstalledError struct {
	Err error
}

func (e *NotInstalledError) Error() string {
	return fmt.Sprintf("codex CLI not installed: %v", e.Err)
}

func (e *NotInstalledError) Unwrap() error { return e.Err }

// ParseError is returned when the CLI produced output that could not be
// decoded into the envelope or the caller's target struct. Raw carries
// the original (truncated) stdout so operators can see what Codex
// actually said.
type ParseError struct {
	Raw string
	Err error
}

func (e *ParseError) Error() string {
	return fmt.Sprintf("codex cli parse error: %v (raw: %.200s)", e.Err, e.Raw)
}

func (e *ParseError) Unwrap() error { return e.Err }

// ExitError is returned when `codex` exits with a non-zero status.
// Stderr carries the truncated stderr output so callers don't see a
// bare "exit status 1".
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
// binary (typically `sh -c`).
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

// WithTimeout caps how long a single Codex CLI call may run. Defaults
// to 5 minutes to mirror the claudecli client.
func WithTimeout(d time.Duration) Option {
	return func(c *Client) { c.timeout = d }
}

// WithCommandFactory overrides how subprocesses are constructed. Tests
// use this to intercept exec without touching the real `codex` binary.
func WithCommandFactory(f CommandFactory) Option {
	return func(c *Client) { c.cmdFactory = f }
}

// WithExtraArgs appends additional flags before the prompt. Useful for
// `--dangerously-bypass-approvals-and-sandbox`, etc. The core flags
// (`exec`, `--json`) are always set.
func WithExtraArgs(args ...string) Option {
	return func(c *Client) { c.extraArgs = append(c.extraArgs, args...) }
}

// WithModel pins the Codex CLI subprocess to a specific model via the
// `--model` flag. Empty string is a no-op (the CLI picks its default).
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

// IsAvailable reports whether the `codex` binary is on PATH. Callers
// use this for auto-detect logic. Cheap and safe to call repeatedly.
func IsAvailable() bool {
	_, err := exec.LookPath("codex")
	return err == nil
}

// Model returns the model the client is pinned to via WithModel, or
// empty string when the client uses the CLI's default.
func (c *Client) Model() string { return c.model }

// Complete is a convenience wrapper for CompleteWithSystem with an
// empty system prompt.
func (c *Client) Complete(ctx context.Context, prompt string, target any) error {
	return c.CompleteWithSystem(ctx, "", prompt, target)
}

// CompleteWithSystem runs `codex exec --json` and unmarshals the
// assistant's JSON output into target. The Codex CLI has no separate
// system-prompt flag in non-interactive mode, so when system is set it
// is bundled into the prompt arg with a delimiter. Errors are typed:
// NotInstalledError when the binary is missing, ExitError on non-zero
// exit, ParseError for any JSON decode failure.
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string, target any) error {
	return c.runAndDecode(ctx, system, prompt, target)
}

// CompleteWithTool injects the tool's JSON Schema into the system
// prompt and reuses the prose-to-JSON parser. The Codex CLI does not
// expose native tool-use with a forced schema, so this falls back to
// the same approach as claudecli. On parse failure a single recovery
// call re-prompts Codex with its own raw output and asks for just the
// JSON.
func (c *Client) CompleteWithTool(ctx context.Context, system, prompt string, tool claude.Tool, target any) error {
	augmented := system
	if augmented != "" {
		augmented += "\n\n"
	}
	augmented += fmt.Sprintf(
		"## Response tool: %s\n%s\n\n"+
			"CRITICAL INSTRUCTION — OUTPUT FORMAT:\n"+
			"Respond with ONLY a single JSON object that conforms to this JSON Schema.\n"+
			"No prose before or after. No markdown fences. No explanation. Just the raw JSON object.\n\n"+
			"Schema:\n%s",
		tool.Name, tool.Description, string(tool.InputSchema),
	)

	err := c.runAndDecode(ctx, augmented, prompt, target)
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
	return c.runAndDecode(ctx, "", recoveryPrompt, target)
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

// envelope is the subset of Codex's `--json` result we care about.
// Mirrors claudecli's shape so the parser is identical.
type envelope struct {
	Type    string `json:"type"`
	Subtype string `json:"subtype"`
	IsError bool   `json:"is_error"`
	Result  string `json:"result"`
}

// runAndDecode executes the subprocess, decodes the envelope, and
// unmarshals the extracted result into target.
func (c *Client) runAndDecode(ctx context.Context, system, prompt string, target any) error {
	env, err := c.runOnce(ctx, system, prompt)
	if err != nil {
		return err
	}

	cleaned := extractJSON(env.Result)
	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		slog.Debug("codex cli parse failed",
			"err", err,
			"raw_len", len(env.Result),
			"cleaned_len", len(cleaned),
			"raw_head", truncate(env.Result, 500),
			"cleaned_head", truncate(cleaned, 500),
		)
		return &ParseError{Raw: env.Result, Err: fmt.Errorf("decoding result into target: %w", err)}
	}
	return nil
}

// runOnce executes a single CLI subprocess attempt. Returns the decoded
// envelope on success; typed errors on failure.
func (c *Client) runOnce(ctx context.Context, system, prompt string) (envelope, error) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	bundled := bundlePrompt(system, prompt)
	args := buildArgs(c.model, c.extraArgs, bundled)
	cmd := c.cmdFactory(ctx, "codex", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Debug("running codex cli", "args", args, "timeout", c.timeout)

	if err := cmd.Run(); err != nil {
		if execErr, ok := err.(*exec.Error); ok {
			return envelope{}, &NotInstalledError{Err: execErr}
		}
		if ctx.Err() != nil {
			return envelope{}, fmt.Errorf("codex cli cancelled: %w", ctx.Err())
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return envelope{}, &ExitError{
				ExitCode: exitErr.ExitCode(),
				Stderr:   stderr.String(),
				Err:      err,
			}
		}
		return envelope{}, fmt.Errorf("running codex cli: %w", err)
	}

	slog.Debug("codex cli completed", "duration", time.Since(start))

	var env envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return envelope{}, &ParseError{Raw: stdout.String(), Err: fmt.Errorf("decoding envelope: %w", err)}
	}
	if env.IsError {
		return envelope{}, &ParseError{Raw: env.Result, Err: fmt.Errorf("codex cli returned is_error=true (subtype=%s)", env.Subtype)}
	}
	if env.Result == "" {
		return envelope{}, &ParseError{Raw: stdout.String(), Err: fmt.Errorf("empty result field in envelope")}
	}

	return env, nil
}

// bundlePrompt produces the single positional prompt arg passed to
// `codex exec`. When system is empty the user prompt is returned
// verbatim; otherwise the two are joined with a labelled delimiter so
// the model can tell them apart. Codex's non-interactive mode has no
// separate system-prompt flag.
func bundlePrompt(system, prompt string) string {
	if system == "" {
		return prompt
	}
	return fmt.Sprintf("[system]\n%s\n\n[user]\n%s", system, prompt)
}

// buildArgs returns the argv after the binary name for one
// `codex exec --json` invocation. Pulled out as a free function so
// it's trivially unit-testable.
func buildArgs(model string, extra []string, prompt string) []string {
	args := make([]string, 0, 6+len(extra))
	args = append(args, "exec", "--json")
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, extra...)
	args = append(args, prompt)
	return args
}

// truncate returns the first n characters of s (never panics on short
// strings). Used purely for bounded debug logging of raw Codex output.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// extractJSON pulls a JSON object out of Codex's result string,
// tolerating prose preambles and markdown fences. Strategy: prefer
// string-aware brace matching from the first `{` to its balanced `}`,
// which handles bare objects, prose-wrapped objects, AND fenced
// objects in one pass. Fenced extraction is a last-resort fallback
// for non-object payloads.
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
	close := strings.Index(text[bodyStart:], fence)
	if close == -1 {
		return ""
	}
	return text[bodyStart : bodyStart+close]
}

// extractBraceObject scans for the first `{` and walks forward
// counting braces (respecting JSON string literals) until it finds the
// matching closing `}`. Returns the balanced slice, or empty string if
// no match.
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
