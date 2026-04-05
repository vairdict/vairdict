package quality

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

// FakeRunner returns configurable output for testing e2e commands.
type FakeRunner struct {
	Responses map[string]fakeResponse
}

type fakeResponse struct {
	Output []byte
	Err    error
}

func (f *FakeRunner) Run(_ context.Context, _ string, name string, args ...string) ([]byte, error) {
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
		Project: config.ProjectConfig{Name: "test"},
		Phases: config.PhasesConfig{
			Quality: config.QualityPhaseConfig{
				MaxLoops:     3,
				E2ERequired:  false,
				PRReviewMode: "auto",
			},
		},
	}
}

func testConfigWithE2E() config.Config {
	cfg := testConfig()
	cfg.Phases.Quality.E2ERequired = true
	cfg.Commands.E2E = "make e2e"
	return cfg
}

func TestJudge_Pass(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 85,
			Pass:  true,
			Gaps:  []state.Gap{},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "build a REST API", "1. Create handlers\n2. Add routes", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true for score 85")
	}
	if verdict.Score != 85 {
		t.Errorf("expected score 85, got %f", verdict.Score)
	}
	if len(verdict.Gaps) != 0 {
		t.Errorf("expected no gaps, got %d", len(verdict.Gaps))
	}

	// Verify prompt was sent correctly.
	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if fake.Calls[0].System == "" {
		t.Error("expected system prompt to be set")
	}
	if !contains(fake.Calls[0].Prompt, "build a REST API") {
		t.Error("expected prompt to contain intent")
	}
	if !contains(fake.Calls[0].Prompt, "Create handlers") {
		t.Error("expected prompt to contain plan")
	}
}

func TestJudge_IntentMismatch(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 30,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "code implements CRUD but intent was auth system", Blocking: true},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "build auth system", "1. Implement auth", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Pass {
		t.Error("expected pass=false for score 30")
	}
	if verdict.Score != 30 {
		t.Errorf("expected score 30, got %f", verdict.Score)
	}
	if len(verdict.Gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[0].Severity != state.SeverityP0 {
		t.Errorf("expected P0 severity for intent mismatch, got %s", verdict.Gaps[0].Severity)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("expected P0 gap to be blocking")
	}
}

func TestJudge_E2EPass(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 90,
			Pass:  true,
			Gaps:  []state.Gap{},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("all tests passed")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true when AI and e2e both pass")
	}
	if verdict.Score != 90 {
		t.Errorf("expected score 90, got %f", verdict.Score)
	}
	if len(verdict.Gaps) != 0 {
		t.Errorf("expected no gaps, got %d", len(verdict.Gaps))
	}
}

func TestJudge_E2EFail(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 85,
			Pass:  true,
			Gaps:  []state.Gap{},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("FAIL: connection refused"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Score should be penalized: 85 - 30 = 55.
	if verdict.Score != 55 {
		t.Errorf("expected score 55 (85-30), got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false when e2e fails and drops score below 70")
	}
	if len(verdict.Gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[0].Severity != state.SeverityP1 {
		t.Errorf("expected P1 severity for e2e failure, got %s", verdict.Gaps[0].Severity)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("expected e2e failure gap to be blocking")
	}
}

func TestJudge_E2EFailHighScore(t *testing.T) {
	// AI gives 100, e2e fails -> 100-30=70, still passes.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 100,
			Pass:  true,
			Gaps:  []state.Gap{},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("FAIL"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 70 {
		t.Errorf("expected score 70 (100-30), got %f", verdict.Score)
	}
	if !verdict.Pass {
		t.Error("expected pass=true when score is exactly 70")
	}
}

func TestJudge_E2ENotRequired(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 75,
			Pass:  true,
			Gaps:  []state.Gap{},
		},
	}

	// E2E not required — runner should not be called even if command exists.
	cfg := testConfig()
	cfg.Commands.E2E = "make e2e"
	// E2ERequired is false by default.

	judge := New(fake, nil, cfg)
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true")
	}
	if verdict.Score != 75 {
		t.Errorf("expected score 75, got %f", verdict.Score)
	}
}

func TestJudge_E2ERequiredNoCommand(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 80,
			Pass:  true,
			Gaps:  []state.Gap{},
		},
	}

	// E2E required but no command configured — skips e2e gracefully.
	cfg := testConfig()
	cfg.Phases.Quality.E2ERequired = true
	// Commands.E2E is empty.

	judge := New(fake, nil, cfg)
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true when no e2e command configured")
	}
	if verdict.Score != 80 {
		t.Errorf("expected score 80, got %f", verdict.Score)
	}
}

func TestJudge_PassThresholdEnforced(t *testing.T) {
	// AI says pass=true but score is 65 — judge overrides to pass=false.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 65,
			Pass:  true,
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "minor issue", Blocking: false},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Pass {
		t.Error("expected pass=false when score 65 < threshold 70")
	}
}

func TestJudge_PassAtExactThreshold(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 70,
			Pass:  false,
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true when score equals threshold exactly")
	}
}

func TestJudge_ClientError(t *testing.T) {
	fake := &claude.FakeClient{
		Err: fmt.Errorf("connection refused"),
	}

	judge := New(fake, nil, testConfig())
	_, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err == nil {
		t.Fatal("expected error when client fails")
	}
}

func TestJudge_MixedGapsWithE2E(t *testing.T) {
	// AI finds minor gaps + e2e fails = combined gaps in verdict.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 80,
			Pass:  true,
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "missing edge case handling", Blocking: false},
			},
			Questions: []state.Question{
				{Text: "Should we add retry logic?", Priority: "medium"},
			},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("timeout"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Score: 80 - 30 = 50.
	if verdict.Score != 50 {
		t.Errorf("expected score 50, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false")
	}
	if len(verdict.Gaps) != 2 {
		t.Fatalf("expected 2 gaps (1 AI + 1 e2e), got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[0].Severity != state.SeverityP2 {
		t.Errorf("expected first gap P2, got %s", verdict.Gaps[0].Severity)
	}
	if verdict.Gaps[1].Severity != state.SeverityP1 {
		t.Errorf("expected second gap P1 (e2e), got %s", verdict.Gaps[1].Severity)
	}
	if len(verdict.Questions) != 1 {
		t.Errorf("expected 1 question, got %d", len(verdict.Questions))
	}
}

func TestJudge_ScoreFloorAtZero(t *testing.T) {
	// AI gives low score, e2e also fails — score should not go below 0.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 20,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "wrong feature", Blocking: true},
			},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("FAIL"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "/work")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Score: max(0, 20-30) = 0.
	if verdict.Score != 0 {
		t.Errorf("expected score 0, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false")
	}
	// 1 AI gap + 1 e2e gap.
	if len(verdict.Gaps) != 2 {
		t.Errorf("expected 2 gaps, got %d", len(verdict.Gaps))
	}
}

func TestJudge_PromptContainsWorkDir(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{Score: 80, Pass: true},
	}

	judge := New(fake, nil, testConfig())
	_, err := judge.Judge(context.Background(), "intent", "plan", "/my/project/dir")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if !contains(fake.Calls[0].Prompt, "/my/project/dir") {
		t.Error("expected prompt to contain work directory path")
	}
}

func contains(s, substr string) bool {
	return len(s) >= len(substr) && containsStr(s, substr)
}

func containsStr(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
