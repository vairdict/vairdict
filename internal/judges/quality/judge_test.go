package quality

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/judges/verdictschema"
	"github.com/vairdict/vairdict/internal/standards"
	"github.com/vairdict/vairdict/internal/state"
)

func TestQualitySystemPrompt_IncludesBaseline(t *testing.T) {
	// #84: the quality judge prompt must include the non-negotiable
	// standards so violations can be flagged with the baseline marker.
	if !strings.Contains(systemPrompt, standards.Block) {
		t.Error("quality judge system prompt must include the baseline standards block")
	}
	for _, tag := range standards.AllRules {
		if !strings.Contains(systemPrompt, string(tag)) {
			t.Errorf("quality judge system prompt missing baseline rule tag %q", tag)
		}
	}
}

func TestQualityJudge_BaselineMarkerForcesBlocking(t *testing.T) {
	// A P1 baseline gap is already blocking under the quality judge's
	// default block set (P0+P1), so this test primarily guards the wiring
	// — a regression where ForceBaselineBlocking stops being called would
	// not show up here, but the unit test in the standards package covers
	// the promotion logic itself. The observable behavior here is: the
	// gap must end up Blocking=true regardless of what the LLM said.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP1, Description: "BASELINE: no-secrets: hardcoded token", Blocking: false},
			},
		},
	}
	judge := New(fake, nil, testConfig())

	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("baseline-marked gap must be blocking")
	}
	if verdict.Pass {
		t.Error("pass must be false when a blocking baseline gap is present")
	}
}

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

func TestJudge_Pass_NoGapsScoresFull(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "build a REST API", "1. Create handlers\n2. Add routes", "diff --git a/x.go b/x.go\n+func H() {}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true with 0 gaps")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100 with 0 gaps, got %f", verdict.Score)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if fake.Calls[0].ToolName != verdictschema.ToolName {
		t.Errorf("expected tool name %q, got %q", verdictschema.ToolName, fake.Calls[0].ToolName)
	}
	if !contains(fake.Calls[0].Prompt, "build a REST API") {
		t.Error("expected prompt to contain intent")
	}
	if !contains(fake.Calls[0].Prompt, "Create handlers") {
		t.Error("expected prompt to contain plan")
	}
}

func TestJudge_IntentMismatch_P0Blocks(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "code implements CRUD but intent was auth system"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "build auth system", "1. Implement auth", "diff --git a/x.go b/x.go\n+func crud() {}")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Pass {
		t.Error("expected pass=false for blocking P0 gap")
	}
	// 100 - 40 = 60.
	if verdict.Score != 60 {
		t.Errorf("expected score 60 (100-40), got %f", verdict.Score)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("expected P0 gap to be blocking")
	}
}

func TestJudge_E2EPass(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("all tests passed")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true when AI and e2e both pass")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100, got %f", verdict.Score)
	}
}

func TestJudge_E2EFail_AddsBlockingGap(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("FAIL: connection refused"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// E2E fail adds a P1 gap -> 100 - 20 = 80.
	if verdict.Score != 80 {
		t.Errorf("expected score 80, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false due to blocking e2e gap, even with score 80")
	}
	if len(verdict.Gaps) != 1 {
		t.Fatalf("expected 1 gap from e2e, got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[0].Severity != state.SeverityP1 {
		t.Errorf("expected P1 severity for e2e failure, got %s", verdict.Gaps[0].Severity)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("expected e2e failure gap to be blocking (assigned deterministically)")
	}
}

func TestJudge_E2ENotRequired(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	cfg := testConfig()
	cfg.Commands.E2E = "make e2e"
	// E2ERequired is false by default.

	judge := New(fake, nil, cfg)
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100, got %f", verdict.Score)
	}
}

func TestJudge_E2ERequiredNoCommand(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	cfg := testConfig()
	cfg.Phases.Quality.E2ERequired = true
	// Commands.E2E is empty.

	judge := New(fake, nil, cfg)
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true when no e2e command configured")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100, got %f", verdict.Score)
	}
}

func TestJudge_AccumulatedP2sDragBelowThreshold(t *testing.T) {
	// Four P2 gaps -> 100 - 40 = 60, below PassThreshold (70).
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "a"},
				{Severity: state.SeverityP2, Description: "b"},
				{Severity: state.SeverityP2, Description: "c"},
				{Severity: state.SeverityP2, Description: "d"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 60 {
		t.Errorf("expected score 60, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false when accumulated non-blocking gaps drag score below threshold")
	}
}

