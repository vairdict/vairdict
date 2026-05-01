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
	"sync"
	"time"

	"github.com/vairdict/vairdict/internal/agents/claude"
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

// Client is a Completer backed by the Claude Code CLI. It holds the
// in-flight Claude session ID so calls 2+ within the same plan run
// can `--resume <id>` instead of re-sending the full system prompt
// and standards block on every loop. The session ID is mutex-guarded
// because Completer methods may be invoked from different goroutines
// in the orchestrator (each loop is sequential, but state.Store
// access is on a separate timeline).
type Client struct {
	timeout    time.Duration
	cmdFactory CommandFactory
	extraArgs  []string
	model      string

	mu        sync.Mutex
	sessionID string
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

// WithModel pins the Claude CLI subprocess to a specific model via the
// `--model` flag. Empty string is a no-op (the CLI picks its default).
// Use this when the judge model must be different from whatever the
// `claude` install would otherwise select.
func WithModel(model string) Option {
	return func(c *Client) { c.model = model }
}

// WithSessionID seeds the initial Claude session ID at construction.
// When set, the very first call switches from `-p` to
// `--resume <id> -p` and skips re-sending the system prompt — used by
// `vairdict resume` to reattach to the session a previous run
// established. Empty string (the default) means "start fresh."
func WithSessionID(id string) Option {
	return func(c *Client) { c.sessionID = id }
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

// IsAvailable reports whether the `claude` binary is on PATH. Callers use
// this for auto-detect logic before deciding between the CLI and the API
// client. It is cheap (a single LookPath call) and safe to call repeatedly.
func IsAvailable() bool {
	_, err := exec.LookPath("claude")
	return err == nil
}

// Model returns the model the client is pinned to via WithModel, or
// empty string when the client uses the CLI's default. Callers that
// stamp verdicts with the model that produced them read this.
func (c *Client) Model() string { return c.model }

// SessionID returns the most recent Claude session ID captured from
// the CLI envelope, or empty string when no session has been
// established yet. The orchestrator reads this after each phase to
// persist the session on state.Task so `vairdict resume` can
// reattach.
func (c *Client) SessionID() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.sessionID
}

// SetSessionID seeds (or clears) the session ID at runtime. Used on
// resume to restore the session a previous run established. Pass ""
// to force a fresh session on the next call.
func (c *Client) SetSessionID(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.sessionID = id
}

// Complete is a convenience wrapper for CompleteWithSystem with an empty
// system prompt. It exists so Client satisfies any narrow Completer-style
// interface that expects both methods.
func (c *Client) Complete(ctx context.Context, prompt string, target any) error {
	return c.CompleteWithSystem(ctx, "", prompt, target)
}

// CompleteWithTool is the CLI-path implementation of the tool-use API.
// The Claude Code CLI does not expose native tool-use with a forced schema,
// so this falls back to embedding the tool's JSON Schema into the system
// prompt and reusing the prose-to-JSON parser. Tools are disabled via
// --tools "" so the CLI produces a single-turn text response instead of
// spending turns on file reads. On parse failure a single recovery call
// re-prompts Claude with its own raw output and asks for just the JSON.
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

	err := c.completeWithOpts(ctx, augmented, prompt, true, target)
	if err == nil {
		return nil
	}

	// If parse failed, attempt a single recovery call: feed the raw output
	// back and ask Claude to extract/reformat it as valid JSON.
	parseErr, ok := err.(*ParseError)
	if !ok || parseErr.Raw == "" {
		return err
	}

	slog.Info("claude cli tool call failed, attempting recovery", "original_err", err)

	recoveryPrompt := fmt.Sprintf(
		"The following text was supposed to be a JSON object conforming to this schema:\n%s\n\n"+
			"But it was returned as prose. Extract the information and return ONLY the JSON object. "+
			"No markdown, no explanation, no fences — just the JSON.\n\n"+
			"Original text:\n%s",
		string(tool.InputSchema), truncate(parseErr.Raw, 4000),
	)
	return c.completeWithOpts(ctx, "", recoveryPrompt, true, target)
}

// CompleteWithTools falls back to single-turn CompleteWithTool using only the
// final tool. The CLI backend cannot do multi-turn tool use, so auxiliary tools
// like check_path are silently unavailable.
func (c *Client) CompleteWithTools(ctx context.Context, system, prompt string, tools []claude.Tool, finalTool string, _ map[string]claude.ToolHandler, target any) error {
	for _, t := range tools {
		if t.Name == finalTool {
			return c.CompleteWithTool(ctx, system, prompt, t, target)
		}
	}
	return fmt.Errorf("final tool %q not found in tools list", finalTool)
}

