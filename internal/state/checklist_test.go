package state

import "testing"

// TestChecklistItem_Required is the simplest contract on the new type:
// each item carries a stable Name (the gate looks it up by name), an
// optional Description (rendered in the verdict comment), a Passed
// flag the judge ticks, and a Required flag the post-processor uses
// to decide whether an unticked item fails the gate. Optional items
// (e.g. nice-to-have observations) leave Required false so they show
// in the checklist but never fail the verdict.
func TestChecklistItem_Required(t *testing.T) {
	got := ChecklistItem{
		Name:        "tests_cover_change",
		Description: "Tests added for the new behavior",
		Required:    true,
		Passed:      true,
	}
	if got.Name != "tests_cover_change" {
		t.Errorf("Name = %q", got.Name)
	}
	if !got.Required || !got.Passed {
		t.Errorf("flags wrong: %+v", got)
	}
}

// TestChecklistTally counts passed vs total. Total counts every item
// (required and optional) so the verdict comment can show "8/10
// checks" honestly. The gate uses RequiredPassed (separately tested)
// so an unticked optional item doesn't fail the verdict.
func TestChecklistTally(t *testing.T) {
	cases := []struct {
		name              string
		items             []ChecklistItem
		wantPassed, total int
	}{
		{"empty", nil, 0, 0},
		{"all_pass", []ChecklistItem{
			{Name: "a", Required: true, Passed: true},
			{Name: "b", Required: true, Passed: true},
		}, 2, 2},
		{"one_unticked", []ChecklistItem{
			{Name: "a", Required: true, Passed: true},
			{Name: "b", Required: true, Passed: false},
		}, 1, 2},
		{"optional_unticked_still_counts_in_total", []ChecklistItem{
			{Name: "a", Required: true, Passed: true},
			{Name: "b", Required: false, Passed: false},
		}, 1, 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			passed, total := ChecklistTally(c.items)
			if passed != c.wantPassed || total != c.total {
				t.Errorf("ChecklistTally = (%d, %d), want (%d, %d)",
					passed, total, c.wantPassed, c.total)
			}
		})
	}
}

// TestRequiredPassed reports whether every Required item in the
// checklist is "satisfied" — either Passed=true, or unpassed with a
// non-empty Reason explaining the deferral. An unpassed Required
// item with no Reason fails the check; that's the forcing-function
// for the judge to either complete the AC item or write down why
// it isn't being completed.
func TestRequiredPassed(t *testing.T) {
	cases := []struct {
		name  string
		items []ChecklistItem
		want  bool
	}{
		{"empty_passes", nil, true},
		{"all_required_pass", []ChecklistItem{
			{Name: "a", Required: true, Passed: true},
			{Name: "b", Required: true, Passed: true},
		}, true},
		{"required_unticked_no_reason_fails", []ChecklistItem{
			{Name: "a", Required: true, Passed: true},
			{Name: "b", Required: true, Passed: false},
		}, false},
		{"required_unticked_with_reason_passes", []ChecklistItem{
			{Name: "a", Required: true, Passed: true},
			{Name: "b", Required: true, Passed: false, Reason: "blocked on #130"},
		}, true},
		{"required_unticked_whitespace_only_reason_fails", []ChecklistItem{
			{Name: "a", Required: true, Passed: false, Reason: "   "},
		}, false},
		{"optional_unticked_still_passes", []ChecklistItem{
			{Name: "a", Required: true, Passed: true},
			{Name: "b", Required: false, Passed: false},
		}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := RequiredPassed(c.items); got != c.want {
				t.Errorf("RequiredPassed = %v, want %v", got, c.want)
			}
		})
	}
}

// TestVerdictState constants — PASS and NEEDS_WORK are the only two
// states the new gate emits. No "in-between" so the verdict doesn't
// pretend a precision the judge can't honestly produce.
func TestVerdictState_Values(t *testing.T) {
	if string(VerdictPass) != "PASS" {
		t.Errorf("VerdictPass = %q, want PASS", VerdictPass)
	}
	if string(VerdictNeedsWork) != "NEEDS_WORK" {
		t.Errorf("VerdictNeedsWork = %q, want NEEDS_WORK", VerdictNeedsWork)
	}
}

// TestDeriveVerdictState pins the new gate logic. PASS requires:
//
//   - zero gaps marked Blocking (any tier promoted to blocking — Critical,
//     High, or a Standards finding for a "block"-state rule)
//   - every Required checklist item Passed
//
// Anything else is NEEDS_WORK. The score is no longer a gate input.
func TestDeriveVerdictState(t *testing.T) {
	cases := []struct {
		name      string
		gaps      []Gap
		checklist []ChecklistItem
		want      VerdictState
	}{
		{
			"empty_gaps_empty_checklist_passes",
			nil, nil, VerdictPass,
		},
		{
			"all_required_passed_no_blocking_gaps",
			[]Gap{{Severity: SeverityLow}},
			[]ChecklistItem{{Required: true, Passed: true}},
			VerdictPass,
		},
		{
			"blocking_gap_fails_even_with_full_checklist",
			[]Gap{{Severity: SeverityCritical, Blocking: true}},
			[]ChecklistItem{{Required: true, Passed: true}},
			VerdictNeedsWork,
		},
		{
			"unticked_required_item_fails_even_with_no_gaps",
			nil,
			[]ChecklistItem{{Required: true, Passed: false}},
			VerdictNeedsWork,
		},
		{
			"deferred_required_with_reason_does_not_fail",
			nil,
			[]ChecklistItem{{Required: true, Passed: false, Reason: "blocked on #130"}},
			VerdictPass,
		},
		{
			"unticked_optional_does_not_fail",
			nil,
			[]ChecklistItem{{Required: false, Passed: false}},
			VerdictPass,
		},
		{
			"non_blocking_high_does_not_fail_alone",
			[]Gap{{Severity: SeverityHigh, Blocking: false}},
			nil,
			VerdictPass,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := DeriveVerdictState(c.gaps, c.checklist); got != c.want {
				t.Errorf("DeriveVerdictState = %q, want %q", got, c.want)
			}
		})
	}
}