func TestJudge_PassAtExactThreshold(t *testing.T) {
	// Three P2 gaps -> 100 - 30 = 70, exactly equal to threshold. Non-blocking.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "a"},
				{Severity: state.SeverityP2, Description: "b"},
				{Severity: state.SeverityP2, Description: "c"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 70 {
		t.Errorf("expected score 70, got %f", verdict.Score)
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
	_, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err == nil {
		t.Fatal("expected error when client fails")
	}
}

func TestJudge_MixedGapsWithE2E(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "missing edge case handling"},
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
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// One P2 (-10) + one P1 e2e (-20) = 100 - 30 = 70.
	if verdict.Score != 70 {
		t.Errorf("expected score 70, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false — the e2e P1 gap is blocking")
	}
	if len(verdict.Gaps) != 2 {
		t.Fatalf("expected 2 gaps (1 AI + 1 e2e), got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[1].Severity != state.SeverityP1 {
		t.Errorf("expected second gap P1 (e2e), got %s", verdict.Gaps[1].Severity)
	}
	if len(verdict.Questions) != 1 {
		t.Errorf("expected 1 question, got %d", len(verdict.Questions))
	}
}

func TestJudge_ScoreFloorAtZero(t *testing.T) {
	// Many severe gaps -> penalty exceeds 100, score must floor at 0.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "wrong feature"},
				{Severity: state.SeverityP0, Description: "missing core"},
				{Severity: state.SeverityP0, Description: "broken api"},
			},
		},
	}

	runner := &FakeRunner{
		Responses: map[string]fakeResponse{
			"make e2e": {Output: []byte("FAIL"), Err: errors.New("exit 1")},
		},
	}

	judge := New(fake, runner, testConfigWithE2E())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 0 {
		t.Errorf("expected score 0 (floored), got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false")
	}
}

func TestJudge_PromptContainsDiff(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{Gaps: []state.Gap{}},
	}

	judge := New(fake, nil, testConfig())
	const diff = "diff --git a/foo.go b/foo.go\n+++ b/foo.go\n+func Foo() {}"
	_, err := judge.Judge(context.Background(), "intent", "plan", diff)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if !contains(fake.Calls[0].Prompt, "func Foo() {}") {
		t.Error("expected prompt to contain diff content")
	}
	if !contains(fake.Calls[0].Prompt, "## Diff") {
		t.Error("expected prompt to contain diff section header")
	}
}

func TestJudge_EmptyDiffPlaceholder(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{Gaps: []state.Gap{}},
	}

	judge := New(fake, nil, testConfig())
	_, err := judge.Judge(context.Background(), "intent", "plan", "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if !contains(fake.Calls[0].Prompt, "no diff provided") {
		t.Error("expected empty-diff placeholder in prompt")
	}
}

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

func TestJudge_SummaryRoundTrip(t *testing.T) {
	want := "## Reviewed\n- Intent matches implementation\n\n## Notes\n- e2e tests still green"
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Summary: want,
		},
	}

	cfg := config.Config{}
	cfg.Phases.Quality.E2ERequired = false
	judge := New(fake, &FakeRunner{}, cfg)
	verdict, err := judge.Judge(context.Background(), "build it", "the plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Summary != want {
		t.Errorf("summary lost in round-trip\n got: %q\nwant: %q", verdict.Summary, want)
	}
}

func TestJudge_SystemPromptMentionsSummary(t *testing.T) {
	if !strings.Contains(systemPrompt, "summary") {
		t.Error("system prompt no longer instructs the LLM to emit a summary field")
	}
	for _, section := range []string{"## Reviewed", "## Notes"} {
		if !strings.Contains(systemPrompt, section) {
			t.Errorf("system prompt missing summary sub-section %q", section)
		}
	}
}

func TestJudge_SystemPromptContainsSecurityChecks(t *testing.T) {
	for _, keyword := range []string{
		"### Security",
		"Hardcoded secrets",
		"SQL injection",
		"Command injection",
		"Path traversal",
		"insecure crypto",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing security keyword %q", keyword)
		}
	}
}

func TestJudge_SystemPromptContainsCodeReuseChecks(t *testing.T) {
	for _, keyword := range []string{
		"### Code reuse",
		"duplicated",
		"copy-pasted",
		"near-identical",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing code-reuse keyword %q", keyword)
		}
	}
}

func TestJudge_SystemPromptContainsStyleChecks(t *testing.T) {
	for _, keyword := range []string{
		"### Style",
		"maintainability",
		"Magic numbers",
		"nested control flow",
		"error handling",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing style keyword %q", keyword)
		}
	}
}

func TestJudge_SecurityChecksAreBlocking(t *testing.T) {
	if !strings.Contains(systemPrompt, "P1 blocking") {
		t.Error("system prompt should mark security checks as P1 blocking")
	}
}

