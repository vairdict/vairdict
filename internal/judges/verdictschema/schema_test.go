package verdictschema

import (
	"encoding/json"
	"testing"

	"github.com/vairdict/vairdict/internal/state"
)

func TestComputeScore(t *testing.T) {
	tests := []struct {
		name string
		gaps []state.Gap
		want float64
	}{
		{"empty", nil, 100},
		{"one_p3", []state.Gap{{Severity: state.SeverityP3}}, 95},
		{"one_p2", []state.Gap{{Severity: state.SeverityP2}}, 90},
		{"one_p1", []state.Gap{{Severity: state.SeverityP1}}, 80},
		{"one_p0", []state.Gap{{Severity: state.SeverityP0}}, 60},
		{"mixed", []state.Gap{
			{Severity: state.SeverityP1},
			{Severity: state.SeverityP2},
			{Severity: state.SeverityP3},
		}, 65},
		{"floors_at_zero", []state.Gap{
			{Severity: state.SeverityP0},
			{Severity: state.SeverityP0},
			{Severity: state.SeverityP0},
		}, 0},
		{"unknown_severity_ignored", []state.Gap{{Severity: "PX"}}, 100},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := ComputeScore(tt.gaps); got != tt.want {
				t.Errorf("ComputeScore(%v) = %v, want %v", tt.gaps, got, tt.want)
			}
		})
	}
}

func TestApplyBlocking_DefaultSet(t *testing.T) {
	gaps := []state.Gap{
		{Severity: state.SeverityP0},
		{Severity: state.SeverityP1},
		{Severity: state.SeverityP2},
		{Severity: state.SeverityP3},
	}

	ApplyBlocking(gaps, nil)

	wants := []bool{true, true, false, false}
	for i, want := range wants {
		if gaps[i].Blocking != want {
			t.Errorf("gap %d (%s): Blocking = %v, want %v",
				i, gaps[i].Severity, gaps[i].Blocking, want)
		}
	}
}

func TestApplyBlocking_CustomSet(t *testing.T) {
	gaps := []state.Gap{
		{Severity: state.SeverityP0, Blocking: false},
		{Severity: state.SeverityP1, Blocking: true}, // LLM opinion — must be overridden
	}

	// Only P0 blocks in this config.
	ApplyBlocking(gaps, map[string]bool{"P0": true})

	if !gaps[0].Blocking {
		t.Error("P0 should block under custom set {P0:true}")
	}
	if gaps[1].Blocking {
		t.Error("P1 should NOT block under custom set {P0:true} — LLM opinion overridden")
	}
}

func TestApplyBlocking_EmptySetBlocksNothing(t *testing.T) {
	// Explicit empty map must mean "nothing is blocking" — not "fall back
	// to default".
	gaps := []state.Gap{
		{Severity: state.SeverityP0},
		{Severity: state.SeverityP1},
	}
	ApplyBlocking(gaps, map[string]bool{})

	for _, g := range gaps {
		if g.Blocking {
			t.Errorf("expected %s non-blocking under empty set", g.Severity)
		}
	}
}

func TestHasBlockingGap(t *testing.T) {
	if HasBlockingGap(nil) {
		t.Error("nil gaps should not be blocking")
	}
	if HasBlockingGap([]state.Gap{{Blocking: false}}) {
		t.Error("no blocking gaps should return false")
	}
	if !HasBlockingGap([]state.Gap{{Blocking: false}, {Blocking: true}}) {
		t.Error("a single blocking gap should return true")
	}
}

func TestIsReflagged(t *testing.T) {
	ack := []state.Assumption{
		{Description: "database choice unclear", Severity: state.SeverityP2},
		{Description: "caching strategy not defined", Severity: state.SeverityP2},
	}

	tests := []struct {
		name string
		gap  state.Gap
		want bool
	}{
		{"exact match", state.Gap{Description: "database choice unclear"}, true},
		{"gap contains assumption", state.Gap{Description: "the database choice unclear — still ambiguous"}, true},
		{"assumption contains gap", state.Gap{Description: "caching strategy"}, true},
		{"case insensitive", state.Gap{Description: "Database Choice Unclear"}, true},
		{"no match", state.Gap{Description: "missing error handling"}, false},
		{"empty description", state.Gap{Description: ""}, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := IsReflagged(tt.gap, ack); got != tt.want {
				t.Errorf("IsReflagged(%q) = %v, want %v", tt.gap.Description, got, tt.want)
			}
		})
	}

	// Empty acknowledged list should never match.
	if IsReflagged(state.Gap{Description: "anything"}, nil) {
		t.Error("expected no match with nil acknowledged list")
	}
}

func TestComputeScoreWithAcknowledged_HalvesPenalty(t *testing.T) {
	ack := []state.Assumption{
		{Description: "database choice unclear", Severity: state.SeverityP2},
	}
	gaps := []state.Gap{
		{Severity: state.SeverityP2, Description: "database choice unclear"},  // re-flagged: 10/2 = 5
		{Severity: state.SeverityP2, Description: "new concern about naming"}, // fresh: 10
	}

	got := ComputeScoreWithAcknowledged(gaps, ack)
	// 100 - 5 - 10 = 85
	if got != 85 {
		t.Errorf("expected 85, got %f", got)
	}
}

func TestComputeScoreWithAcknowledged_NoAcknowledged(t *testing.T) {
	gaps := []state.Gap{
		{Severity: state.SeverityP2, Description: "a"},
		{Severity: state.SeverityP2, Description: "b"},
	}

	withAck := ComputeScoreWithAcknowledged(gaps, nil)
	plain := ComputeScore(gaps)
	if withAck != plain {
		t.Errorf("expected identical to ComputeScore (%f), got %f", plain, withAck)
	}
}

func TestVerdictTool_SchemaIsValidJSON(t *testing.T) {
	tool := VerdictTool("test")
	if tool.Name != ToolName {
		t.Errorf("expected tool name %q, got %q", ToolName, tool.Name)
	}
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("schema is not valid JSON: %v", err)
	}
	if schema["type"] != "object" {
		t.Errorf("schema must be an object, got %v", schema["type"])
	}
	// The schema must declare gaps, questions, summary required.
	required, ok := schema["required"].([]any)
	if !ok {
		t.Fatalf("required field missing or wrong type")
	}
	want := map[string]bool{"summary": true, "gaps": true, "questions": true}
	for _, r := range required {
		if s, ok := r.(string); ok {
			delete(want, s)
		}
	}
	if len(want) != 0 {
		t.Errorf("schema missing required fields: %v", want)
	}
}
