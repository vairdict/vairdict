package gemini

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/vairdict/vairdict/internal/agents/claude"
)

// --- Test helpers ---

// fakeCmd builds an exec.Cmd that runs `sh -c script` via the given ctx
// so each test can script stdout/stderr/exit-code precisely without
// touching the real gemini binary.
func fakeCmd(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", script)
}

// flagValue returns the value following the named flag in args, or ""
// if the flag is not present or has no value following it.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// envelopeOut wraps the given response text in the JSON envelope
// gemini-cli emits under `--output-format json`:
//
//	{"response": "<text>", "stats": {...}, "error": null}
//
// Used by writingFactory to build realistic stdout.
func envelopeOut(response string) string {
	out, _ := json.Marshal(map[string]any{
		"response": response,
		"stats":    map[string]any{"session": map[string]any{"duration": 1}},
		"error":    nil,
	})
	return string(out)
}

// stdoutCmd returns a sh -c command that prints content to stdout,
// stderr to stderr, and exits with the given code. Used by the fake
// command factory to simulate the gemini binary's output.
func stdoutCmd(ctx context.Context, stdout, stderr string, exit int) *exec.Cmd {
	script := `printf '%s' "$FAKE_STDOUT"; if [ -n "$FAKE_STDERR" ]; then printf '%s' "$FAKE_STDERR" >&2; fi; exit ${FAKE_EXIT:-0}`
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"FAKE_STDOUT="+stdout,
		"FAKE_STDERR="+stderr,
		"FAKE_EXIT="+itoa(exit),
	)
	return cmd
}

// writingFactory returns a CommandFactory that captures argv into
// captured (if non-nil) and emits an envelope wrapping responseText on
// stdout.
func writingFactory(responseText string, captured *[][]string) CommandFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if captured != nil {
			*captured = append(*captured, append([]string{name}, args...))
		}
		return stdoutCmd(ctx, envelopeOut(responseText), "", 0)
	}
}

// rawFactory returns a CommandFactory that captures argv and emits
// rawStdout verbatim (no envelope wrapping) — used to test malformed
// envelope handling.
func rawFactory(rawStdout string, captured *[][]string) CommandFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if captured != nil {
			*captured = append(*captured, append([]string{name}, args...))
		}
		return stdoutCmd(ctx, rawStdout, "", 0)
	}
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	digits := []byte{}
	neg := false
	if n < 0 {
		neg = true
		n = -n
	}
	for n > 0 {
		digits = append([]byte{byte('0' + n%10)}, digits...)
		n /= 10
	}
	if neg {
		return "-" + string(digits)
	}
	return string(digits)
}

// --- Core CompleteWithSystem behaviour ---

