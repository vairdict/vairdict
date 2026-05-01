package claudecli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// fakeCmd builds an exec.Cmd that runs `sh -c script` via the given ctx so
// each test can script stdout/stderr/exit-code precisely without touching
// the real claude binary.
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

func TestCompleteWithSystem_HappyPath(t *testing.T) {
	envelope := `{"type":"result","subtype":"success","is_error":false,"result":"{\"answer\":42,\"label\":\"ok\"}"}`
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
	// Claude sometimes wraps the result in ```json … ``` fences.
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

func TestCompleteWithSystem_ArgsIncludeSystemAndExtras(t *testing.T) {
	var captured []string
	envelope := `{"type":"result","is_error":false,"result":"{}"}`
	c := New(
		WithExtraArgs("--dangerously-skip-permissions", "--model", "opus"),
		WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			captured = append([]string{name}, args...)
			return stdoutCmd(ctx, envelope)
		}),
	)

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "you are a judge", "do the thing", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := strings.Join(captured, " ")
	wants := []string{"claude", "-p", "--output-format", "json", "--append-system-prompt", "you are a judge", "--dangerously-skip-permissions", "--model", "opus", "do the thing"}
	for _, w := range wants {
		if !strings.Contains(joined, w) {
			t.Errorf("captured args %q missing %q", joined, w)
		}
	}
	// Prompt must be last.
	if captured[len(captured)-1] != "do the thing" {
		t.Errorf("prompt not last: %v", captured)
	}
}

func TestCompleteWithSystem_WithModelAddsFlag(t *testing.T) {
	// AC: WithModel must reach the actual subprocess invocation. Pinning
	// the judge model in config has to flow into the claude --model flag
	// or it doesn't actually do anything.
	var captured []string
	envelope := `{"type":"result","is_error":false,"result":"{}"}`
	c := New(
		WithModel("claude-opus-4-7"),
		WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			captured = append([]string{name}, args...)
			return stdoutCmd(ctx, envelope)
		}),
	)

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if c.Model() != "claude-opus-4-7" {
		t.Errorf("client Model() = %q, want claude-opus-4-7", c.Model())
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--model claude-opus-4-7") {
		t.Errorf("captured args missing --model claude-opus-4-7: %v", captured)
	}
}

func TestCompleteWithSystem_NoModelSkipsFlag(t *testing.T) {
	// Without WithModel the CLI's default applies — no --model flag.
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

func TestCompleteWithSystem_NoSystemPromptSkipsFlag(t *testing.T) {
	var captured []string
	envelope := `{"type":"result","is_error":false,"result":"{}"}`
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return stdoutCmd(ctx, envelope)
	}))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "", "prompt", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	for _, a := range captured {
		if a == "--append-system-prompt" {
			t.Errorf("should not pass --append-system-prompt when system is empty: %v", captured)
		}
	}
}

func TestIsAvailable_SmokeTest(t *testing.T) {
	// Just ensure it doesn't panic and returns a bool — we can't assume
	// the test host has claude installed or not.
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
			"Now I have everything I need. Let me produce the plan.\n\n```json\n{\"x\":4}\n```",
			`{"x":4}`,
		},
		{
			"preamble_then_fence_no_close",
			"Let me produce the plan.\n\n```json\n{\"x\":5}\n",
			`{"x":5}`,
		},
		{
			"prose_then_bare_object",
			"Here is the plan: {\"x\":6} done.",
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
			// Regression: a JSON string value that contains an embedded
			// ``` fence used to fool the fence-first extractor into
			// matching the embedded fence as the closing fence and
			// returning a truncated payload. Brace matching ignores
			// braces and fences inside string literals.
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

// --- Session reuse tests (#137) ---

// stdoutCmdWithStderr is a fake exec.Cmd that prints stdout AND stderr.
// Used for expired-session tests so the script can exit non-zero with
// a stderr message resembling Claude's "session not found" wording.
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

func TestCompleteWithSystem_FirstCallSendsFullSystemPrompt(t *testing.T) {
	// First call (no prior session) must send --append-system-prompt
	// AND must NOT include --resume.
	envelope := `{"type":"result","is_error":false,"result":"{}","session_id":"sess_abc"}`
	var captured []string
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		captured = append([]string{name}, args...)
		return stdoutCmd(ctx, envelope)
	}))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "the system prompt", "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--append-system-prompt the system prompt") {
		t.Errorf("first call must include --append-system-prompt, got: %s", joined)
	}
	if strings.Contains(joined, "--resume") {
		t.Errorf("first call must NOT include --resume, got: %s", joined)
	}
	if c.SessionID() != "sess_abc" {
		t.Errorf("session id not captured: got %q, want sess_abc", c.SessionID())
	}
}

