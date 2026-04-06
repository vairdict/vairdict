package github

import (
	"context"
	"errors"
	"strings"
	"testing"
)

func TestExecRunnerFailureIncludesStderr(t *testing.T) {
	r := &ExecRunner{}
	// `sh -c "echo out; echo err 1>&2; exit 3"` lets us verify that
	// stdout is preserved and stderr ends up in the wrapped error.
	stdout, err := r.Run(context.Background(), "sh", "-c", "echo out; echo boom 1>&2; exit 3")
	if err == nil {
		t.Fatal("expected error from non-zero exit")
	}
	if !strings.Contains(string(stdout), "out") {
		t.Errorf("stdout not preserved: %q", stdout)
	}
	var ee *ExecError
	if !errors.As(err, &ee) {
		t.Fatalf("expected *ExecError, got %T", err)
	}
	if !strings.Contains(ee.Stderr, "boom") {
		t.Errorf("stderr not captured: %q", ee.Stderr)
	}
	if !strings.Contains(ee.Error(), "boom") {
		t.Errorf("Error() should surface stderr: %q", ee.Error())
	}
	if ee.Unwrap() == nil {
		t.Error("Unwrap should return underlying error")
	}
}

func TestExecRunnerSuccessReturnsStdoutOnly(t *testing.T) {
	r := &ExecRunner{}
	out, err := r.Run(context.Background(), "sh", "-c", "echo hi; echo warn 1>&2")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if strings.TrimSpace(string(out)) != "hi" {
		t.Errorf("stdout = %q, want hi", out)
	}
}