func TestCompleteWithSystem_HappyPath(t *testing.T) {
	c := New(WithCommandFactory(writingFactory(`{"answer":42,"label":"ok"}`, nil)))

	var got struct {
		Answer int    `json:"answer"`
		Label  string `json:"label"`
	}
	if err := c.CompleteWithSystem(context.Background(), "sys", "hi", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Answer != 42 || got.Label != "ok" {
		t.Errorf("got %+v, want {42 ok}", got)
	}
}

func TestCompleteWithSystem_FencedResult(t *testing.T) {
	// gemini-cli has no native schema enforcement, so models commonly
	// wrap output in ```json … ``` fences. extractJSON must tolerate.
	c := New(WithCommandFactory(writingFactory("```json\n{\"x\":1}\n```", nil)))

	var got struct {
		X int `json:"x"`
	}
	if err := c.Complete(context.Background(), "hi", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.X != 1 {
		t.Errorf("got %+v, want {1}", got)
	}
}

func TestCompleteWithSystem_EmptyStdout(t *testing.T) {
	c := New(WithCommandFactory(rawFactory("", nil)))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
}

func TestCompleteWithSystem_EnvelopeNotJSON(t *testing.T) {
	// stdout is non-JSON garbage — envelope decode fails. Raw is
	// preserved so operators can see what gemini said.
	c := New(WithCommandFactory(rawFactory("oops this is not json at all", nil)))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if !strings.Contains(parseErr.Raw, "oops") {
		t.Errorf("raw should preserve stdout, got %q", parseErr.Raw)
	}
}

func TestCompleteWithSystem_EnvelopeError(t *testing.T) {
	// envelope's `.error` field is non-null — surface as ParseError
	// carrying gemini's error message so operators can see it.
	raw := `{"response":"","error":"rate limited","stats":{}}`
	c := New(WithCommandFactory(rawFactory(raw, nil)))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if !strings.Contains(parseErr.Error(), "rate limited") {
		t.Errorf("error message should include gemini's error, got %q", parseErr.Error())
	}
}

func TestCompleteWithSystem_EnvelopeEmptyResponse(t *testing.T) {
	raw := `{"response":"","error":null,"stats":{}}`
	c := New(WithCommandFactory(rawFactory(raw, nil)))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError for empty .response, got %T: %v", err, err)
	}
}

func TestCompleteWithSystem_TargetDecodeFailure(t *testing.T) {
	// .response is a JSON string but not an object — decoding into a
	// struct target should surface as ParseError with the response
	// text preserved as Raw.
	c := New(WithCommandFactory(writingFactory(`"a string"`, nil)))

	var got struct{ X int }
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if !strings.Contains(parseErr.Raw, "a string") {
		t.Errorf("expected raw to preserve response text, got %q", parseErr.Raw)
	}
}

func TestCompleteWithSystem_NonZeroExit(t *testing.T) {
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return fakeCmd(ctx, "echo 'boom' >&2; exit 2")
	}))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var exitErr *ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("expected ExitError, got %T: %v", err, err)
	}
	if exitErr.ExitCode != 2 {
		t.Errorf("exit code = %d, want 2", exitErr.ExitCode)
	}
	if !strings.Contains(exitErr.Stderr, "boom") {
		t.Errorf("stderr = %q, want to contain 'boom'", exitErr.Stderr)
	}
}

func TestCompleteWithSystem_NotInstalled(t *testing.T) {
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "nonexistent-binary-xyz-12345")
	}))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var ni *NotInstalledError
	if !errors.As(err, &ni) {
		t.Fatalf("expected NotInstalledError, got %T: %v", err, err)
	}
}

func TestCompleteWithSystem_ContextCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return fakeCmd(ctx, "exec sleep 1")
	}))

	var got map[string]any
	err := c.CompleteWithSystem(ctx, "", "hi", &got)
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !strings.Contains(err.Error(), "cancelled") && !strings.Contains(err.Error(), "context") {
		t.Errorf("expected cancellation error, got %v", err)
	}
}

func TestCompleteWithSystem_Timeout(t *testing.T) {
	c := New(
		WithTimeout(50*time.Millisecond),
		WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return fakeCmd(ctx, "exec sleep 5")
		}),
	)

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- Argv shape (verified against google-gemini/gemini-cli headless mode) ---

