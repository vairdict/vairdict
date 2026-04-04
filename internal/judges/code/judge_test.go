package code

import (
	"context"
	"errors"
	"testing"

	"github.com/vairdict/vairdict/internal/config"
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
	key := name
	for _, a := range args {
		key += " " + a
	}
	if resp, ok := f.Responses[key]; ok {
		return resp.Output, resp.Err
	}
	return nil, nil
}

func testConfig() config.Config {
	return config.Config{
		Commands: config.CommandsConfig{
			Build: "make build",
			Test:  "make test",
			Lint:  "make lint",
		},
		Conventions: config.ConventionsConfig{
			Formatter: "gofmt",
		},
	}
}

func TestJudge_AllPass(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"gofmt -l .": {Output: []byte("")},
			"make lint":  {Output: []byte("ok")},
			"make test":  {Output: []byte("ok")},
			"make build": {Output: []byte("ok")},
		},
	}

	judge := New(executor, testConfig())
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
			"gofmt -l .": {Output: []byte("")},
			"make lint":  {Output: []byte("ok")},
			"make test":  {Output: []byte("ok")},
			"make build": {Output: []byte("build error: missing main"), Err: errors.New("exit 1")},
		},
	}

	judge := New(executor, testConfig())
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
			"gofmt -l .": {Output: []byte("")},
			"make lint":  {Output: []byte("ok")},
			"make test":  {Output: []byte("FAIL: 2 failures"), Err: errors.New("exit 1")},
			"make build": {Output: []byte("ok")},
		},
	}

	judge := New(executor, testConfig())
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
			"gofmt -l .": {Output: []byte("")},
			"make lint":  {Output: []byte("unused var"), Err: errors.New("exit 1")},
			"make test":  {Output: []byte("ok")},
			"make build": {Output: []byte("ok")},
		},
	}

	judge := New(executor, testConfig())
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

func TestJudge_FormatFailed(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"gofmt -l .": {Output: []byte("file.go\n"), Err: errors.New("exit 1")},
			"make lint":  {Output: []byte("ok")},
			"make test":  {Output: []byte("ok")},
			"make build": {Output: []byte("ok")},
		},
	}

	judge := New(executor, testConfig())
	verdict, err := judge.Judge(context.Background(), "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Pass {
		t.Error("expected fail")
	}
	if verdict.Gaps[0].Severity != state.SeverityP2 {
		t.Errorf("severity = %v, want P2", verdict.Gaps[0].Severity)
	}
}

func TestJudge_AllFailed(t *testing.T) {
	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"gofmt -l .": {Output: []byte("file.go"), Err: errors.New("exit 1")},
			"make lint":  {Output: []byte("lint error"), Err: errors.New("exit 1")},
			"make test":  {Output: []byte("test error"), Err: errors.New("exit 1")},
			"make build": {Output: []byte("build error"), Err: errors.New("exit 1")},
		},
	}

	judge := New(executor, testConfig())
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

func TestJudge_NoFormatter(t *testing.T) {
	cfg := testConfig()
	cfg.Conventions.Formatter = ""

	executor := &FakeExecutor{
		Responses: map[string]fakeResponse{
			"make lint":  {Output: []byte("ok")},
			"make test":  {Output: []byte("ok")},
			"make build": {Output: []byte("ok")},
		},
	}

	judge := New(executor, cfg)
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
}
