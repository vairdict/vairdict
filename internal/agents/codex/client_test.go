package codex

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
// touching the real codex binary.
func fakeCmd(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", script)
}

// flagValue returns the value following the named flag in args, or "" if
// the flag is not present or has no value following it.
func flagValue(args []string, flag string) string {
	for i, a := range args {
		if a == flag && i+1 < len(args) {
			return args[i+1]
		}
	}
	return ""
}

// writeAndExitCmd returns a sh -c command that writes content to path
// (when path is non-empty) and exits with the given code. Used by the
// fake command factory to simulate codex writing the final agent
// message to the file passed via --output-last-message.
func writeAndExitCmd(ctx context.Context, path, content, stderr string, exit int) *exec.Cmd {
	script := `if [ -n "$FAKE_PATH" ]; then printf '%s' "$FAKE_CONTENT" > "$FAKE_PATH"; fi; if [ -n "$FAKE_STDERR" ]; then printf '%s' "$FAKE_STDERR" >&2; fi; exit ${FAKE_EXIT:-0}`
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"FAKE_CONTENT="+content,
		"FAKE_PATH="+path,
		"FAKE_STDERR="+stderr,
		"FAKE_EXIT="+itoa(exit),
	)
	return cmd
}

// writingFactory returns a CommandFactory that captures argv into
// captured (if non-nil) and writes content to whatever path follows
// --output-last-message in the args.
func writingFactory(content string, captured *[][]string) CommandFactory {
	return func(ctx context.Context, name string, args ...string) *exec.Cmd {
		if captured != nil {
			*captured = append(*captured, append([]string{name}, args...))
		}
		path := flagValue(args, "--output-last-message")
		return writeAndExitCmd(ctx, path, content, "", 0)
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
	// Even with --output-schema available, models sometimes still wrap
	// output in ```json … ``` fences. extractJSON tolerates that.
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

func TestCompleteWithSystem_OutputFileMissing(t *testing.T) {
	// Process exits 0 but never writes the output file. Real failure
	// mode if codex crashed silently or was killed mid-flight.
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		// Empty FAKE_PATH means the script does not write the file,
		// even though the actual --output-last-message path was real.
		return exec.CommandContext(ctx, "sh", "-c", "exit 0")
	}))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
}

func TestCompleteWithSystem_OutputFileEmpty(t *testing.T) {
	c := New(WithCommandFactory(writingFactory("", nil)))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
}