func TestCompleteWithSystem_ArgvShape(t *testing.T) {
	// Production argv shape against the real gemini binary:
	//   gemini --output-format json [-m M] [extras...] -p <prompt>
	// We deliberately do NOT use --output-format stream-json: that
	// emits JSONL telemetry events not a single result, mirroring why
	// codex avoids --json.
	var captured [][]string
	c := New(
		WithExtraArgs("--yolo"),
		WithCommandFactory(writingFactory(`{}`, &captured)),
	)

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "you are a judge", "do the thing", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(captured) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(captured))
	}
	run := captured[0]
	if run[0] != "gemini" {
		t.Errorf("binary = %q, want gemini", run[0])
	}
	joined := strings.Join(run, " ")
	if !strings.Contains(joined, "--output-format json") {
		t.Errorf("argv must contain --output-format json: %s", joined)
	}
	if strings.Contains(joined, "stream-json") {
		t.Errorf("argv must NOT contain stream-json (it streams JSONL events, not structured output): %s", joined)
	}
	if !strings.Contains(joined, "--yolo") {
		t.Errorf("argv missing extra arg: %s", joined)
	}
	// Prompt is passed via -p; the prompt text is the last arg and
	// contains both system + user prompt (bundled, no markers).
	promptVal := flagValue(run[1:], "-p")
	if promptVal == "" {
		t.Errorf("-p has no value: %v", run)
	}
	if !strings.Contains(promptVal, "you are a judge") {
		t.Errorf("-p arg %q must contain system prompt", promptVal)
	}
	if !strings.Contains(promptVal, "do the thing") {
		t.Errorf("-p arg %q must contain user prompt", promptVal)
	}
}

func TestCompleteWithSystem_WithModelAddsFlag(t *testing.T) {
	var captured [][]string
	c := New(
		WithModel("gemini-2.5-pro"),
		WithCommandFactory(writingFactory(`{}`, &captured)),
	)

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Model() != "gemini-2.5-pro" {
		t.Errorf("client Model() = %q, want gemini-2.5-pro", c.Model())
	}
	joined := strings.Join(captured[0], " ")
	if !strings.Contains(joined, "-m gemini-2.5-pro") {
		t.Errorf("captured args missing -m gemini-2.5-pro: %v", captured[0])
	}
}

func TestCompleteWithSystem_NoModelSkipsFlag(t *testing.T) {
	var captured [][]string
	c := New(WithCommandFactory(writingFactory(`{}`, &captured)))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range captured[0] {
		if a == "-m" {
			t.Errorf("client without WithModel must not add -m flag: %v", captured[0])
		}
	}
}

func TestCompleteWithSystem_NoSystemPromptKeepsPromptClean(t *testing.T) {
	// When system is empty, the -p arg is the user prompt verbatim.
	var captured [][]string
	c := New(WithCommandFactory(writingFactory(`{}`, &captured)))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "just the prompt", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	promptVal := flagValue(captured[0][1:], "-p")
	if promptVal != "just the prompt" {
		t.Errorf("expected -p value to equal user prompt verbatim when system empty, got %q", promptVal)
	}
}

// --- Tool use (schema injected into system prompt — no native flag) ---

func TestCompleteWithTool_SchemaInjectedIntoPrompt(t *testing.T) {
	// CompleteWithTool must (a) inject the tool's InputSchema into the
	// system prompt (no native --output-schema in gemini-cli — tracked
	// at google-gemini/gemini-cli#5021) and (b) parse the .response
	// field into the target.
	var captured [][]string

	schemaJSON := `{"type":"object","properties":{"verdict":{"type":"string"},"score":{"type":"number"}},"required":["verdict","score"]}`
	tool := claude.Tool{
		Name:        "verdict",
		Description: "judge verdict",
		InputSchema: json.RawMessage(schemaJSON),
	}

	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append(captured, append([]string{name}, args...))
		return stdoutCmd(ctx, envelopeOut(`{"verdict":"pass","score":0.9}`), "", 0)
	}))

	var got struct {
		Verdict string  `json:"verdict"`
		Score   float64 `json:"score"`
	}
	if err := c.CompleteWithTool(context.Background(), "you are a judge", "judge it", tool, &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != "pass" || got.Score != 0.9 {
		t.Errorf("got %+v, want {pass 0.9}", got)
	}
	promptVal := flagValue(captured[0][1:], "-p")
	if !strings.Contains(promptVal, schemaJSON) {
		t.Errorf("-p value must contain InputSchema verbatim, got %q", promptVal)
	}
	if !strings.Contains(promptVal, "you are a judge") {
		t.Errorf("-p value must still contain caller's system prompt, got %q", promptVal)
	}
}

