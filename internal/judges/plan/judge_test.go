package plan

import (
	"context"
	"fmt"
	"testing"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/config"
	"github.com/vairdict/vairdict/internal/state"
)

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

func TestJudge_Pass(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 90,
			Pass:  true,
			Gaps:  []state.Gap{},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "build a REST API", "1. Create handlers\n2. Add routes\n3. Write tests")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true for score 90 with threshold 80")
	}
	if verdict.Score != 90 {
		t.Errorf("expected score 90, got %f", verdict.Score)
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
}

func TestJudge_Fail(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 50,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "no error handling", Blocking: true},
				{Severity: state.SeverityP1, Description: "missing auth", Blocking: true},
			},
			Questions: []state.Question{
				{Text: "What database?", Priority: "high"},
			},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "build a REST API", "1. Create handlers")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Pass {
		t.Error("expected pass=false for score 50 with threshold 80")
	}
	if verdict.Score != 50 {
		t.Errorf("expected score 50, got %f", verdict.Score)
	}
	if len(verdict.Gaps) != 2 {
		t.Fatalf("expected 2 gaps, got %d", len(verdict.Gaps))
	}
	if !verdict.Gaps[0].Blocking {
		t.Error("expected P0 gap to be blocking")
	}
	if !verdict.Gaps[1].Blocking {
		t.Error("expected P1 gap to be blocking")
	}
	if len(verdict.Questions) != 1 {
		t.Errorf("expected 1 question, got %d", len(verdict.Questions))
	}
}

func TestJudge_BlockingEnforcedByConfig(t *testing.T) {
	// LLM returns P0 as non-blocking and P2 as blocking — judge overrides both.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 75,
			Pass:  false,
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "critical gap", Blocking: false},
				{Severity: state.SeverityP2, Description: "ambiguous gap", Blocking: true},
			},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan")
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

func TestJudge_PassEnforcedByConfig(t *testing.T) {
	// LLM says pass=true but score is below threshold.
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 70,
			Pass:  true,
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Pass {
		t.Error("expected pass=false when score 70 < threshold 80, regardless of LLM opinion")
	}
}

func TestJudge_PassTrueAtExactThreshold(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 80,
			Pass:  false,
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Pass {
		t.Error("expected pass=true when score equals threshold exactly")
	}
}

func TestJudge_P3GapsDeferred(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 85,
			Gaps: []state.Gap{
				{Severity: state.SeverityP3, Description: "nice to have", Blocking: true},
			},
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if verdict.Gaps[0].Blocking {
		t.Error("expected P3 gap to be non-blocking (deferred)")
	}
	if !verdict.Pass {
		t.Error("expected pass=true for score 85 with threshold 80")
	}
}

func TestJudge_ClientError(t *testing.T) {
	fake := &claude.FakeClient{
		Err: fmt.Errorf("connection refused"),
	}

	judge := New(fake, defaultCfg())
	_, err := judge.Judge(context.Background(), "intent", "plan")
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

	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score: 65,
			Gaps: []state.Gap{
				{Severity: state.SeverityP0, Description: "critical", Blocking: true},
				{Severity: state.SeverityP1, Description: "important", Blocking: true},
			},
		},
	}

	judge := New(fake, cfg)
	verdict, err := judge.Judge(context.Background(), "intent", "plan")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if !verdict.Gaps[0].Blocking {
		t.Error("expected P0 gap to be blocking")
	}
	if verdict.Gaps[1].Blocking {
		t.Error("expected P1 gap to be non-blocking with custom config")
	}
	if !verdict.Pass {
		t.Error("expected pass=true for score 65 with threshold 60")
	}
}

func TestJudge_EmptyGapsAndQuestions(t *testing.T) {
	fake := &claude.FakeClient{
		Response: state.Verdict{
			Score:     95,
			Pass:      true,
			Gaps:      nil,
			Questions: nil,
		},
	}

	judge := New(fake, defaultCfg())
	verdict, err := judge.Judge(context.Background(), "intent", "plan")
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
