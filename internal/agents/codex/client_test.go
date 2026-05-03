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

// fakeCmd builds an exec.Cmd that runs `sh -c script` via the given ctx so
// each test can script stdout/stderr/exit-code precisely without touching
// the real codex binary.
func fakeCmd(ctx context.Context, script string) *exec.Cmd {
	return exec.CommandContext(ctx, "sh", "-c", script)
}

// stdoutCmd returns a fake exec.Cmd that prints the given bytes verbatim
// on stdout. Bytes are passed through an env var to sidestep shell quoting.
func stdoutCmd(ctx context.Context, stdout string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "sh", "-c", `printf '%s' "$FAKE_STDOUT"`)
	cmd.Env = append(os.Environ(), "FAKE_STDOUT="+stdout)
	return cmd
}

// stdoutCmdWithStderr is a fake exec.Cmd that prints stdout AND stderr
// and exits with the given code. Used for non-zero-exit error tests.
func stdoutCmdWithStderr(ctx context.Context, stdout, stderr string, exit int) *exec.Cmd {
	script := `printf '%s' "$FAKE_STDOUT"; printf '%s' "$FAKE_STDERR" >&2; exit ${FAKE_EXIT:-0}`
	cmd := exec.CommandContext(ctx, "sh", "-c", script)
	cmd.Env = append(os.Environ(),
		"FAKE_STDOUT="+stdout,
		"FAKE_STDERR="+stderr,
		"FAKE_EXIT="+itoa(exit),
	)
	return cmd
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
	envelope := `{"type":"result","is_error":false,"result":"{\"answer\":42,\"label\":\"ok\"}"}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stdoutCmd(ctx, envelope)
	}))

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
	// Codex sometimes wraps the result in ```json … ``` fences.
	envelope := "{\"type\":\"result\",\"is_error\":false,\"result\":\"```json\\n{\\\"x\\\":1}\\n```\"}"
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stdoutCmd(ctx, envelope)
	}))

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

func TestCompleteWithSystem_EnvelopeParseError(t *testing.T) {
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stdoutCmd(ctx, "not json at all")
	}))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if !strings.Contains(parseErr.Raw, "not json") {
		t.Errorf("expected raw to contain stdout, got %q", parseErr.Raw)
	}
}

func TestCompleteWithSystem_IsErrorEnvelope(t *testing.T) {
	envelope := `{"type":"result","subtype":"error_during_execution","is_error":true,"result":"oh no"}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stdoutCmd(ctx, envelope)
	}))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
	if !strings.Contains(parseErr.Err.Error(), "is_error=true") {
		t.Errorf("expected is_error message, got %v", parseErr.Err)
	}
}

func TestCompleteWithSystem_EmptyResult(t *testing.T) {
	envelope := `{"type":"result","is_error":false,"result":""}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stdoutCmd(ctx, envelope)
	}))

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
	}
}

func TestCompleteWithSystem_TargetDecodeFailure(t *testing.T) {
	// Envelope ok, result ok JSON but not an object — decoding into a
	// struct target should surface as ParseError.
	envelope := `{"type":"result","is_error":false,"result":"\"a string\""}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stdoutCmd(ctx, envelope)
	}))

	var got struct{ X int }
	err := c.Complete(context.Background(), "hi", &got)
	var parseErr *ParseError
	if !errors.As(err, &parseErr) {
		t.Fatalf("expected ParseError, got %T: %v", err, err)
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
		return fakeCmd(ctx, "sleep 30")
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
			return fakeCmd(ctx, "sleep 30")
		}),
	)

	var got map[string]any
	err := c.Complete(context.Background(), "hi", &got)
	if err == nil {
		t.Fatal("expected timeout error")
	}
}

// --- Argv shape ---

func TestCompleteWithSystem_ArgsIncludeSubcommandJSONAndExtras(t *testing.T) {
	// Production argv shape: codex exec --json [--model M] [<extra>...] <prompt>.
	// System prompt is bundled into the prompt arg (codex has no separate
	// --system flag in non-interactive mode), so the prompt arg must
	// contain the system text when system is set.
	var captured []string
	envelope := `{"type":"result","is_error":false,"result":"{}"}`
	c := New(
		WithExtraArgs("--dangerously-bypass-approvals-and-sandbox"),
		WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			captured = append([]string{name}, args...)
			return stdoutCmd(ctx, envelope)
		}),
	)

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "you are a judge", "do the thing", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(captured) == 0 {
		t.Fatalf("nothing captured")
	}
	if captured[0] != "codex" {
		t.Errorf("binary = %q, want codex", captured[0])
	}
	joined := strings.Join(captured, " ")
	for _, w := range []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox"} {
		if !strings.Contains(joined, w) {
			t.Errorf("captured args %q missing %q", joined, w)
		}
	}
	// Prompt must be last and must contain both the system prompt and
	// the user prompt.
	last := captured[len(captured)-1]
	if !strings.Contains(last, "you are a judge") {
		t.Errorf("prompt arg %q must contain system prompt", last)
	}
	if !strings.Contains(last, "do the thing") {
		t.Errorf("prompt arg %q must contain user prompt", last)
	}
}

func TestCompleteWithSystem_WithModelAddsFlag(t *testing.T) {
	var captured []string
	envelope := `{"type":"result","is_error":false,"result":"{}"}`
	c := New(
		WithModel("gpt-5-codex"),
		WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			captured = append([]string{name}, args...)
			return stdoutCmd(ctx, envelope)
		}),
	)

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Model() != "gpt-5-codex" {
		t.Errorf("client Model() = %q, want gpt-5-codex", c.Model())
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--model gpt-5-codex") {
		t.Errorf("captured args missing --model gpt-5-codex: %v", captured)
	}
}

func TestCompleteWithSystem_NoModelSkipsFlag(t *testing.T) {
	var captured []string
	envelope := `{"type":"result","is_error":false,"result":"{}"}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return stdoutCmd(ctx, envelope)
	}))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	for _, a := range captured {
		if a == "--model" {
			t.Errorf("client without WithModel must not add --model flag: %v", captured)
		}
	}
}

