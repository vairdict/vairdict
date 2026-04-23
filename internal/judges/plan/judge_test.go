package plan

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/judges/verdictschema"
	"github.com/vairdict/vairdict/internal/standards"
	"github.com/vairdict/vairdict/internal/state"
)

func TestSystemPrompt_IncludesBaseline(t *testing.T) {
	// #84: the judge must see the non-negotiable standards so it can flag
	// baseline violations with the correct marker.
	if !strings.Contains(systemPrompt, standards.Block) {
		t.Error("plan judge system prompt must include the baseline standards block")
	}
	for _, tag := range standards.AllRules {
		if !strings.Contains(systemPrompt, string(tag)) {
			t.Errorf("plan judge system prompt missing baseline rule tag %q", tag)
		}
	}
}

func TestJudge_BaselineViolationForcedBlocking_UnderPermissiveConfig(t *testing.T) {
	// Simulate a team config that only blocks P0. A baseline P1 gap would
	// normally slip through — the judge must force it blocking anyway.
	cfg := config.PlanPhaseConfig{
		CoverageThreshold: 60,
		MaxLoops:          3,
		Severity: config.SeverityConfig{
			BlockOn:  []string{"P0"},
			AssumeOn: []string{"P2"},
			DeferOn:  []string{"P3"},
		},
	}
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP1, Description: "BASELINE: no-secrets: literal token"},
			},
		},
	}
	judge := New(fake, cfg)

	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("baseline P1 gap must be blocking even when BlockOn=[P0]")
	}
	if verdict.Pass {
		t.Error("pass must be false when a blocking gap is present")
	}
}

func TestJudge_NonBaselineP1StillGovernedByConfig(t *testing.T) {
	// Control: a plain (non-baseline) P1 under BlockOn=[P0] remains
	// non-blocking. Guards against ForceBaselineBlocking over-promoting.
	cfg := config.PlanPhaseConfig{
		CoverageThreshold: 60,
		MaxLoops:          3,
		Severity: config.SeverityConfig{
			BlockOn: []string{"P0"},
		},
	}
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP1, Description: "missing validation"},
			},
		},
	}
	judge := New(fake, cfg)

	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Gaps[0].Blocking {
		t.Error("non-baseline P1 should stay non-blocking under BlockOn=[P0]")
	}
}

func defaultCfg() config.PlanPhaseConfig {
	return config.PlanPhaseConfig{
		CoverageThreshold: 80,
		MaxLoops:          3,
		Severity: config.SeverityConfig{
			BlockOn:  []string{"P0", "P1"},
			AssumeOn: []string{"P2"},
			DeferOn:  []string{"P3"},
		},
	}
}

func TestJudge_Pass_NoGapsScoresFull(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "build a REST API", "1. Create handlers\n2. Add routes\n3. Write tests", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true for 0 gaps (score 100)")
	}
	if verdict.Score != 100 {
		t.Errorf("expected score 100 for 0 gaps, got %f", verdict.Score)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	if fake.Calls[0].ToolName != verdictschema.ToolName {
		t.Errorf("expected tool name %q, got %q", verdictschema.ToolName, fake.Calls[0].ToolName)
	}
	if fake.Calls[0].System == "" {
		t.Error("expected system prompt to be set")
	}
}

func TestJudge_Fail_BlockingGapsDriveScoreDown(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "no error handling"},
				{Severity: state.SeverityP1, Description: "missing auth"},
			},
			Questions: []state.Question{
				{Text: "What database?", Priority: "high"},
			},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "build a REST API", "1. Create handlers", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// P0 (-40) + P1 (-20) = 40.
	if verdict.Score != 40 {
		t.Errorf("expected deterministic score 40, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false with blocking gaps")
	}
	if !verdict.Gaps[0].Blocking || !verdict.Gaps[1].Blocking {
		t.Error("expected P0 and P1 gaps to be blocking")
	}
	if len(verdict.Questions) != 1 {
		t.Errorf("expected 1 question, got %d", len(verdict.Questions))
	}
}

func TestJudge_BlockingIgnoresLLMOpinion(t *testing.T) {
	// LLM returns stray Blocking flags — judge recomputes from severity.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "critical gap", Blocking: false},
				{Severity: state.SeverityP2, Description: "ambiguous gap", Blocking: true},
			},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Gaps[0].Blocking {
		t.Error("expected P0 gap to be forced blocking by config")
	}
	if verdict.Gaps[1].Blocking {
		t.Error("expected P2 gap to be forced non-blocking by config")
	}
}

func TestJudge_AccumulatedP2sDragScoreBelowThreshold(t *testing.T) {
	// Three P2 gaps: 100 - 3*10 = 70, below threshold 80.
	// P2 is non-blocking, so pass is gated purely on the score.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "a"},
				{Severity: state.SeverityP2, Description: "b"},
				{Severity: state.SeverityP2, Description: "c"},
			},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 70 {
		t.Errorf("expected score 70, got %f", verdict.Score)
	}
	if verdict.Pass {
		t.Error("expected pass=false when accumulated non-blocking gaps drag score below threshold")
	}
}

func TestJudge_PassTrueAtExactThreshold(t *testing.T) {
	// Two P2 gaps: 100 - 20 = 80, exactly equal to threshold.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "a"},
				{Severity: state.SeverityP2, Description: "b"},
			},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Score != 80 {
		t.Errorf("expected score 80, got %f", verdict.Score)
	}
	if !verdict.Pass {
		t.Error("expected pass=true when score equals threshold exactly")
	}
}

