package github

import (
	"strconv"
	"strings"
)

// DiffPosition maps a file path and line number (in the new file) to the
// position within the unified diff that GitHub's review API expects.
// Position is 1-based and counts lines from the start of the diff hunk
// for that file.
type DiffPosition struct {
	File     string
	Line     int
	Position int
}

// ParseDiffPositions parses a unified diff and builds a lookup table
// mapping (file, new-line-number) to diff position. The position is what
// GitHub's pull request review API expects: the line's offset within the
// diff output for that file, counting from the first @@ line.
func ParseDiffPositions(diff string) map[string]map[int]int {
	result := make(map[string]map[int]int)

	lines := strings.Split(diff, "\n")
	var currentFile string
	var position int // position within the current file's diff (1-based)
	var newLine int  // current line number in the new file

	for _, line := range lines {
		// New file header: diff --git a/foo b/foo
		if strings.HasPrefix(line, "diff --git ") {
			currentFile = parseGitDiffFile(line)
			position = 0
			newLine = 0
			if currentFile != "" {
				result[currentFile] = make(map[int]int)
			}
			continue
		}

		// Skip --- and +++ headers (they don't count as positions)
		if strings.HasPrefix(line, "--- ") || strings.HasPrefix(line, "+++ ") {
			continue
		}

		// Hunk header: @@ -old,count +new,count @@
		if strings.HasPrefix(line, "@@") {
			position++
			newLine = parseHunkNewStart(line)
			continue
		}

		if currentFile == "" {
			continue
		}

		// Context line (no prefix or space prefix): exists in both old and new
		if len(line) == 0 || line[0] == ' ' {
			position++
			if result[currentFile] != nil {
				result[currentFile][newLine] = position
			}
			newLine++
			continue
		}

		// Added line
		if line[0] == '+' {
			position++
			if result[currentFile] != nil {
				result[currentFile][newLine] = position
			}
			newLine++
			continue
		}

		// Removed line (exists in old, not in new)
		if line[0] == '-' {
			position++
			continue
		}

		// Binary file, "\ No newline at end of file", etc — skip
	}

	return result
}

// ResolveDiffPosition looks up the diff position for a given file and line.
// Returns the position (1-based) and true if found, or 0 and false if the
// line is not part of the diff.
func ResolveDiffPosition(positions map[string]map[int]int, file string, line int) (int, bool) {
	filePositions, ok := positions[file]
	if !ok {
		return 0, false
	}
	pos, ok := filePositions[line]
	if !ok {
		return 0, false
	}
	return pos, true
}

// parseGitDiffFile extracts the file path from a "diff --git a/X b/X" line.
// Returns the path without the b/ prefix.
func parseGitDiffFile(line string) string {
	// Format: diff --git a/path/to/file b/path/to/file
	parts := strings.SplitN(line, " b/", 2)
	if len(parts) != 2 {
		return ""
	}
	return parts[1]
}

// parseHunkNewStart extracts the new-file starting line from a hunk header.
// Format: @@ -old,count +new,count @@ optional context
func parseHunkNewStart(line string) int {
	// Find +N or +N,count
	plusIdx := strings.Index(line, "+")
	if plusIdx < 0 {
		return 0
	}
	rest := line[plusIdx+1:]
	// Take until comma or space
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