// envelope is the subset of Claude Code's `--output-format json` result
// that we care about. SessionID was previously discarded; #137 captures
// it so subsequent calls can `--resume <id>` instead of re-sending the
// full system prompt on every loop.
type envelope struct {
	Type      string `json:"type"`
	Subtype   string `json:"subtype"`
	IsError   bool   `json:"is_error"`
	Result    string `json:"result"`
	SessionID string `json:"session_id,omitempty"`
}

// completeWithOpts is the core CLI invocation. When noTools is true it
// passes --tools "" to disable all tool use, producing a single-turn text
// response. This is critical for judge calls that must return JSON directly
// rather than spending turns reading files.
func (c *Client) completeWithOpts(ctx context.Context, system, prompt string, noTools bool, target any) error {
	return c.runAndDecode(ctx, system, prompt, noTools, target)
}

// runAndDecode handles session-aware CLI invocation: builds args based
// on the current session ID, runs the subprocess, decodes the envelope,
// captures the new session_id, and unmarshals the result into target.
// On a stderr that looks like an expired/missing session it transparently
// clears the session ID and retries once with full args (start fresh).
func (c *Client) runAndDecode(ctx context.Context, system, prompt string, noTools bool, target any) error {
	env, err := c.runOnce(ctx, system, prompt, noTools)
	if err != nil {
		// If the failure looks like an expired session, drop the
		// stored ID and try once more from scratch. We log the
		// reason so an operator can see the resume cycle in
		// `vairdict logs`.
		if c.maybeClearExpiredSession(err) {
			slog.Info("claude cli session expired, retrying with fresh session", "err", err.Error())
			env, err = c.runOnce(ctx, system, prompt, noTools)
		}
		if err != nil {
			return err
		}
	}

	cleaned := extractJSON(env.Result)
	if err := json.Unmarshal([]byte(cleaned), target); err != nil {
		slog.Debug("claude cli parse failed",
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
// envelope on success; typed errors (NotInstalledError, ExitError,
// ParseError) on failure. Captures and stores the envelope's session_id
// so subsequent calls within this Client lifetime can --resume.
func (c *Client) runOnce(ctx context.Context, system, prompt string, noTools bool) (envelope, error) {
	start := time.Now()

	ctx, cancel := context.WithTimeout(ctx, c.timeout)
	defer cancel()

	c.mu.Lock()
	sessionID := c.sessionID
	c.mu.Unlock()

	args := buildArgs(sessionID, system, c.model, c.extraArgs, prompt, noTools)
	cmd := c.cmdFactory(ctx, "claude", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	slog.Debug("running claude cli",
		"args", args,
		"noTools", noTools,
		"timeout", c.timeout,
		"resumed", sessionID != "",
	)

	if err := cmd.Run(); err != nil {
		if execErr, ok := err.(*exec.Error); ok {
			return envelope{}, &NotInstalledError{Err: execErr}
		}
		if ctx.Err() != nil {
			return envelope{}, fmt.Errorf("claude cli cancelled: %w", ctx.Err())
		}
		if exitErr, ok := err.(*exec.ExitError); ok {
			return envelope{}, &ExitError{
				ExitCode: exitErr.ExitCode(),
				Stderr:   stderr.String(),
				Err:      err,
			}
		}
		return envelope{}, fmt.Errorf("running claude cli: %w", err)
	}

	slog.Debug("claude cli completed", "duration", time.Since(start), "noTools", noTools)

	var env envelope
	if err := json.Unmarshal(stdout.Bytes(), &env); err != nil {
		return envelope{}, &ParseError{Raw: stdout.String(), Err: fmt.Errorf("decoding envelope: %w", err)}
	}
	if env.IsError {
		return envelope{}, &ParseError{Raw: env.Result, Err: fmt.Errorf("claude cli returned is_error=true (subtype=%s)", env.Subtype)}
	}
	if env.Result == "" {
		return envelope{}, &ParseError{Raw: stdout.String(), Err: fmt.Errorf("empty result field in envelope")}
	}

	if env.SessionID != "" {
		c.mu.Lock()
		c.sessionID = env.SessionID
		c.mu.Unlock()
	}

	return env, nil
}

// buildArgs returns the argv for one `claude -p` invocation. When
// sessionID is non-empty the call uses `--resume <id>` and skips
// `--append-system-prompt` (the system prompt is already in the
// resumed session); otherwise it sends the full first-call args.
// Pulled out as a free function so it's trivially unit-testable.
func buildArgs(sessionID, system, model string, extra []string, prompt string, noTools bool) []string {
	args := make([]string, 0, 12)
	if sessionID != "" {
		args = append(args, "--resume", sessionID)
	}
	args = append(args, "-p", "--output-format", "json")
	if noTools {
		args = append(args, "--tools", "")
	}
	if sessionID == "" && system != "" {
		args = append(args, "--append-system-prompt", system)
	}
	if model != "" {
		args = append(args, "--model", model)
	}
	args = append(args, extra...)
	args = append(args, prompt)
	return args
}

// maybeClearExpiredSession inspects an error from a `claude` invocation
// and, if it looks like an expired/missing session, clears the stored
// session ID and returns true so the caller knows to retry. Returns
// false (and leaves state alone) when the error is unrelated to
// session lifecycle. We only consider clearing when a session was
// actually in use — otherwise there's nothing to clear.
func (c *Client) maybeClearExpiredSession(err error) bool {
	c.mu.Lock()
	hadSession := c.sessionID != ""
	c.mu.Unlock()
	if !hadSession {
		return false
	}

	exitErr, ok := err.(*ExitError)
	if !ok {
		return false
	}
	if !looksLikeSessionExpired(exitErr.Stderr) {
		return false
	}

	c.mu.Lock()
	c.sessionID = ""
	c.mu.Unlock()
	return true
}

// looksLikeSessionExpired matches the Claude CLI's stderr signatures
// for an unusable session id. Kept loose because the CLI's wording has
// drifted across versions; false positives just cause an extra fresh
// invocation, which is cheap.
func looksLikeSessionExpired(stderr string) bool {
	if stderr == "" {
		return false
	}
	low := strings.ToLower(stderr)
	if !strings.Contains(low, "session") {
		return false
	}
	for _, marker := range []string{"expired", "not found", "no such", "invalid", "does not exist"} {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// CompleteWithSystem runs `claude -p --output-format json` with the given
// system and user prompts and unmarshals the assistant's JSON output into
// target. Structure:
//
//  1. Spawn `claude` with args built from the options (including
//     `--resume <id>` when a prior session ID is on the Client).
//  2. Read the envelope and extract envelope.Result. Capture
//     envelope.SessionID so subsequent calls can resume.
//  3. Pass envelope.Result through extractJSON (strips markdown fences).
//  4. json.Unmarshal into target.
//
// Errors are typed: NotInstalledError when the binary is missing, ExitError
// when the process exits non-zero, ParseError for any JSON decode failure.
// ctx cancellation and the configured timeout are both honored via
// CommandContext. Tool use is left enabled (this is the planner path).
func (c *Client) CompleteWithSystem(ctx context.Context, system, prompt string, target any) error {
	return c.runAndDecode(ctx, system, prompt, false, target)
}

// truncate returns the first n characters of s (never panics on short
// strings). Used purely for bounded debug logging of raw Claude output.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// extractJSON pulls a JSON object out of Claude's result string, tolerating
// two common ways the CLI wraps structured output even when told not to:
//
//  1. A plain top-level { ... } object, possibly preceded by prose like
//     "Let me produce the plan." and possibly wrapped in a ```json fence.
//  2. A fenced markdown block (```json ... ``` or ``` ... ```) with no
//     extractable brace object (rare — content isn't JSON-shaped).
//
// Strategy: prefer string-aware brace matching from the first `{` to its
// balanced `}`. This handles bare objects, prose-wrapped objects, AND
// fenced objects in one pass — the leading ```json and trailing ``` sit
// outside the braces and are naturally ignored. Crucially, brace matching
// is immune to a subtle fence bug: if the JSON string values themselves
// contain an embedded ``` (e.g. an issue body that includes a code block),
// a fence-based extractor will treat that embedded fence as the closing
// fence and chop the JSON in half. Fenced extraction is kept only as a
// last-resort fallback for non-object payloads.
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
// fences, or empty string if no complete pair is found. A missing closing
// fence (truncated output) returns empty so callers fall back to other
// extraction strategies.
func extractFencedBlock(text string) string {
	const fence = "```"
	open := strings.Index(text, fence)
	if open == -1 {
		return ""
	}
	// Skip the opening fence and any language tag on the same line.
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

// extractBraceObject scans for the first `{` and walks forward counting
// braces (respecting JSON string literals) until it finds the matching
// closing `}`. Returns the balanced slice, or empty string if no match.
// This is the fallback when fenced extraction doesn't apply — e.g. Claude
// returned `Here is the plan: {...}` with no markdown at all.
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
