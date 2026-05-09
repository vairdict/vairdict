package verdictschema

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/state"
)

// TestVerdictTool_SchemaIncludesChecklist: the submit_verdict tool's
// JSON Schema must declare a `checklist` array so the model knows it
// can (and must, when items are present) emit per-AC ticks. The
// schema is the contract — if it's missing, the model has no
// affordance to populate the field.
func TestVerdictTool_SchemaIncludesChecklist(t *testing.T) {
	tool := VerdictTool("test")
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal schema: %v", err)
	}
	props, ok := schema["properties"].(map[string]any)
	if !ok {
		t.Fatalf("schema has no properties: %v", schema)
	}
	cl, ok := props["checklist"]
	if !ok {
		t.Fatalf("schema missing checklist property: %v", props)
	}
	clMap, ok := cl.(map[string]any)
	if !ok {
		t.Fatalf("checklist is not an object: %T", cl)
	}
	if clMap["type"] != "array" {
		t.Errorf("checklist type = %v, want array", clMap["type"])
	}
	// Per-item shape must include name (id), passed (bool), reason (string).
	items, ok := clMap["items"].(map[string]any)
	if !ok {
		t.Fatalf("checklist items not an object: %v", clMap)
	}
	itemProps, ok := items["properties"].(map[string]any)
	if !ok {
		t.Fatalf("checklist item missing properties: %v", items)
	}
	for _, want := range []string{"name", "passed", "reason"} {
		if _, ok := itemProps[want]; !ok {
			t.Errorf("checklist item missing %q field: %v", want, itemProps)
		}
	}
}

// TestMergeChecklistAudit_HappyPath: source ACs from the issue plus
// the judge's per-item ticks merge into a final Checklist that
// preserves Description/Required/Name from the source and applies
// the judge's Passed/Reason verdict.
func TestMergeChecklistAudit_HappyPath(t *testing.T) {
	source := []state.ChecklistItem{
		{Name: "ac_1", Description: "Add codex package", Required: true, Passed: false},
		{Name: "ac_2", Description: "Wire into resolver", Required: true, Passed: false},
	}
	audit := []state.ChecklistItem{
		{Name: "ac_1", Passed: true, Reason: "internal/agents/codex/client.go:127"},
		{Name: "ac_2", Passed: false, Reason: "deferred: depends on #130"},
	}
	got := MergeChecklistAudit(source, audit)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].Name != "ac_1" || got[0].Description != "Add codex package" || !got[0].Passed {
		t.Errorf("ac_1 wrong: %+v", got[0])
	}
	if got[0].Reason != "internal/agents/codex/client.go:127" {
		t.Errorf("ac_1 Reason = %q", got[0].Reason)
	}
	if got[1].Name != "ac_2" || got[1].Description != "Wire into resolver" || got[1].Passed {
		t.Errorf("ac_2 wrong: %+v", got[1])
	}
	if got[1].Reason != "deferred: depends on #130" {
		t.Errorf("ac_2 Reason = %q", got[1].Reason)
	}
}

// TestMergeChecklistAudit_MissingAuditEntry: if the judge fails to
// emit an audit entry for a source item, the merged item keeps
// Passed=false from the source AND gets an empty Reason so the gate
// blocks. The judge cannot quietly skip an item by omission.
func TestMergeChecklistAudit_MissingAuditEntry(t *testing.T) {
	source := []state.ChecklistItem{
		{Name: "ac_1", Description: "first", Required: true, Passed: false},
		{Name: "ac_2", Description: "second", Required: true, Passed: false},
	}
	audit := []state.ChecklistItem{
		{Name: "ac_1", Passed: true, Reason: "done at file:1"},
		// ac_2 omitted
	}
	got := MergeChecklistAudit(source, audit)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2 (one per source)", len(got))
	}
	if got[1].Passed {
		t.Errorf("missing audit must not be Passed: %+v", got[1])
	}
	if got[1].Reason != "" {
		t.Errorf("missing audit must have empty Reason so gate blocks, got: %q", got[1].Reason)
	}
	// Sanity: gate should now refuse to pass.
	if state.RequiredPassed(got) {
		t.Errorf("RequiredPassed should be false when an audit is missing")
	}
}

// TestMergeChecklistAudit_AuditWithUnknownName: an audit entry
// referring to a Name that isn't in the source list is dropped (the
// judge cannot invent items). Existing source items are unaffected.
func TestMergeChecklistAudit_AuditWithUnknownName(t *testing.T) {
	source := []state.ChecklistItem{
		{Name: "ac_1", Description: "real", Required: true, Passed: false},
	}
	audit := []state.ChecklistItem{
		{Name: "ac_1", Passed: true, Reason: "ok"},
		{Name: "ac_999", Passed: true, Reason: "made up"},
	}
	got := MergeChecklistAudit(source, audit)
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1 (only the source item)", len(got))
	}
	if got[0].Name != "ac_1" {
		t.Errorf("got %q, want ac_1", got[0].Name)
	}
}

// TestMergeChecklistAudit_PreservesPrechecked: an issue body that
// already had `- [x]` items keeps Passed=true from the source even
// if the judge didn't emit an audit entry. Pre-checked items are
// human-asserted; we don't undo that.
func TestMergeChecklistAudit_PreservesPrechecked(t *testing.T) {
	source := []state.ChecklistItem{
		{Name: "ac_1", Description: "already done", Required: true, Passed: true},
	}
	got := MergeChecklistAudit(source, nil)
	if !got[0].Passed {
		t.Errorf("pre-checked item must remain Passed: %+v", got[0])
	}
}

// TestMergeChecklistAudit_NilSource: nil source means no AC checklist
// from the issue (legacy mode). Returns nil regardless of audit.
func TestMergeChecklistAudit_NilSource(t *testing.T) {
	if got := MergeChecklistAudit(nil, []state.ChecklistItem{{Name: "ac_1", Passed: true}}); got != nil {
		t.Errorf("nil source should return nil, got: %+v", got)
	}
}

// TestVerdictTool_DescriptionMentionsChecklistContract: the schema's
// description text must tell the model it has to populate the
// checklist with one entry per AC item and explain the
// passed-needs-evidence vs unpassed-needs-reason contract. Without
// this guidance the model emits empty arrays.
func TestVerdictTool_DescriptionMentionsChecklistContract(t *testing.T) {
	tool := VerdictTool("test")
	var schema map[string]any
	if err := json.Unmarshal(tool.InputSchema, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	props := schema["properties"].(map[string]any)
	cl := props["checklist"].(map[string]any)
	desc, _ := cl["description"].(string)
	desc = strings.ToLower(desc)
	for _, want := range []string{"acceptance", "evidence", "reason"} {
		if !strings.Contains(desc, want) {
			t.Errorf("checklist description missing %q context: %q", want, desc)
		}
	}
}
