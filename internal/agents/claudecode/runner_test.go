package claudecode

import (
	"context"
	"errors"
	"os/exec"
	"testing"
	"time"

	"github.com/vairdict/vairdict/internal/state"
)

func TestRun_Success(t *testing.T) {
	r := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "echo", "hello from claude")
	}))

	result, err := r.Run(context.Background(), "write a test", "/tmp")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "hello from claude\n" {
		t.Errorf("output = %q, want %q", result.Output, "hello from claude\n")
	}
}

func TestRun_NonZeroExit(t *testing.T) {
	r := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sh", "-c", "echo fail >&2; exit 1")
	}))

	result, err := r.Run(context.Background(), "bad prompt", "/tmp")
	if err != nil {
		t.Fatalf("non-zero exit should not return error, got: %v", err)
	}
	if result.ExitCode != 1 {
		t.Errorf("exit code = %d, want 1", result.ExitCode)
	}
	if result.Stderr != "fail\n" {
		t.Errorf("stderr = %q, want %q", result.Stderr, "fail\n")
	}
}

func TestRun_NotInstalled(t *testing.T) {
	r := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "nonexistent-binary-xyz")
	}))

	_, err := r.Run(context.Background(), "prompt", "/tmp")
	if err == nil {
		t.Fatal("expected error for missing binary")
	}

	var notInstalled *NotInstalledError
	if !errors.As(err, &notInstalled) {
		t.Errorf("expected NotInstalledError, got: %T: %v", err, err)
	}
}

func TestRun_ContextCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel() // cancel immediately

	r := New(WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
		return exec.CommandContext(ctx, "sleep", "60")
	}))

	_, err := r.Run(ctx, "prompt", "/tmp")
	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
}

func TestRun_Timeout(t *testing.T) {
	r := New(
		WithTimeout(50*time.Millisecond),
		WithCommandFactory(func(ctx context.Context, name string, args ...string) *exec.Cmd {
			return exec.CommandContext(ctx, "sleep", "60")
		}),
	)

	_, err := r.Run(context.Background(), "prompt", "/tmp")
	if err == nil {
		t.Fatal("expected error for timeout")
	}
}

func TestFakeRunner(t *testing.T) {
	fake := &FakeRunner{
		Result: state.AgentResult{Output: "fake output"},
	}

	result, err := fake.Run(context.Background(), "test prompt", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Output != "fake output" {
		t.Errorf("output = %q, want %q", result.Output, "fake output")
	}
	if len(fake.Calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(fake.Calls))
	}
	if fake.Calls[0].Prompt != "test prompt" {
		t.Errorf("prompt = %q, want %q", fake.Calls[0].Prompt, "test prompt")
	}
	if fake.Calls[0].WorkDir != "/work" {
		t.Errorf("workDir = %q, want %q", fake.Calls[0].WorkDir, "/work")
	}
}

func TestFakeRunner_Error(t *testing.T) {
	fake := &FakeRunner{
		Err: errors.New("fake error"),
	}

	_, err := fake.Run(context.Background(), "prompt", "/work")
	if err == nil {
		t.Fatal("expected error")
	}
	if err.Error() != "fake error" {
		t.Errorf("error = %q, want %q", err.Error(), "fake error")
	}
}
