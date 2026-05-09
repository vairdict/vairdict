package verdictschema

import "github.com/vairdict/vairdict/internal/state"

// MergeChecklistAudit combines the source AC list (parsed from the
// issue body) with the judge's per-item ticks into the final
// Checklist that lands on state.Verdict.
//
// The audit slice carries the model's per-item response. Only the
// Name, Passed, and Reason fields on each audit item matter — the
// schema (schema.go) constrains the model to those three properties.
// Description and Required come from the source; the judge cannot
// rename items, mark them optional, or invent new ones.
//
// Rules:
//
//   - Source list is the source of truth for Description, Required,
//     and Name.
//   - When an audit entry matches a source Name, the source's
//     Passed and Reason are replaced with the audit's values.
//   - When a source item has no matching audit entry, it stays as
//     the source had it — Passed=false from the parser yields
//     empty Reason which (per the gate) blocks the verdict. This is
//     the "judge cannot quietly skip an item" rule.
//   - Pre-checked source items (Passed=true at parse time) are
//     preserved; they were a human assertion and we don't undo
//     that. The judge can still tick them in the audit (replacing
//     Reason with evidence) — the merge prefers audit when
//     present.
//   - Audit entries with Names not present in the source list are
//     dropped. The judge cannot extend the contract.
//   - nil source returns nil regardless of the audit. Legacy mode
//     where no AC list was provided.
func MergeChecklistAudit(source, audit []state.ChecklistItem) []state.ChecklistItem {
	if source == nil {
		return nil
	}
	byName := make(map[string]state.ChecklistItem, len(audit))
	for _, a := range audit {
		byName[a.Name] = a
	}
	out := make([]state.ChecklistItem, len(source))
	for i, s := range source {
		out[i] = s
		a, ok := byName[s.Name]
		if !ok {
			continue
		}
		out[i].Passed = a.Passed
		out[i].Reason = a.Reason
	}
	return out
}