func TestJudge_P3GapsDeferred(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP3, Description: "nice to have"},
			},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Gaps[0].Blocking {
		t.Error("expected P3 gap to be non-blocking (deferred)")
	}
	// 100 - 5 = 95, above threshold.
	if !verdict.Pass {
		t.Error("expected pass=true for one P3 gap (score 95)")
	}
}

func TestJudge_ClientError(t *testing.T) {
	fake := &claude.FakeClient{
		Err: fmt.Errorf("connection refused"),
	}

	judge := New(fake, defaultCfg())
	_, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err == nil {
		t.Fatal("expected error when client fails")
	}
}

func TestJudge_CustomSeverityConfig(t *testing.T) {
	// Config where only P0 blocks, P1 is assume-on.
	cfg := config.PlanPhaseConfig{
		CoverageThreshold: 60,
		MaxLoops:          3,
		Severity: config.SeverityConfig{
			BlockOn:  []string{"P0"},
			AssumeOn: []string{"P1", "P2"},
			DeferOn:  []string{"P3"},
		},
	}

	// Only a P1 gap — with custom config it is non-blocking.
	// Score: 100 - 20 = 80, above threshold 60, so pass.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP1, Description: "important"},
			},
		},
	}

	judge := New(fake, cfg)
	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Gaps[0].Blocking {
		t.Error("expected P1 gap to be non-blocking with custom config")
	}
	if !verdict.Pass {
		t.Errorf("expected pass=true (score %f, threshold 60, non-blocking)", verdict.Score)
	}
}

func TestJudge_EmptyGapsAndQuestions(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps:      nil,
			Questions: nil,
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true")
	}
	if verdict.Gaps != nil {
		t.Errorf("expected nil gaps, got %v", verdict.Gaps)
	}
	if verdict.Questions != nil {
		t.Errorf("expected nil questions, got %v", verdict.Questions)
	}
}

func TestJudge_SummaryRoundTrip(t *testing.T) {
	// The judge must preserve the narrative summary the LLM emits so the
	// CLI renderer can show decisions/risks/files under the phase header.
	want := "## Decided\n- Use cobra for CLI\n\n## Risks\n- dependency on gh CLI"
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Summary: want,
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan", nil)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if verdict.Summary != want {
		t.Errorf("summary lost in round-trip\n got: %q\nwant: %q", verdict.Summary, want)
	}
}

func TestJudge_SystemPromptMentionsSummary(t *testing.T) {
	// Regression guard: if the summary instructions get stripped from the
	// system prompt, the renderer silently loses its narrative block.
	if !strings.Contains(systemPrompt, "summary") {
		t.Error("system prompt no longer instructs the LLM to emit a summary field")
	}
	for _, section := range []string{"## Decided", "## Risks", "## Files to touch"} {
		if !strings.Contains(systemPrompt, section) {
			t.Errorf("system prompt missing summary sub-section %q", section)
		}
	}
}

func TestJudge_SystemPromptIncludesFewShotExamples(t *testing.T) {
	// Issue #85 requires at least 2 few-shot examples (one pass, one fail)
	// to keep the model's output stable across runs.
	examples := []string{"Example 1", "Example 2", "submit_verdict"}
	for _, needle := range examples {
		if !strings.Contains(systemPrompt, needle) {
			t.Errorf("system prompt missing few-shot anchor %q", needle)
		}
	}
}

func TestJudge_AcknowledgedAssumptionsInPrompt(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{},
		},
	}

	judge := New(fake, defaultCfg())
	assumptions := []state.Assumption{
		{Description: "database choice unclear", Severity: state.SeverityP2, Phase: state.PhasePlan},
		{Description: "caching strategy TBD", Severity: state.SeverityP2, Phase: state.PhasePlan},
	}

	_, err := judge.Judge(context.Background(), "intent", "plan", assumptions)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(fake.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(fake.Calls))
	}
	prompt := fake.Calls[0].Prompt
	if !strings.Contains(prompt, "Acknowledged Assumptions") {
		t.Error("expected prompt to contain Acknowledged Assumptions section")
	}
	if !strings.Contains(prompt, "database choice unclear") {
		t.Error("expected prompt to contain first assumption")
	}
	if !strings.Contains(prompt, "caching strategy TBD") {
		t.Error("expected prompt to contain second assumption")
	}
	if !strings.Contains(prompt, "do not re-flag") {
		t.Error("expected prompt to instruct judge not to re-flag")
	}
}

func TestJudge_ReflaggedGapHalvedPenalty(t *testing.T) {
	// When the judge re-flags an already-acknowledged P2 gap, its
	// penalty should be halved (5 instead of 10).
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Gaps: []state.Gap{
				{Severity: state.SeverityP2, Description: "database choice unclear"},
				{Severity: state.SeverityP2, Description: "new naming concern"},
			},
		},
	}

	judge := New(fake, defaultCfg())
	acknowledged := []state.Assumption{
		{Description: "database choice unclear", Severity: state.SeverityP2},
	}

	verdict, err := judge.Judge(context.Background(), "intent", "plan", acknowledged)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// 100 - 5 (halved re-flag) - 10 (new) = 85
	if verdict.Score != 85 {
		t.Errorf("expected score 85 (halved re-flag penalty), got %f", verdict.Score)
	}
}
