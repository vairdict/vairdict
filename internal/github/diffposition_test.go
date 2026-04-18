package github

import "testing"

const sampleDiff = `diff --git a/internal/foo/bar.go b/internal/foo/bar.go
--- a/internal/foo/bar.go
+++ b/internal/foo/bar.go
@@ -10,6 +10,8 @@ func existing() {
 	unchanged := true
 	_ = unchanged
+	added1 := "new"
+	added2 := "also new"
 	more := "context"
 	_ = more
 }
diff --git a/internal/baz/qux.go b/internal/baz/qux.go
new file mode 100644
--- /dev/null
+++ b/internal/baz/qux.go
@@ -0,0 +1,5 @@
+package baz
+
+func Qux() {
+	return
+}
`

func TestParseDiffPositions_BasicMapping(t *testing.T) {
	positions := ParseDiffPositions(sampleDiff)

	// Verify file entries exist
	if _, ok := positions["internal/foo/bar.go"]; !ok {
		t.Fatal("missing positions for internal/foo/bar.go")
	}
	if _, ok := positions["internal/baz/qux.go"]; !ok {
		t.Fatal("missing positions for internal/baz/qux.go")
	}
}

func TestParseDiffPositions_AddedLines(t *testing.T) {
	positions := ParseDiffPositions(sampleDiff)

	// In bar.go: @@ -10,6 +10,8 @@
	// Position 1: @@ header
	// Position 2: line 10 (context: unchanged := true)
	// Position 3: line 11 (context: _ = unchanged)
	// Position 4: line 12 (added: added1)
	// Position 5: line 13 (added: added2)
	// Position 6: line 14 (context: more)
	// Position 7: line 15 (context: _ = more)
	// Position 8: line 16 (context: })

	pos, ok := ResolveDiffPosition(positions, "internal/foo/bar.go", 12)
	if !ok {
		t.Fatal("expected to find position for bar.go:12")
	}
	if pos != 4 {
		t.Errorf("bar.go:12 position = %d, want 4", pos)
	}

	pos, ok = ResolveDiffPosition(positions, "internal/foo/bar.go", 13)
	if !ok {
		t.Fatal("expected to find position for bar.go:13")
	}
	if pos != 5 {
		t.Errorf("bar.go:13 position = %d, want 5", pos)
	}
}

func TestParseDiffPositions_NewFile(t *testing.T) {
	positions := ParseDiffPositions(sampleDiff)

	// In qux.go: @@ -0,0 +1,5 @@
	// Position 1: @@ header
	// Position 2: line 1 (+package baz)
	// Position 3: line 2 (+empty)
	// Position 4: line 3 (+func Qux)
	// Position 5: line 4 (+return)
	// Position 6: line 5 (+})

	pos, ok := ResolveDiffPosition(positions, "internal/baz/qux.go", 1)
	if !ok {
		t.Fatal("expected to find position for qux.go:1")
	}
	if pos != 2 {
		t.Errorf("qux.go:1 position = %d, want 2", pos)
	}

	pos, ok = ResolveDiffPosition(positions, "internal/baz/qux.go", 4)
	if !ok {
		t.Fatal("expected to find position for qux.go:4")
	}
	if pos != 5 {
		t.Errorf("qux.go:4 position = %d, want 5", pos)
	}
}

func TestResolveDiffPosition_UnknownFile(t *testing.T) {
	positions := ParseDiffPositions(sampleDiff)

	_, ok := ResolveDiffPosition(positions, "nonexistent.go", 1)
	if ok {
		t.Error("expected false for unknown file")
	}
}

func TestResolveDiffPosition_LineNotInDiff(t *testing.T) {
	positions := ParseDiffPositions(sampleDiff)

	// Line 1 of bar.go is not in the diff (diff starts at line 10)
	_, ok := ResolveDiffPosition(positions, "internal/foo/bar.go", 1)
	if ok {
		t.Error("expected false for line outside diff hunk")
	}
}

func TestParseDiffPositions_EmptyDiff(t *testing.T) {
	positions := ParseDiffPositions("")
	if len(positions) != 0 {
		t.Errorf("expected empty map, got %d entries", len(positions))
	}
}

func TestParseDiffPositions_RemovedLines(t *testing.T) {
	diff := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -5,7 +5,6 @@ func f() {
 	a := 1
-	b := 2
 	c := 3
+	d := 4
 	e := 5
 }
`
	positions := ParseDiffPositions(diff)

	// @@ -5,7 +5,6 @@
	// Pos 1: @@ header
	// Pos 2: line 5 (context: a := 1)
	// Pos 3: removed line (b := 2) — no new line
	// Pos 4: line 6 (context: c := 3)
	// Pos 5: line 7 (added: d := 4)
	// Pos 6: line 8 (context: e := 5)
	// Pos 7: line 9 (context: })

	// Line 7 is the added line d := 4
	pos, ok := ResolveDiffPosition(positions, "x.go", 7)
	if !ok {
		t.Fatal("expected to find position for x.go:7")
	}
	if pos != 5 {
		t.Errorf("x.go:7 position = %d, want 5", pos)
	}

	// Line 6 is context after the removal
	pos, ok = ResolveDiffPosition(positions, "x.go", 6)
	if !ok {
		t.Fatal("expected to find position for x.go:6")
	}
	if pos != 4 {
		t.Errorf("x.go:6 position = %d, want 4", pos)
	}
}

func TestParseGitDiffFile(t *testing.T) {
	tests := []struct {
		line string
		want string
	}{
		{"diff --git a/foo.go b/foo.go", "foo.go"},
		{"diff --git a/internal/foo/bar.go b/internal/foo/bar.go", "internal/foo/bar.go"},
		{"diff --git a/pkg/util.go b/pkg/util.go", "pkg/util.go"},
		{"not a diff line", ""},
	}
	for _, tc := range tests {
		got := parseGitDiffFile(tc.line)
		if got != tc.want {
			t.Errorf("parseGitDiffFile(%q) = %q, want %q", tc.line, got, tc.want)
		}
	}
}

func TestParseHunkNewStart(t *testing.T) {
	tests := []struct {
		line string
		want int
	}{
		{"@@ -10,6 +10,8 @@ func existing() {", 10},
		{"@@ -0,0 +1,5 @@", 1},
		{"@@ -1 +1 @@", 1},
		{"@@ -100,3 +200,5 @@ context", 200},
		{"not a hunk", 0},
	}
	for _, tc := range tests {
		got := parseHunkNewStart(tc.line)
		if got != tc.want {
			t.Errorf("parseHunkNewStart(%q) = %d, want %d", tc.line, got, tc.want)
		}
	}
}
