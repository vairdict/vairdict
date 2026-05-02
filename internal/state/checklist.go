package state

// VerdictState is the binary outcome of the new pass gate. The score
// number that judges historically returned is now decorative — the
// gate is mechanical, derived from the checklist + blocking gaps.
type VerdictState string

const (
	// VerdictPass means every Required checklist item is Passed and
	// no Blocking gap was emitted. The PR is good to merge as far as
	// the judge is concerned.
	VerdictPass VerdictState = "PASS"
	// VerdictNeedsWork means at least one Required item is unticked,
	// or at least one Blocking gap was emitted. The PR author must
	// either resolve the gap or address the unticked item before the
	// next review round.
	VerdictNeedsWork VerdictState = "NEEDS_WORK"
)

// ChecklistItem is one observable check the judge ticks (Passed=true)
// or leaves unticked (Passed=false). The Name is a stable key so the
// gate and the renderer can refer to specific checks; the Description
// is the human-readable label that ends up in the verdict comment.
//
// Required items participate in the pass gate: an unticked Required
// item flips the verdict to NEEDS_WORK. Optional items show up in the
// checklist for transparency but don't fail the verdict — useful for
// "tests cover the change" style observations that aren't appropriate
// gates for every change but are still worth recording.
type ChecklistItem struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Required    bool   `json:"required"`
	Passed      bool   `json:"passed"`
}

// ChecklistTally returns the (passed, total) counts across every
// item in the list. Used by the verdict renderer to surface "8/10
// checks passed" — descriptive, not a gate.
func ChecklistTally(items []ChecklistItem) (passed, total int) {
	for _, it := range items {
		total++
		if it.Passed {
			passed++
		}
	}
	return passed, total
}

// RequiredPassed reports whether every Required item in the checklist
// is Passed. Optional items are ignored. Half of the new pass gate;
// the other half is "zero blocking gaps" (see DeriveVerdictState).
func RequiredPassed(items []ChecklistItem) bool {
	for _, it := range items {
		if it.Required && !it.Passed {
			return false
		}
	}
	return true
}

// DeriveVerdictState computes the new binary gate from the gap list
// and the checklist. PASS requires both:
//
//   - zero Blocking gaps (any tier promoted to blocking — Critical,
//     High, or a Standards finding whose rule is in the "block" state)
//   - every Required checklist item Passed
//
// Anything else is NEEDS_WORK. The score number is no longer an
// input to the gate; it's a decorative tally produced by
// ChecklistTally for skim-readability in the PR comment.
func DeriveVerdictState(gaps []Gap, checklist []ChecklistItem) VerdictState {
	for _, g := range gaps {
		if g.Blocking {
			return VerdictNeedsWork
		}
	}
	if !RequiredPassed(checklist) {
		return VerdictNeedsWork
	}
	return VerdictPass
}