func TestCompleteWithSystem_SecondCallResumesAndSkipsSystemPrompt(t *testing.T) {
	// Second call (session id captured from first) must include
	// --resume <id> and must NOT re-send --append-system-prompt — that's
	// the whole point of the optimization.
	envelopes := []string{
		`{"type":"result","is_error":false,"result":"{}","session_id":"sess_abc"}`,
		`{"type":"result","is_error":false,"result":"{}","session_id":"sess_abc"}`,
	}
	var capturedRuns [][]string
	idx := 0
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedRuns = append(capturedRuns, append([]string{name}, args...))
		env := envelopes[idx]
		idx++
		return stdoutCmd(ctx, env)
	}))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "system prompt v1", "p1", &got); err != nil {
		t.Fatalf("first: %v", err)
	}
	if err := c.CompleteWithSystem(context.Background(), "system prompt v1", "p2", &got); err != nil {
		t.Fatalf("second: %v", err)
	}

	if len(capturedRuns) != 2 {
		t.Fatalf("expected 2 runs, got %d", len(capturedRuns))
	}
	first := strings.Join(capturedRuns[0], " ")
	second := strings.Join(capturedRuns[1], " ")

	if !strings.Contains(first, "--append-system-prompt") {
		t.Errorf("first call should include --append-system-prompt, got: %s", first)
	}
	if !strings.Contains(second, "--resume sess_abc") {
		t.Errorf("second call should include --resume sess_abc, got: %s", second)
	}
	if strings.Contains(second, "--append-system-prompt") {
		t.Errorf("second call must NOT re-send --append-system-prompt, got: %s", second)
	}
}

func TestWithSessionID_FirstCallResumesImmediately(t *testing.T) {
	// Construction-time session id (e.g. from `vairdict resume`)
	// must take effect on the very first call.
	envelope := `{"type":"result","is_error":false,"result":"{}","session_id":"sess_xyz"}`
	var captured []string
	c := New(
		WithSessionID("sess_xyz"),
		WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			captured = append([]string{name}, args...)
			return stdoutCmd(ctx, envelope)
		}),
	)
	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "system prompt", "p", &got); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	joined := strings.Join(captured, " ")
	if !strings.Contains(joined, "--resume sess_xyz") {
		t.Errorf("seeded session must --resume on first call, got: %s", joined)
	}
	if strings.Contains(joined, "--append-system-prompt") {
		t.Errorf("seeded session must NOT re-send system prompt, got: %s", joined)
	}
}

func TestSetSessionID_RoundTrips(t *testing.T) {
	c := New()
	if c.SessionID() != "" {
		t.Errorf("fresh client should have empty session id, got %q", c.SessionID())
	}
	c.SetSessionID("manual_id")
	if c.SessionID() != "manual_id" {
		t.Errorf("SetSessionID round-trip failed: got %q", c.SessionID())
	}
	c.SetSessionID("")
	if c.SessionID() != "" {
		t.Errorf("empty SetSessionID should clear, got %q", c.SessionID())
	}
}

