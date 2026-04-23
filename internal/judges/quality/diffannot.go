package quality

import (
	"strconv"
	"strings"
)

// annotateDiff walks a unified diff and prefixes every added ('+') line
// with its absolute new-file line number in the form "+L<n>: ". The goal
// is to let the judging model cite file:line anchors by copying the
// number it sees rather than counting lines within a hunk — LLMs
// routinely get that arithmetic wrong, which produces mis-anchored
// inline PR comments.
//
// Context and removed lines are left untouched: the judge anchors gaps
// to new-file lines (the "+ side"), so only added lines need the label.
// The hunk header still provides the starting line number for any edge
// cases the prompt wants to reason about.
//
// The input is expected to be a standard `git diff` unified output; any
// line the parser can't classify is passed through verbatim so a
// malformed diff never prevents the judge from running.
func annotateDiff(diff string) string {
	if diff == "" {
		return diff
	}
	lines := strings.Split(diff, "\n")
	var newLine int
	inHunk := false

	for i, line := range lines {
		switch {
		case strings.HasPrefix(line, "@@"):
			newLine = parseHunkNewStart(line)
			inHunk = newLine > 0
		case !inHunk:
			// File headers ("diff --git", "---", "+++", "index ...") and
			// any pre-hunk content pass through unchanged.
		case strings.HasPrefix(line, "+++"), strings.HasPrefix(line, "---"):
			// File headers can appear between hunks in multi-file diffs;
			// they don't advance the line counter.
		case strings.HasPrefix(line, "+"):
			lines[i] = "+L" + strconv.Itoa(newLine) + ": " + line[1:]
			newLine++
		case strings.HasPrefix(line, "-"):
			// Removed line — no new-file number to attach.
		case strings.HasPrefix(line, " "), line == "":
			// Context line — advances the new-file counter without
			// receiving a label (gaps anchor to added lines).
			newLine++
		}
	}
	return strings.Join(lines, "\n")
}

// parseHunkNewStart extracts the starting new-file line number from a
// unified-diff hunk header: "@@ -<old>,<len> +<new>,<len> @@ ...".
// Returns 0 when the header is malformed so the caller can skip
// annotation for that hunk rather than fabricate line numbers.
func parseHunkNewStart(header string) int {
	plus := strings.Index(header, "+")
	if plus < 0 {
		return 0
	}
	rest := header[plus+1:]
	end := strings.IndexAny(rest, ", @")
	if end < 0 {
		return 0
	}
	n, err := strconv.Atoi(rest[:end])
	if err != nil {
		return 0
	}
	return n
}
