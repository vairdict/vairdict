package code

import (
	"context"
	"errors"
	"testing"

	"github.com/vairdict/vairdict/internal/state"
)

// FakeExecutor returns configurable output for testing.
type FakeExecutor struct {
	Responses map[string]fakeResponse
}

type fakeResponse struct {
	Output []byte
	Err    error
}

func (f *FakeExecutor) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
	// Build a key from the command.
	key := name
	if len(args) > 0 {
		key = name + " " + args[0]
	}
	if resp, ok := f.Responses[key]; ok {
		return resp.Output, resp.Err
	}
	return nil, nil
}

func TestJudge_AllPass(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"spm --version": {Output: []byte("spm 1.1.3")},
			"spm exec":      {Output: []byte("✓ format\n✓ lint\n✓ test\n✓ build\n")},
		},
	}

	judge := New(executor)
	verdict, err := judge.Judge(context.Background(), "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Pass {
		t.Error("expected pass")
	}
	if verdict.Score != 100 {
		t.Errorf("score = %v, want 100", verdict.Score)
	}
	if len(verdict.Gaps) != 0 {
		t.Errorf("gaps = %d, want 0", len(verdict.Gaps))
	}
}

func TestJudge_BuildFailed(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"spm --version": {Output: []byte("spm 1.1.3")},
			"spm exec":      {Output: []byte("✓ format\n✓ lint\n✓ test\n✗ build\nbuild error: missing main"), Err: errors.New("exit 1")},
		},
	}

	judge := New(executor)
	verdict, err := judge.Judge(context.Background(), "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail")
	}
	if verdict.Score != 75 {
		t.Errorf("score = %v, want 75", verdict.Score)
	}
	if len(verdict.Gaps) != 1 {
		t.Fatalf("gaps = %d, want 1", len(verdict.Gaps))
	}
	if verdict.Gaps[0].Severity != state.SeverityP0 {
		t.Errorf("severity = %v, want P0", verdict.Gaps[0].Severity)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("build failure should be blocking")
	}
}

func TestJudge_TestFailed(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"spm --version": {Output: []byte("spm 1.1.3")},
			"spm exec":      {Output: []byte("✓ format\n✓ lint\n✗ test\ntest failed: 2 failures\n✓ build"), Err: errors.New("exit 1")},
		},
	}

	judge := New(executor)
	verdict, err := judge.Judge(context.Background(), "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail")
	}
	if verdict.Score != 75 {
		t.Errorf("score = %v, want 75", verdict.Score)
	}
	if len(verdict.Gaps) != 1 {
		t.Fatalf("gaps = %d, want 1", len(verdict.Gaps))
	}
	if verdict.Gaps[0].Severity != state.SeverityP1 {
		t.Errorf("severity = %v, want P1", verdict.Gaps[0].Severity)
	}
}

func TestJudge_LintFailed(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"spm --version": {Output: []byte("spm 1.1.3")},
			"spm exec":      {Output: []byte("✓ format\n✗ lint\nlint error: unused var\n✓ test\n✓ build"), Err: errors.New("exit 1")},
		},
	}

	judge := New(executor)
	verdict, err := judge.Judge(context.Background(), "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail")
	}
	if len(verdict.Gaps) != 1 {
		t.Fatalf("gaps = %d, want 1", len(verdict.Gaps))
	}
	if verdict.Gaps[0].Severity != state.SeverityP2 {
		t.Errorf("severity = %v, want P2", verdict.Gaps[0].Severity)
	}
	if verdict.Gaps[0].Blocking {
		t.Error("lint failure should not be blocking")
	}
}

func TestJudge_AllFailed(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"spm --version": {Output: []byte("spm 1.1.3")},
			"spm exec":      {Output: []byte("✗ format\nformat failed\n✗ lint\nlint failed\n✗ test\ntest failed\n✗ build\nbuild failed"), Err: errors.New("exit 1")},
		},
	}

	judge := New(executor)
	verdict, err := judge.Judge(context.Background(), "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail")
	}
	if verdict.Score != 0 {
		t.Errorf("score = %v, want 0", verdict.Score)
	}
	if len(verdict.Gaps) != 4 {
		t.Errorf("gaps = %d, want 4", len(verdict.Gaps))
	}
}

func TestJudge_SpmNotInstalled(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"spm --version": {Err: errors.New("executable not found")},
		},
	}

	judge := New(executor)
	_, err := judge.Judge(context.Background(), "/work")
	if err == nil {
		t.Fatal("expected error for missing spm")
	}
	if !errors.Is(err, err) {
		t.Errorf("unexpected error type: %v", err)
	}
}

func TestJudge_GenericFailure(t *testing.T) {
	// When ship fails but no specific check is identifiable.
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"spm --version": {Output: []byte("spm 1.1.3")},
			"spm exec":      {Output: []byte("unknown error occurred"), Err: errors.New("exit 1")},
		},
	}

	judge := New(executor)
	verdict, err := judge.Judge(context.Background(), "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail")
	}
	// Should have at least one gap (build assumed failed).
	if len(verdict.Gaps) == 0 {
		t.Error("expected at least one gap for generic failure")
	}
}
