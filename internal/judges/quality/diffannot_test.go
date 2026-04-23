package quality

import (
	"strings"
	"testing"
)

func TestAnnotateDiff_AddsNewFileLineNumberToAddedLines(t *testing.T) {
	in := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -10,3 +10,4 @@ func Foo() {
 	a := 1
+	b := 2
 	return a + b
+	// trailing comment
`
	out := annotateDiff(in)

	// First '+' line is line 11 (hunk starts at 10, one preceding context line).
	if !strings.Contains(out, "+L11: \tb := 2") {
		t.Errorf("expected '+L11: \\tb := 2' in annotated diff, got:\n%s", out)
	}
	// Second '+' line: after line 11 and context line 12, trailing '+' is 13.
	if !strings.Contains(out, "+L13: \t// trailing comment") {
		t.Errorf("expected '+L13: \\t// trailing comment' in annotated diff, got:\n%s", out)
	}
}

func TestAnnotateDiff_LeavesRemovedAndContextLinesAlone(t *testing.T) {
	in := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -5,3 +5,3 @@
 	context line
-	removed line
+	added line
`
	out := annotateDiff(in)

	// Context line must not be modified at all.
	if !strings.Contains(out, "\n \tcontext line\n") {
		t.Errorf("context line should pass through unchanged, got:\n%s", out)
	}
	// Removed line must keep its '-' prefix and content intact.
	if !strings.Contains(out, "\n-\tremoved line\n") {
		t.Errorf("removed line should pass through unchanged, got:\n%s", out)
	}
	// Added line gets the label.
	if !strings.Contains(out, "+L6: \tadded line") {
		t.Errorf("added line should be labelled +L6, got:\n%s", out)
	}
}

func TestAnnotateDiff_HandlesMultipleHunksPerFile(t *testing.T) {
	// Two hunks in one file; each restarts from its own @@ header.
	in := `diff --git a/foo.go b/foo.go
--- a/foo.go
+++ b/foo.go
@@ -1,2 +1,3 @@
 	a
+	b
@@ -10,2 +11,3 @@
 	x
+	y
`
	out := annotateDiff(in)

	if !strings.Contains(out, "+L2: \tb") {
		t.Errorf("first hunk's added line should be L2, got:\n%s", out)
	}
	if !strings.Contains(out, "+L12: \ty") {
		t.Errorf("second hunk's added line should be L12 (11 + 1 context), got:\n%s", out)
	}
}

func TestAnnotateDiff_HandlesMultipleFiles(t *testing.T) {
	// Annotator should restart counting at every file's first hunk.
	in := `diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,1 +1,2 @@
 	x
+	y
diff --git a/b.go b/b.go
--- a/b.go
+++ b/b.go
@@ -100,1 +100,2 @@
 	p
+	q
`
	out := annotateDiff(in)

	if !strings.Contains(out, "+L2: \ty") {
		t.Errorf("a.go added line should be L2, got:\n%s", out)
	}
	if !strings.Contains(out, "+L101: \tq") {
		t.Errorf("b.go added line should be L101, got:\n%s", out)
	}
}

func TestAnnotateDiff_EmptyAndNonDiffInputsPassThrough(t *testing.T) {
	if got := annotateDiff(""); got != "" {
		t.Errorf("empty input should return empty, got %q", got)
	}
	// Input with no @@ header at all: nothing to annotate, pass through.
	nonDiff := "just some text\nwith a + leading char\nbut no hunks"
	if got := annotateDiff(nonDiff); got != nonDiff {
		t.Errorf("non-diff input should pass through verbatim, got:\n%s", got)
	}
}

func TestAnnotateDiff_MalformedHunkHeaderSkipsAnnotation(t *testing.T) {
	// When parseHunkNewStart returns 0, we must NOT fabricate a label.
	// "@@ no plus side @@" is malformed; the following '+' line stays raw.
	in := "@@ malformed @@\n+content\n"
	out := annotateDiff(in)
	if strings.Contains(out, "+L0:") {
		t.Errorf("malformed hunk must not produce L0 labels, got:\n%s", out)
	}
	if !strings.Contains(out, "+content") {
		t.Errorf("malformed hunk should leave added line untouched, got:\n%s", out)
	}
}

func TestParseHunkNewStart(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"@@ -1,3 +4,5 @@", 4},
		{"@@ -10,0 +11 @@ func Foo()", 11},
		{"@@ -5 +5 @@", 5},
		{"@@ no plus side @@", 0},
		{"", 0},
		{"@@ -1,3 +notanumber @@", 0},
	}
	for _, c := range cases {
		if got := parseHunkNewStart(c.in); got != c.want {
			t.Errorf("parseHunkNewStart(%q) = %d, want %d", c.in, got, c.want)
		}
	}
}