func TestCompleteWithTool_RecoveryOnDecodeFailure(t *testing.T) {
	// First call returns prose (model ignored the schema-in-prompt
	// guidance, which can happen). Client must retry once with a
	// recovery prompt and parse the second response.
	tool := claude.Tool{
		Name:        "answer",
		Description: "give answer",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`),
	}

	calls := 0
	var capturedPrompts []string
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		calls++
		capturedPrompts = append(capturedPrompts, flagValue(args, "-p"))
		switch calls {
		case 1:
			return stdoutCmd(ctx, envelopeOut("sure thing! the answer is x=7"), "", 0)
		default:
			return stdoutCmd(ctx, envelopeOut(`{"x":7}`), "", 0)
		}
	}))

	var got struct {
		X int `json:"x"`
	}
	if err := c.CompleteWithTool(context.Background(), "", "what is x?", tool, &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.X != 7 {
		t.Errorf("got %+v, want {7}", got)
	}
	if calls != 2 {
		t.Fatalf("expected 2 subprocess calls (initial + recovery), got %d", calls)
	}
	if !strings.Contains(capturedPrompts[1], "x=7") {
		t.Errorf("recovery prompt should include the original prose, got %q", capturedPrompts[1])
	}
}

func TestCompleteWithTools_RoutesToFinalTool(t *testing.T) {
	c := New(WithCommandFactory(writingFactory(`{"verdict":"pass"}`, nil)))

	tools := []claude.Tool{
		{
			Name:        "check_path",
			Description: "auxiliary",
			InputSchema: json.RawMessage(`{"type":"object"}`),
		},
		{
			Name:        "verdict",
			Description: "final",
			InputSchema: json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string"}}}`),
		},
	}

	var got struct {
		Verdict string `json:"verdict"`
	}
	if err := c.CompleteWithTools(context.Background(), "", "p", tools, "verdict", nil, &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got.Verdict != "pass" {
		t.Errorf("got %+v, want {pass}", got)
	}
}

func TestCompleteWithTools_UnknownFinalToolErrors(t *testing.T) {
	c := New(WithCommandFactory(writingFactory(`{}`, nil)))

	tools := []claude.Tool{{Name: "verdict", InputSchema: json.RawMessage(`{}`)}}

	var got map[string]any
	err := c.CompleteWithTools(context.Background(), "", "p", tools, "missing_tool", nil, &got)
	if err == nil {
		t.Fatalf("expected error for unknown final tool")
	}
	if !strings.Contains(err.Error(), "missing_tool") {
		t.Errorf("error should name the missing tool, got %v", err)
	}
}

// --- Compile-time interface check ---

func TestClient_HasCompleterShape(t *testing.T) {
	// Compile-time guard mirroring the cmd/vairdict completer
	// interface. If the interface drifts and this package isn't
	// updated, the build here breaks immediately rather than failing
	// later at the wiring site.
	type completerLike interface {
		CompleteWithSystem(ctx context.Context, system, prompt string, target any) error
		CompleteWithTool(ctx context.Context, system, prompt string, tool claude.Tool, target any) error
		CompleteWithTools(ctx context.Context, system, prompt string, tools []claude.Tool, finalTool string, handlers map[string]claude.ToolHandler, target any) error
		Model() string
	}
	var _ completerLike = (*Client)(nil)
}

// --- Availability + helpers ---

func TestIsAvailable_SmokeTest(t *testing.T) {
	_ = IsAvailable()
}

