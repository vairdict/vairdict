package github

import (
	"reflect"
	"testing"

	"github.com/vairdict/vairdict/internal/state"
)

// TestParseChecklist_Empty: an empty body yields no items. The judge
// then runs in legacy mode (score-based pass) — the AC gate is
// opt-in via a checklist actually being present.
func TestParseChecklist_Empty(t *testing.T) {
	if got := ParseChecklist(""); len(got) != 0 {
		t.Errorf("empty body: got %d items, want 0", len(got))
	}
}

// TestParseChecklist_NoChecklist: prose-only issues yield no items.
// We deliberately do not attempt LLM-driven extraction from prose
// here; that's a separate follow-up.
func TestParseChecklist_NoChecklist(t *testing.T) {
	body := `## Intent

Add a thing that does the thing. No checklist here.`
	if got := ParseChecklist(body); len(got) != 0 {
		t.Errorf("prose-only body: got %d items, want 0", len(got))
	}
}

// TestParseChecklist_BasicUnchecked: a single `- [ ]` item is parsed
// with Required=true, Passed=false, Description verbatim.
func TestParseChecklist_BasicUnchecked(t *testing.T) {
	body := `## Acceptance Criteria

- [ ] Add a Codex completer
`
	got := ParseChecklist(body)
	want := []state.ChecklistItem{
		{Name: "ac_1", Description: "Add a Codex completer", Required: true, Passed: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestParseChecklist_BasicChecked: `- [x]` and `- [X]` both parse as
// Passed=true. Pre-checked items are honored; the judge can still
// scrutinise them and add Reason as evidence.
func TestParseChecklist_BasicChecked(t *testing.T) {
	body := `- [x] lower x done
- [X] upper X done`
	got := ParseChecklist(body)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2: %+v", len(got), got)
	}
	for i, it := range got {
		if !it.Passed {
			t.Errorf("item %d not Passed: %+v", i, it)
		}
		if !it.Required {
			t.Errorf("item %d not Required: %+v", i, it)
		}
	}
}

// TestParseChecklist_Mixed: a typical AC list with both checked and
// unchecked items round-trips cleanly. Names are stable (ac_1..n in
// document order).
func TestParseChecklist_Mixed(t *testing.T) {
	body := `## Acceptance Criteria

- [x] Item one
- [ ] Item two
- [ ] Item three
`
	got := ParseChecklist(body)
	want := []state.ChecklistItem{
		{Name: "ac_1", Description: "Item one", Required: true, Passed: true},
		{Name: "ac_2", Description: "Item two", Required: true, Passed: false},
		{Name: "ac_3", Description: "Item three", Required: true, Passed: false},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("got %+v, want %+v", got, want)
	}
}

// TestParseChecklist_AlternateMarkers: GitHub-flavored markdown
// accepts `*`, `+`, `-` as bullet markers. We mirror that. Numbered
// lists are out of scope (rare in AC checklists).
func TestParseChecklist_AlternateMarkers(t *testing.T) {
	body := `- [ ] dash
* [ ] star
+ [ ] plus`
	got := ParseChecklist(body)
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(got), got)
	}
	wantDesc := []string{"dash", "star", "plus"}
	for i, it := range got {
		if it.Description != wantDesc[i] {
			t.Errorf("item %d Description = %q, want %q", i, it.Description, wantDesc[i])
		}
	}
}

// TestParseChecklist_IndentedItems: indented checklists are still
// parsed. Indentation in AC lists is unusual but not invalid.
func TestParseChecklist_IndentedItems(t *testing.T) {
	body := "- [ ] top\n  - [ ] indented\n    - [ ] deeper"
	got := ParseChecklist(body)
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(got), got)
	}
}

// TestParseChecklist_PreservesMarkdownInDescription: bold, code, and
// links inside an AC item's description are preserved verbatim. The
// judge gets the original text; the renderer can format it.
func TestParseChecklist_PreservesMarkdownInDescription(t *testing.T) {
	body := "- [ ] Add `Client.Foo()` in **internal/agents/codex** per [docs](https://example.com)"
	got := ParseChecklist(body)
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1", len(got))
	}
	want := "Add `Client.Foo()` in **internal/agents/codex** per [docs](https://example.com)"
	if got[0].Description != want {
		t.Errorf("Description = %q, want %q", got[0].Description, want)
	}
}

// TestParseChecklist_FullIssueBody: the parser pulls only checkbox
// lines out of a real-shaped issue body — section headers, prose,
// notes, etc. are ignored.
func TestParseChecklist_FullIssueBody(t *testing.T) {
	body := `## Intent

Add a Codex completer satisfying the completer interface.

## Acceptance Criteria

- [ ] New package internal/agents/codex
- [ ] Shells out to the codex binary
- [ ] IsAvailable() helper

## Notes

This depends on #130 landing for the registry registration.`
	got := ParseChecklist(body)
	if len(got) != 3 {
		t.Fatalf("got %d items, want 3: %+v", len(got), got)
	}
	wantDesc := []string{
		"New package internal/agents/codex",
		"Shells out to the codex binary",
		"IsAvailable() helper",
	}
	for i, it := range got {
		if it.Description != wantDesc[i] {
			t.Errorf("item %d Description = %q, want %q", i, it.Description, wantDesc[i])
		}
	}
}

// TestParseChecklist_StableNames: items get stable, ordered names
// (ac_1, ac_2, ...) so the judge can echo them verbatim and
// post-processing can match. Names are 1-indexed because they
// appear in user-facing rendering.
func TestParseChecklist_StableNames(t *testing.T) {
	body := "- [ ] one\n- [ ] two\n- [ ] three"
	got := ParseChecklist(body)
	wantNames := []string{"ac_1", "ac_2", "ac_3"}
	for i, it := range got {
		if it.Name != wantNames[i] {
			t.Errorf("item %d Name = %q, want %q", i, it.Name, wantNames[i])
		}
	}
}

// TestParseChecklist_TrimsTrailingWhitespace: trailing spaces and
// CRs in a description don't leak into the parsed item.
func TestParseChecklist_TrimsTrailingWhitespace(t *testing.T) {
	body := "- [ ] item with trailing spaces   \r\n- [x] another\t  \n"
	got := ParseChecklist(body)
	if len(got) != 2 {
		t.Fatalf("got %d items, want 2", len(got))
	}
	if got[0].Description != "item with trailing spaces" {
		t.Errorf("item 0 Description = %q (not trimmed)", got[0].Description)
	}
	if got[1].Description != "another" {
		t.Errorf("item 1 Description = %q (not trimmed)", got[1].Description)
	}
}

// TestParseChecklist_IgnoresNonCheckboxBrackets: lines that have
// brackets but aren't checkbox-shaped (e.g. just bullets) must not
// be parsed as AC items.
func TestParseChecklist_IgnoresNonCheckboxBrackets(t *testing.T) {
	body := `- normal bullet
- [stuff] in brackets but no space-or-x
- [ ] real ac
- [other] still not a checkbox`
	got := ParseChecklist(body)
	if len(got) != 1 {
		t.Fatalf("got %d items, want 1: %+v", len(got), got)
	}
	if got[0].Description != "real ac" {
		t.Errorf("Description = %q, want 'real ac'", got[0].Description)
	}
}