func TestCompleteWithSystem_NoSystemPromptKeepsPromptClean(t *testing.T) {
	// When system is empty, the prompt arg is the user prompt verbatim —
	// no bundled-in system fragment, no prefix.
	var captured []string
	envelope := `{"type":"result","is_error":false,"result":"{}"}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return stdoutCmd(ctx, envelope)
	}))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "just the prompt", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if captured[len(captured)-1] != "just the prompt" {
		t.Errorf("expected last arg to equal user prompt verbatim when system empty, got %q", captured[len(captured)-1])
	}
}

// --- Tool use ---

func TestCompleteWithTool_InjectsSchemaAndParsesResult(t *testing.T) {
	// CompleteWithTool must (a) inject the tool's JSON Schema into the
	// system prompt the subprocess sees, and (b) parse the envelope's
	// result back into the target as plain JSON.
	var captured []string
	envelope := `{"type":"result","is_error":false,"result":"{\"verdict\":\"pass\",\"score\":0.9}"}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return stdoutCmd(ctx, envelope)
	}))

	tool := claude.Tool{
		Name:        "verdict",
		Description: "judge verdict",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"verdict":{"type":"string"},"score":{"type":"number"}}}`),
	}

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

	// The schema must reach the subprocess somewhere in the prompt arg.
	last := captured[len(captured)-1]
	if !strings.Contains(last, "verdict") {
		t.Errorf("prompt arg must mention tool name, got %q", last)
	}
	if !strings.Contains(last, `"score"`) {
		t.Errorf("prompt arg must include schema fragment, got %q", last)
	}
}

func TestCompleteWithTool_RecoveryOnProseResult(t *testing.T) {
	// First subprocess call returns prose that won't parse as JSON.
	// Client must retry once with a recovery prompt and parse the
	// second response. The recovery call's prompt arg must reference
	// the schema and contain the original prose.
	proseEnvelope := `{"type":"result","is_error":false,"result":"sure thing! the answer is x=7"}`
	recoveryEnvelope := `{"type":"result","is_error":false,"result":"{\"x\":7}"}`

	calls := 0
	var capturedRuns [][]string
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedRuns = append(capturedRuns, append([]string{name}, args...))
		calls++
		switch calls {
		case 1:
			return stdoutCmd(ctx, proseEnvelope)
		default:
			return stdoutCmd(ctx, recoveryEnvelope)
		}
	}))

	tool := claude.Tool{
		Name:        "answer",
		Description: "give answer",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"x":{"type":"integer"}}}`),
	}

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

	// Recovery prompt must include the prose that failed to parse.
	recoveryPromptArg := capturedRuns[1][len(capturedRuns[1])-1]
	if !strings.Contains(recoveryPromptArg, "x=7") {
		t.Errorf("recovery prompt should include original prose, got %q", recoveryPromptArg)
	}
}

func TestCompleteWithTools_RoutesToFinalTool(t *testing.T) {
	envelope := `{"type":"result","is_error":false,"result":"{\"verdict\":\"pass\"}"}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stdoutCmd(ctx, envelope)
	}))

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
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return stdoutCmd(ctx, `{"type":"result","is_error":false,"result":"{}"}`)
	}))

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

// --- Availability + helpers ---

func TestIsAvailable_SmokeTest(t *testing.T) {
	// Just ensure it doesn't panic and returns a bool — we can't assume
	// the test host has codex installed or not.
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
	t.Run("base_no_model_no_extra", func(t *testing.T) {
		args := buildArgs("", nil, "user prompt")
		if args[0] != "exec" {
			t.Errorf("first arg must be subcommand exec, got %q", args[0])
		}
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--json") {
			t.Errorf("missing --json: %s", joined)
		}
		if strings.Contains(joined, "--model") {
			t.Errorf("must not include --model when empty: %s", joined)
		}
		if args[len(args)-1] != "user prompt" {
			t.Errorf("prompt must be last: %v", args)
		}
	})

	t.Run("with_model_and_extra", func(t *testing.T) {
		args := buildArgs("gpt-5-codex", []string{"--dangerously-bypass-approvals-and-sandbox"}, "p")
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--model gpt-5-codex") {
			t.Errorf("missing --model gpt-5-codex: %s", joined)
		}
		if !strings.Contains(joined, "--dangerously-bypass-approvals-and-sandbox") {
			t.Errorf("missing extra arg: %s", joined)
		}
		if args[len(args)-1] != "p" {
			t.Errorf("prompt must be last: %v", args)
		}
	})
}