func TestCompleteWithSystem_ExpiredSessionRetriesFresh(t *testing.T) {
	// First call: success, captures session id.
	// Second call: simulates expired session — exits non-zero with
	// stderr matching "session not found". Client must clear the
	// session id and retry once with a fresh full-args invocation.
	successEnv := `{"type":"result","is_error":false,"result":"{}","session_id":"sess_old"}`
	freshEnv := `{"type":"result","is_error":false,"result":"{}","session_id":"sess_new"}`

	var capturedRuns [][]string
	calls := 0
	c := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		capturedRuns = append(capturedRuns, append([]string{name}, args...))
		calls++
		switch calls {
		case 1:
			return stdoutCmd(ctx, successEnv)
		case 2:
			// Resume attempt — fail with expired-session stderr.
			return stdoutCmdWithStderr(ctx, "", "Error: session not found\n", 1)
		default:
			// Fresh retry succeeds.
			return stdoutCmd(ctx, freshEnv)
		}
	}))

	var got map[string]any
	if err := c.CompleteWithSystem(context.Background(), "sys", "p1", &got); err != nil {
		t.Fatalf("first call: %v", err)
	}
	if err := c.CompleteWithSystem(context.Background(), "sys", "p2", &got); err != nil {
		t.Fatalf("second call after expired-session retry: %v", err)
	}

	if calls != 3 {
		t.Errorf("expected 3 subprocess invocations (initial, expired resume, fresh retry), got %d", calls)
	}
	joinedRetry := strings.Join(capturedRuns[2], " ")
	if strings.Contains(joinedRetry, "--resume") {
		t.Errorf("retry after expired session should NOT --resume, got: %s", joinedRetry)
	}
	if !strings.Contains(joinedRetry, "--append-system-prompt") {
		t.Errorf("retry after expired session should re-send --append-system-prompt, got: %s", joinedRetry)
	}
	if c.SessionID() != "sess_new" {
		t.Errorf("after retry, session id should be sess_new, got %q", c.SessionID())
	}
}

func TestLooksLikeSessionExpired(t *testing.T) {
	cases := []struct {
		stderr string
		want   bool
	}{
		{"Error: session not found", true},
		{"session expired", true},
		{"Session does not exist for id sess_xyz", true},
		{"no such session", true},
		{"invalid session", true},
		{"connection timeout", false},
		{"some other error", false},
		{"", false},
	}
	for _, tc := range cases {
		if got := looksLikeSessionExpired(tc.stderr); got != tc.want {
			t.Errorf("looksLikeSessionExpired(%q) = %v, want %v", tc.stderr, got, tc.want)
		}
	}
}

func TestBuildArgs_Shape(t *testing.T) {
	t.Run("no_session_with_system", func(t *testing.T) {
		args := buildArgs("", "sys", "", nil, "p", false)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "-p --output-format json") {
			t.Errorf("missing core flags: %s", joined)
		}
		if !strings.Contains(joined, "--append-system-prompt sys") {
			t.Errorf("missing system: %s", joined)
		}
		if strings.Contains(joined, "--resume") {
			t.Errorf("must not include --resume: %s", joined)
		}
		if args[len(args)-1] != "p" {
			t.Errorf("prompt must be last: %v", args)
		}
	})

	t.Run("with_session_skips_system", func(t *testing.T) {
		args := buildArgs("sess_id", "sys", "", nil, "p", false)
		joined := strings.Join(args, " ")
		if !strings.HasPrefix(joined, "--resume sess_id") {
			t.Errorf("--resume must come first, got: %s", joined)
		}
		if strings.Contains(joined, "--append-system-prompt") {
			t.Errorf("must not include --append-system-prompt on resume: %s", joined)
		}
	})

	t.Run("notools_flag", func(t *testing.T) {
		args := buildArgs("", "", "", nil, "p", true)
		joined := strings.Join(args, " ")
		if !strings.Contains(joined, "--tools") {
			t.Errorf("expected --tools flag: %s", joined)
		}
	})
}