func TestJudge_CodeReuseAndStyleAreNonBlocking(t *testing.T) {
	if !strings.Contains(systemPrompt, "P2 non-blocking") {
		t.Error("system prompt should mark code-reuse checks as P2 non-blocking")
	}
	if !strings.Contains(systemPrompt, "P3 non-blocking") {
		t.Error("system prompt should mark style checks as P3 non-blocking")
	}
}

func TestJudge_SystemPromptRequestsFileAndLine(t *testing.T) {
	// #72: the system prompt must instruct the LLM to include file/line
	// in gaps so inline PR comments can be posted. #100 follow-up: the
	// prompt must also push back against omitting file/line — otherwise
	// the judge tends to classify everything as "architectural" and
	// gaps fall out of the inline-review path.
	for _, keyword := range []string{
		`"file"`,
		`"line"`,
		"b/ side",
		"+ side",
		"ANY plausible anchor",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing file/line keyword %q", keyword)
		}
	}
}

func TestJudge_GapWithFileAndLine(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{
					Severity:    state.SeverityP2,
					Description: "magic number",
					File:        "internal/foo/bar.go",
					Line:        42,
				},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(verdict.Gaps) != 1 {
		t.Fatalf("expected 1 gap, got %d", len(verdict.Gaps))
	}
	if verdict.Gaps[0].File != "internal/foo/bar.go" {
		t.Errorf("expected file = %q, got %q", "internal/foo/bar.go", verdict.Gaps[0].File)
	}
	if verdict.Gaps[0].Line != 42 {
		t.Errorf("expected line = 42, got %d", verdict.Gaps[0].Line)
	}
}

func TestJudge_CodeFactsInjectedIntoPrompt(t *testing.T) {
	// Issue #85: objective checks (tests pass, lint clean, build succeeds) must
	// be sourced from the code judge and injected as facts, so the LLM does
	// not re-evaluate them.
	fake := &claude.FakeClient{
		Response: state.Verdict{Gaps: []state.Gap{}},
	}

	judge := New(fake, nil, testConfig()).WithCodeFacts("Score: 100%\nAll checks passed (lint, test, build)")
	_, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !contains(fake.Calls[0].Prompt, "## Facts (from code judge)") {
		t.Error("expected facts section in prompt")
	}
	if !contains(fake.Calls[0].Prompt, "All checks passed") {
		t.Error("expected fact body in prompt")
	}
}

func TestJudge_SystemPromptInstructsNoRecheckOfObjectiveChecks(t *testing.T) {
	// The judge must tell the LLM to trust the code judge's results.
	for _, keyword := range []string{
		"tests",
		"lint",
		"build",
		"code judge",
	} {
		if !strings.Contains(systemPrompt, keyword) {
			t.Errorf("system prompt missing 'do not re-evaluate' keyword %q", keyword)
		}
	}
}

func TestJudge_SystemPromptIncludesFewShotExamples(t *testing.T) {
	// Issue #85 requires at least 2 few-shot examples (one pass, one fail).
	// Post-#79 we add Example 3 — a false-positive the judge must NOT
	// produce — so keep all three anchors asserted.
	for _, needle := range []string{"Example 1", "Example 2", "Example 3", "submit_verdict"} {
		if !strings.Contains(systemPrompt, needle) {
			t.Errorf("system prompt missing few-shot anchor %q", needle)
		}
	}
}

func TestJudge_SystemPromptHasPartialDiffFalsePositiveExample(t *testing.T) {
	// The judge violated the partial-diff rule on PR #99 by flagging an
	// existing same-package function as "undefined". Keep a concrete
	// false-positive example + its correction in the prompt so the model
	// sees the mistake pattern explicitly, not just the abstract rule.
	for _, needle := range []string{
		"INCORRECT submit_verdict",
		"CORRECT submit_verdict",
		"same-package symbols do not need imports",
	} {
		if !strings.Contains(systemPrompt, needle) {
			t.Errorf("system prompt missing partial-diff false-positive anchor %q", needle)
		}
	}
}

func TestJudge_BlockingGapFailsEvenWithHighScore(t *testing.T) {
	// A single P1 gap costs only 20 points (score stays at 80), but the
	// gap is blocking, so the verdict must still fail.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP1, Description: "tautological assertion"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 80 {
		t.Errorf("expected score 80, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false — blocking gap")
	}
}

func TestJudge_NonBlockingGapsAllowPass(t *testing.T) {
	// One P2 + one P3 -> 100 - 10 - 5 = 85, non-blocking, pass.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "magic number"},
				{Severity: state.SeverityP3, Description: "long function"},
			},
		},
	}

	judge := New(fake, nil, testConfig())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", "fake-diff")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 85 {
		t.Errorf("expected score 85, got %f", verdict.Score)
	}
	if !verdict.Pass {
		t.Error("expected pass=true — only non-blocking gaps, score above threshold")
	}
}
