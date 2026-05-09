package github

import (
	"fmt"
	"regexp"
	"strings"

	"github.com/vairdict/vairdict/internal/state"
)

// checklistItemRE matches a GitHub-Flavored-Markdown task list item:
//
//	[indent]<bullet> [<state>] <description>
//
// where bullet is `-`, `*`, or `+`, and state is a single space (or
// empty) for unchecked, or `x`/`X` for checked. The capture groups
// pull out the state char and the description so the parser can
// produce a ChecklistItem without re-walking the line.
//
// We accept zero or one space inside the brackets so a body authored
// with `- []` (no inner space) still parses — GitHub renders it as
// an unchecked box too.
var checklistItemRE = regexp.MustCompile(`^[ \t]*[-*+] \[([ xX])\] (.+?)[ \t\r]*$`)

// ParseChecklist extracts GitHub-Flavored-Markdown task list items
// from a raw issue body. Returns one ChecklistItem per matched line,
// in document order. Items get stable Name keys (ac_1, ac_2, …) so
// the judge can echo them verbatim and post-processing can pair the
// model's response back to the source list.
//
// All parsed items are Required=true. Optional items are reserved
// for judge-internal observations (e.g. "tests cover the change")
// and are not extracted from the issue body.
//
// Lines outside checklist syntax — prose, headers, code blocks,
// non-checkbox bullets — are ignored. Code blocks are NOT
// fence-aware: a fake checklist embedded in a ``` block will be
// extracted. That's a known minor failure mode; AC checklists in
// fenced blocks are a documentation smell anyway.
func ParseChecklist(body string) []state.ChecklistItem {
	if body == "" {
		return nil
	}
	var items []state.ChecklistItem
	idx := 0
	for _, line := range strings.Split(body, "\n") {
		m := checklistItemRE.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		idx++
		state := m[1]
		description := strings.TrimRight(m[2], " \t\r")
		items = append(items, state2Item(idx, state, description))
	}
	return items
}

// state2Item builds a ChecklistItem for one matched line. Factored
// out so the parser body stays a tight loop.
func state2Item(idx int, mark, description string) state.ChecklistItem {
	passed := mark == "x" || mark == "X"
	return state.ChecklistItem{
		Name:        fmt.Sprintf("ac_%d", idx),
		Description: description,
		Required:    true,
		Passed:      passed,
	}
}