func TestCompleteWithSystem_TargetDecodeFailure(t *testing.T) {
	// Output is valid JSON but not an object — decoding into a struct
	// target should surface as ParseError with the raw content
	// preserved.
	c := New(WithCommandFactory(writingFactory(`"a string"`, nil)))

	var got struct{ X int }
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if !strings.Contains(parseErr.Raw, "a string") {
		t.Errorf("expected raw to preserve output, got %q", parseErr.Raw)
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

// --- Argv shape (verified against codex-rs/exec/src/cli.rs) ---

func TestCompleteWithSystem_ArgvShape(t *testing.T) {
	// Production argv shape against the real codex-exec binary:
	//   codex exec --output-last-message <tmpfile> [--model M] [extras...] <prompt>
	// Critically, --json is NOT used: that flag emits streaming JSONL
	// telemetry events, not a single structured result. The structured
	// final response comes from --output-last-message reading.
	var captured [][]string
	c := New(
		WithExtraArgs("--dangerously-bypass-approvals-and-sandbox"),
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
	if run[0] != "codex" {
		t.Errorf("binary = %q, want codex", run[0])
	}
	if run[1] != "exec" {
		t.Errorf("first arg must be subcommand `exec`, got %q", run[1])
	}
	joined := strings.Join(run, " ")
	if strings.Contains(joined, "--json") {
		t.Errorf("argv must NOT contain --json (it streams JSONL telemetry, not structured output): %s", joined)
	}
	if !strings.Contains(joined, "--output-last-message") {
		t.Errorf("argv must contain --output-last-message: %s", joined)
	}
	if !strings.Contains(joined, "--dangerously-bypass-approvals-and-sandbox") {
		t.Errorf("argv missing extra arg: %s", joined)
	}
	// Output file path must be a real path string after the flag.
	outPath := flagValue(run[1:], "--output-last-message")
	if outPath == "" {
		t.Errorf("--output-last-message has no value: %v", run)
	}
	// Prompt is the last arg and contains both system + user prompt
	// (bundled, no markers — codex exec has no separate
	// system-prompt flag in non-interactive mode).
	last := run[len(run)-1]
	if !strings.Contains(last, "you are a judge") {
		t.Errorf("prompt arg %q must contain system prompt", last)
	}
	if !strings.Contains(last, "do the thing") {
		t.Errorf("prompt arg %q must contain user prompt", last)
	}
}

func TestCompleteWithSystem_TempFilesCleanedUp(t *testing.T) {
	// The output file the client passes to codex must be removed after
	// the call returns. Otherwise long-running judge loops leak temp
	// files into /tmp.
	var captured [][]string
	c := New(WithCommandFactory(writingFactory(`{}`, &captured)))

	var got map[string]any
	if err := c.Complete(context.Background(), "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(captured) != 1 {
		t.Fatalf("expected 1 invocation, got %d", len(captured))
	}
	outPath := flagValue(captured[0][1:], "--output-last-message")
	if outPath == "" {
		t.Fatal("--output-last-message path missing")
	}
	if _, err := os.Stat(outPath); !os.IsNotExist(err) {
		t.Errorf("output tempfile %q must be cleaned up after call (stat err = %v)", outPath, err)
	}
}

func TestCompleteWithSystem_WithModelAddsFlag(t *testing.T) {
	var captured [][]string
	c := New(
		WithModel("gpt-5-codex"),
		WithCommandFactory(writingFactory(`{}`, &captured)),
	)

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Model() != "gpt-5-codex" {
		t.Errorf("client Model() = %q, want gpt-5-codex", c.Model())
	}
	joined := strings.Join(captured[0], " ")
	if !strings.Contains(joined, "--model gpt-5-codex") {
		t.Errorf("captured args missing --model gpt-5-codex: %v", captured[0])
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
		if a == "--model" {
			t.Errorf("client without WithModel must not add --model flag: %v", captured[0])
		}
	}
}

func TestCompleteWithSystem_NoSystemPromptKeepsPromptClean(t *testing.T) {
	// When system is empty, the prompt arg is the user prompt verbatim.
	var captured [][]string
	c := New(WithCommandFactory(writingFactory(`{}`, &captured)))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "just the prompt", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if captured[0][len(captured[0])-1] != "just the prompt" {
		t.Errorf("expected last arg to equal user prompt verbatim when system empty, got %q", captured[0][len(captured[0])-1])
	}
}

// --- Tool use (uses --output-schema for native structured output) ---

func TestCompleteWithTool_PassesOutputSchemaFile(t *testing.T) {
	// CompleteWithTool must (a) write the tool's InputSchema to a
	// tempfile, (b) pass it via --output-schema, and (c) parse the
	// agent's final message file into the target. The schema file
	// content must match the InputSchema verbatim — that's how codex
	// enforces the response shape.
	var captured [][]string
	var capturedSchemaContent string

	schemaJSON := `{"type":"object","properties":{"verdict":{"type":"string"},"score":{"type":"number"}},"required":["verdict","score"]}`
	tool := claude.Tool{
		Name:        "verdict",
		Description: "judge verdict",
		InputSchema: json.RawMessage(schemaJSON),
	}

	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append(captured, append([]string{name}, args...))
		// Capture schema file content before codex would consume it.
		schemaPath := flagValue(args, "--output-schema")
		if schemaPath != "" {
			b, err := os.ReadFile(schemaPath)
			if err == nil {
				capturedSchemaContent = string(b)
			}
		}
		path := flagValue(args, "--output-last-message")
		return writeAndExitCmd(ctx, path, `{"verdict":"pass","score":0.9}`, "", 0)
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
	if !strings.Contains(strings.Join(captured[0], " "), "--output-schema") {
		t.Errorf("argv must include --output-schema for tool use: %v", captured[0])
	}
	if capturedSchemaContent != schemaJSON {
		t.Errorf("schema file content mismatch:\n got: %s\nwant: %s", capturedSchemaContent, schemaJSON)
	}
}

func TestCompleteWithTool_SchemaFileCleanedUp(t *testing.T) {
	tool := claude.Tool{
		Name:        "x",
		InputSchema: json.RawMessage(`{"type":"object"}`),
	}

	var capturedSchemaPath string
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedSchemaPath = flagValue(args, "--output-schema")
		path := flagValue(args, "--output-last-message")
		return writeAndExitCmd(ctx, path, `{}`, "", 0)
	}))

	var got map[string]any
	if err := c.CompleteWithTool(context.Background(), "", "p", tool, &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if capturedSchemaPath == "" {
		t.Fatal("schema path was not captured")
	}
	if _, err := os.Stat(capturedSchemaPath); !os.IsNotExist(err) {
		t.Errorf("schema tempfile %q must be cleaned up after call (stat err = %v)", capturedSchemaPath, err)
	}
}

func TestCompleteWithTool_RecoveryOnDecodeFailure(t *testing.T) {
	// First call writes prose to the output file (model ignored the
	// schema, which can happen). Client must retry once with a
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
		capturedPrompts = append(capturedPrompts, args[len(args)-1])
		path := flagValue(args, "--output-last-message")
		switch calls {
		case 1:
			return writeAndExitCmd(ctx, path, "sure thing! the answer is x=7", "", 0)
		default:
			return writeAndExitCmd(ctx, path, `{"x":7}`, "", 0)
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
			// literals — same regression coverage as claudecli.
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
	t.Run("base_no_model_no_schema_no_extra", func(t *testing.T) {
		args := buildArgs("", "/tmp/out", "", nil, "user prompt")
		if args[0] != "exec" {
			t.Errorf("first arg must be subcommand exec, got %q", args[0])
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--output-last-message /tmp/out") {
			t.Errorf("missing --output-last-message: %s", joined)
		}
		if strings.Contains(joined, "--json") {
			t.Errorf("must NOT include --json: %s", joined)
		}
		if strings.Contains(joined, "--model") {
			t.Errorf("must not include --model when empty: %s", joined)
		}
		if strings.Contains(joined, "--output-schema") {
			t.Errorf("must not include --output-schema when empty: %s", joined)
		}
		if args[len(args)-1] != "user prompt" {
			t.Errorf("prompt must be last: %v", args)
		}
	})

	t.Run("with_model_schema_and_extra", func(t *testing.T) {
		args := buildArgs("gpt-5-codex", "/tmp/out", "/tmp/schema", []string{"--dangerously-bypass-approvals-and-sandbox"}, "p")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--model gpt-5-codex") {
			t.Errorf("missing --model gpt-5-codex: %s", joined)
		}
		if !strings.Contains(joined, "--output-schema /tmp/schema") {
			t.Errorf("missing --output-schema /tmp/schema: %s", joined)
		}
		if !strings.Contains(joined, "--dangerously-bypass-approvals-and-sandbox") {
			t.Errorf("missing extra arg: %s", joined)
		}
		if args[len(args)-1] != "p" {
			t.Errorf("prompt must be last: %v", args)
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
	// No invented [system]/[user] markers — the model would not honor
	// them. Plain join only.
	if strings.Contains(got, "[system]") || strings.Contains(got, "[user]") {
		t.Errorf("bundlePrompt must not invent role markers: %q", got)
	}
}
