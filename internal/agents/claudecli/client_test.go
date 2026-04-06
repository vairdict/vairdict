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
		in, want string
	}{
		{`{"x":1}`, `{"x":1}`},
		{"```json\n{\"x\":1}\n```", `{"x":1}` + "\n"},
		{"```\n{\"x\":2}\n```", `{"x":2}` + "\n"},
		{"   {\"x\":3}   ", `{"x":3}`},
	}
	for _, tc := range cases {
		got := extractJSON(tc.in)
		if got != tc.want {
			t.Errorf("extractJSON(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}
