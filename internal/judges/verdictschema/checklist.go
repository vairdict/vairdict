package verdictschema

import (
	"fmt"
	"strings"

	"github.com/vairdict/vairdict/internal/state"
)

// RenderACSection produces the "## Acceptance Criteria" prompt
// fragment listing the AC items the judge must tick, with explicit
// per-item-evidence instructions. Empty when the items slice is
// empty; in that case the judge runs in legacy mode (no AC
// enforcement).
//
// The instructions are deliberately repetitive of the schema's
// `checklist` field description (schema.go) — the prompt repeats
// the contract so the model walks it consciously instead of
// treating it as a side effect of the tool call. The
// negative-space prompt ("which files would I expect to change?")
// is the load-bearing part — without it the model marks items met
// based on plausibility rather than evidence.
//
// Shared between the plan judge (evidence is plan text covering
// each AC) and the quality judge (evidence is file:line in the
// diff). Both judges enforce the same per-item contract.
func RenderACSection(items []state.ChecklistItem) string {
	if len(items) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\n\n## Acceptance Criteria\n\n")
	b.WriteString("For EACH item below, populate one entry in the submit_verdict tool's `checklist` array. Use the exact `name` shown.\n\n")
	b.WriteString("Contract:\n")
	b.WriteString("- `passed=true` requires concrete evidence in `reason` (file:line in the diff for the quality judge; a quote from the plan for the plan judge). If you can't cite the evidence, do not mark it passed.\n")
	b.WriteString("- `passed=false` requires a deferral note in `reason` explaining why this item isn't being completed (e.g. \"blocked on #N\", \"needs upstream X\", \"out of scope per <commit>\"). Empty `reason` on an unpassed item BLOCKS the verdict — the judge cannot quietly skip an AC item.\n\n")
	b.WriteString("Negative-space check, run for each item before deciding `passed`:\n")
	b.WriteString("1. What concrete evidence would I expect to see to satisfy this criterion?\n")
	b.WriteString("2. Is that evidence present?\n")
	b.WriteString("3. If no, this item is NOT done, even if related work is present.\n\n")
	b.WriteString("Items:\n")
	for _, it := range items {
		fmt.Fprintf(&b, "- `%s`: %s\n", it.Name, it.Description)
	}
	return b.String()
}

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