func TestExtractJSON(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"bare", `{"x":1}`, `{"x":1}`},
		{"fenced_json", "```json\n{\"x\":1}\n```", `{"x":1}`},
		{"fenced_plain", "```\n{\"x\":2}\n```", `{"x":2}`},
		{"padded", "   {\"x\":3}   ", `{"x":3}`},
		{
			"preamble_then_fence",
			"Here is the answer.\n\n```json\n{\"x\":4}\n```",
			`{"x":4}`,
		},
		{
			"prose_then_bare_object",
			"Here is the result: {\"x\":6} done.",
			`{"x":6}`,
		},
		{
			"string_with_braces",
			`{"msg":"a{b}c","n":7}`,
			`{"msg":"a{b}c","n":7}`,
		},
		{
			"escaped_quote_in_string",
			`{"msg":"he said \"hi\"","n":8}`,
			`{"msg":"he said \"hi\"","n":8}`,
		},
		{
			"nested_object",
			`prefix {"a":{"b":{"c":9}}} suffix`,
			`{"a":{"b":{"c":9}}}`,
		},
		{
			// Brace matching ignores braces and fences inside string
			// literals — same regression coverage as codex/claudecli.
			"fenced_with_embedded_fence_in_string",
			"```json\n{\"requirements\":\"see ```html\\nfoo\\n``` for an example\",\"n\":10}\n```",
			"{\"requirements\":\"see ```html\\nfoo\\n``` for an example\",\"n\":10}",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := extractJSON(tc.in)
			if got != tc.want {
				t.Errorf("extractJSON(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestBuildArgs_Shape(t *testing.T) {
	t.Run("base_no_model_no_extra", func(t *testing.T) {
		args := buildArgs("", nil, "user prompt")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--output-format json") {
			t.Errorf("missing --output-format json: %s", joined)
		}
		if strings.Contains(joined, "stream-json") {
			t.Errorf("must NOT include stream-json: %s", joined)
		}
		if strings.Contains(joined, "-m") {
			t.Errorf("must not include -m when model empty: %s", joined)
		}
		// Prompt is the last arg, via -p.
		if args[len(args)-2] != "-p" {
			t.Errorf("second-to-last arg must be -p, got %q (args=%v)", args[len(args)-2], args)
		}
		if args[len(args)-1] != "user prompt" {
			t.Errorf("prompt must be last arg, got %q (args=%v)", args[len(args)-1], args)
		}
	})

	t.Run("with_model_and_extra", func(t *testing.T) {
		args := buildArgs("gemini-2.5-pro", []string{"--yolo"}, "p")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-m gemini-2.5-pro") {
			t.Errorf("missing -m gemini-2.5-pro: %s", joined)
		}
		if !strings.Contains(joined, "--yolo") {
			t.Errorf("missing extra arg: %s", joined)
		}
		if args[len(args)-1] != "p" {
			t.Errorf("prompt must be last arg, got %q (args=%v)", args[len(args)-1], args)
		}
	})
}

func TestBundlePrompt(t *testing.T) {
	if got := bundlePrompt("", "user"); got != "user" {
		t.Errorf("empty system: got %q, want %q", got, "user")
	}
	got := bundlePrompt("sys", "user")
	if !strings.Contains(got, "sys") || !strings.Contains(got, "user") {
		t.Errorf("bundle missing parts: %q", got)
	}
	if strings.Contains(got, "[system]") || strings.Contains(got, "[user]") {
		t.Errorf("bundlePrompt must not invent role markers: %q", got)
	}
}

func TestBundleSchema(t *testing.T) {
	tool := claude.Tool{
		Name:        "x",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"y":{"type":"integer"}}}`),
	}
	// Empty system: schema-only output.
	got := bundleSchema("", tool)
	if !strings.Contains(got, `"type":"object"`) {
		t.Errorf("bundleSchema must inline the InputSchema, got %q", got)
	}
	// With system: caller's system prompt is preserved alongside schema.
	got = bundleSchema("you are a judge", tool)
	if !strings.Contains(got, "you are a judge") {
		t.Errorf("bundleSchema must preserve caller system prompt, got %q", got)
	}
	if !strings.Contains(got, `"type":"object"`) {
		t.Errorf("bundleSchema must inline the InputSchema, got %q", got)
	}
}
