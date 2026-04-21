package deps

import (
	"errors"
	"fmt"
	"sort"
	"strings"
	"testing"
)

func buildGraph(t *testing.T, edges map[string][]string) *Graph {
	t.Helper()
	g := New()
	// Add in sorted-key order so insertion seq is deterministic across
	// runs — Go map iteration is randomised, and seq is Ready()'s
	// tiebreak since #80.
	ids := make([]string, 0, len(edges))
	for id := range edges {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if err := g.Add(id, edges[id]); err != nil {
			t.Fatalf("Add(%q): %v", id, err)
		}
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	return g
}

func TestLinearChain_AReadyThenBThenC(t *testing.T) {
	// A → B → C. At start only A is ready. After A done, B. After B
	// done, C. AllSettled is true only once C completes.
	g := buildGraph(t, map[string][]string{
		"A": nil,
		"B": {"A"},
		"C": {"B"},
	})

	if got := g.Ready(); !equal(got, []string{"A"}) {
		t.Fatalf("initial ready = %v, want [A]", got)
	}

	_ = g.MarkRunning("A")
	_ = g.MarkDone("A")
	if got := g.Ready(); !equal(got, []string{"B"}) {
		t.Fatalf("after A done, ready = %v, want [B]", got)
	}

	_ = g.MarkRunning("B")
	_ = g.MarkDone("B")
	if got := g.Ready(); !equal(got, []string{"C"}) {
		t.Fatalf("after B done, ready = %v, want [C]", got)
	}

	_ = g.MarkRunning("C")
	_ = g.MarkDone("C")
	if !g.AllSettled() {
		t.Error("expected AllSettled after C done")
	}
}

func TestDiamond_BCReadyAfterA_DAfterBoth(t *testing.T) {
	//   A
	//  / \
	// B   C
	//  \ /
	//   D
	g := buildGraph(t, map[string][]string{
		"A": nil,
		"B": {"A"},
		"C": {"A"},
		"D": {"B", "C"},
	})

	if got := g.Ready(); !equal(got, []string{"A"}) {
		t.Fatalf("initial ready = %v, want [A]", got)
	}

	_ = g.MarkRunning("A")
	_ = g.MarkDone("A")
	if got := g.Ready(); !equal(got, []string{"B", "C"}) {
		t.Fatalf("after A done, ready = %v, want [B C]", got)
	}

	// B done first; C still needs to finish before D is ready.
	_ = g.MarkRunning("B")
	_ = g.MarkDone("B")
	if got := g.Ready(); !equal(got, []string{"C"}) {
		t.Fatalf("after B done but C still pending, ready = %v, want [C]", got)
	}

	_ = g.MarkRunning("C")
	_ = g.MarkDone("C")
	if got := g.Ready(); !equal(got, []string{"D"}) {
		t.Fatalf("after C done, ready = %v, want [D]", got)
	}
}

func TestCycle_RejectedAtValidate(t *testing.T) {
	g := New()
	_ = g.Add("A", []string{"C"})
	_ = g.Add("B", []string{"A"})
	_ = g.Add("C", []string{"B"})

	err := g.Validate()
	if err == nil {
		t.Fatal("expected Validate to reject a cycle")
	}
	if !errors.Is(err, ErrCycle) {
		t.Errorf("expected ErrCycle, got %v", err)
	}
	// Every participating node must appear in the error for the operator
	// to actually diagnose it.
	for _, n := range []string{"A", "B", "C"} {
		if !strings.Contains(err.Error(), n) {
			t.Errorf("cycle error must name %q; got %q", n, err.Error())
		}
	}
}

func TestSelfLoop_RejectedAsCycle(t *testing.T) {
	g := New()
	_ = g.Add("A", []string{"A"})
	err := g.Validate()
	if !errors.Is(err, ErrCycle) {
		t.Errorf("self-loop must be rejected as cycle, got %v", err)
	}
}

func TestUnknownDep_RejectedAtValidate(t *testing.T) {
	g := New()
	_ = g.Add("A", []string{"B"}) // B never added

	err := g.Validate()
	if !errors.Is(err, ErrUnknownDep) {
		t.Errorf("expected ErrUnknownDep, got %v", err)
	}
}

func TestFailureCascade_BlocksTransitiveDownstream(t *testing.T) {
	// A failure on X must block Y and Z but NOT the unrelated branch Q.
	//
	//  X → Y → Z
	//  Q
	g := buildGraph(t, map[string][]string{
		"X": nil,
		"Y": {"X"},
		"Z": {"Y"},
		"Q": nil,
	})

	_ = g.MarkRunning("X")
	blocked, err := g.MarkFailed("X")
	if err != nil {
		t.Fatalf("MarkFailed: %v", err)
	}
	if !equal(blocked, []string{"Y", "Z"}) {
		t.Errorf("expected Y and Z blocked, got %v", blocked)
	}

	xStatus, _ := g.Status("X")
	yStatus, _ := g.Status("Y")
	zStatus, _ := g.Status("Z")
	qStatus, _ := g.Status("Q")

	if xStatus != StatusFailed {
		t.Errorf("X status = %s, want failed", xStatus)
	}
	if yStatus != StatusBlocked || zStatus != StatusBlocked {
		t.Errorf("Y/Z should both be blocked, got %s/%s", yStatus, zStatus)
	}
	if qStatus != StatusPending {
		t.Errorf("unrelated Q must stay pending after X fails, got %s", qStatus)
	}
}

func TestParallelRoots_AllReady(t *testing.T) {
	g := buildGraph(t, map[string][]string{
		"A": nil,
		"B": nil,
		"C": nil,
	})
	if got := g.Ready(); !equal(got, []string{"A", "B", "C"}) {
		t.Errorf("expected all three ready, got %v", got)
	}
}

func TestReady_DoesNotReturnRunningNodes(t *testing.T) {
	g := buildGraph(t, map[string][]string{"A": nil})
	_ = g.MarkRunning("A")
	if got := g.Ready(); len(got) != 0 {
		t.Errorf("running node must not appear in Ready(); got %v", got)
	}
}

func TestMarkRunning_FromNonPending_Errors(t *testing.T) {
	g := buildGraph(t, map[string][]string{"A": nil})
	_ = g.MarkRunning("A")
	_ = g.MarkDone("A")
	if err := g.MarkRunning("A"); err == nil {
		t.Error("expected error when marking done node as running")
	}
}

func TestDuplicateAdd_Errors(t *testing.T) {
	g := New()
	_ = g.Add("A", nil)
	if err := g.Add("A", nil); err == nil {
		t.Error("expected duplicate Add to error — graph is built once per run")
	}
}

func TestAllSettled_FalseUntilEveryoneTerminal(t *testing.T) {
	g := buildGraph(t, map[string][]string{"A": nil, "B": {"A"}})
	if g.AllSettled() {
		t.Error("AllSettled should be false with work pending")
	}
	_ = g.MarkRunning("A")
	_ = g.MarkDone("A")
	if g.AllSettled() {
		t.Error("AllSettled should be false while B is still pending")
	}
	_ = g.MarkRunning("B")
	_ = g.MarkDone("B")
	if !g.AllSettled() {
		t.Error("AllSettled should be true once all nodes done")
	}
}

func TestSnapshot_ReturnsSortedProjection(t *testing.T) {
	g := buildGraph(t, map[string][]string{
		"zeta":  nil,
		"alpha": {"zeta"},
	})
	snap := g.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	if snap[0].ID != "alpha" || snap[1].ID != "zeta" {
		t.Errorf("snapshot not sorted by ID: %+v", snap)
	}
	if !equal(snap[0].DependsOn, []string{"zeta"}) {
		t.Errorf("alpha.DependsOn = %v, want [zeta]", snap[0].DependsOn)
	}
}

func TestPriority_HigherGoesFirst(t *testing.T) {
	g := New()
	// Add in reverse priority order to confirm the sort doesn't rely on
	// insertion order.
	_ = g.AddWithPriority("low",    nil, PriorityLow)
	_ = g.AddWithPriority("normal", nil, PriorityNormal)
	_ = g.AddWithPriority("high",   nil, PriorityHigh)
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got := g.Ready()
	want := []string{"high", "normal", "low"}
	if !equal(got, want) {
		t.Errorf("Ready() priority order = %v, want %v", got, want)
	}
}

func TestPriority_EqualFallsBackToInsertionOrder(t *testing.T) {
	// Two high-priority tasks added in a specific order must come out
	// in that order — insertion seq is the stable tiebreaker, not ID
	// alphabetic order.
	g := New()
	_ = g.AddWithPriority("zeta",  nil, PriorityHigh)
	_ = g.AddWithPriority("alpha", nil, PriorityHigh)
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	got := g.Ready()
	want := []string{"zeta", "alpha"}
	if !equal(got, want) {
		t.Errorf("Ready() insertion-order tiebreak = %v, want %v", got, want)
	}
}

func TestPriority_InteractsWithDeps_HighDepOnLowWaitsForLow(t *testing.T) {
	// Even a high-priority node must wait for its dependency. Priority
	// sorts within the ready set; it cannot skip the graph structure.
	g := New()
	_ = g.AddWithPriority("low_root", nil,                  PriorityLow)
	_ = g.AddWithPriority("high_dep", []string{"low_root"}, PriorityHigh)
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// low_root is the only ready node even though high_dep has higher
	// priority — high_dep depends on low_root.
	if got := g.Ready(); !equal(got, []string{"low_root"}) {
		t.Fatalf("before low_root done: ready = %v, want [low_root]", got)
	}

	_ = g.MarkRunning("low_root")
	_ = g.MarkDone("low_root")
	if got := g.Ready(); !equal(got, []string{"high_dep"}) {
		t.Errorf("after low_root done: ready = %v, want [high_dep]", got)
	}
}

func TestPriority_StarvationIsBounded(t *testing.T) {
	// Starvation scenario: many high-priority tasks alongside a low. Once
	// the low is in Ready() it must eventually be returned — it does not
	// get permanently shadowed by equal-or-higher-priority siblings.
	// With a static ready set, the low task appears in every poll until
	// it is taken; we assert it is never silently filtered out.
	g := New()
	_ = g.AddWithPriority("low", nil, PriorityLow)
	for i := 0; i < 5; i++ {
		id := fmt.Sprintf("hi%d", i)
		_ = g.AddWithPriority(id, nil, PriorityHigh)
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	ready := g.Ready()
	// low must appear; it is the last entry because all 5 highs come first.
	if got := ready[len(ready)-1]; got != "low" {
		t.Errorf("low should be last (not missing) in priority-sorted ready; got last=%q, full=%v", got, ready)
	}
	// And it must be present even after running each high in sequence.
	for i, id := range ready[:5] {
		_ = g.MarkRunning(id)
		_ = g.MarkDone(id)
		leftover := g.Ready()
		wantLowPresent := true
		for _, lid := range leftover {
			if lid == "low" {
				wantLowPresent = true
				break
			}
		}
		if !wantLowPresent {
			t.Errorf("after %d highs done, low must still be ready; got %v", i+1, leftover)
		}
	}
}

func TestSnapshot_IncludesPriority(t *testing.T) {
	g := New()
	_ = g.AddWithPriority("a", nil, PriorityHigh)
	_ = g.AddWithPriority("b", nil, PriorityLow)
	_ = g.Validate()

	snap := g.Snapshot()
	if len(snap) != 2 {
		t.Fatalf("snapshot len = %d, want 2", len(snap))
	}
	if snap[0].Priority != PriorityHigh || snap[1].Priority != PriorityLow {
		t.Errorf("snapshot priorities = %v,%v; want high,low",
			snap[0].Priority, snap[1].Priority)
	}
}

func TestParsePriority_Values(t *testing.T) {
	cases := []struct {
		in   string
		want Priority
		err  bool
	}{
		{"", PriorityNormal, false},
		{"normal", PriorityNormal, false},
		{"high", PriorityHigh, false},
		{"low", PriorityLow, false},
		{"HIGH", 0, true},     // case-sensitive on purpose — YAML is lowercase
		{"critical", 0, true}, // not a defined level
	}
	for _, c := range cases {
		got, err := ParsePriority(c.in)
		if c.err {
			if err == nil {
				t.Errorf("ParsePriority(%q): expected error, got nil", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("ParsePriority(%q): unexpected error %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("ParsePriority(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestPriority_StringRoundTrip(t *testing.T) {
	// Round-trip: ParsePriority then String should return the canonical
	// lowercase label. Protects the status-command rendering path.
	for _, label := range []string{"high", "normal", "low"} {
		p, err := ParsePriority(label)
		if err != nil {
			t.Fatalf("ParsePriority(%q): %v", label, err)
		}
		if p.String() != label {
			t.Errorf("Priority(%q).String() = %q, want %q", label, p.String(), label)
		}
	}
}

func equal(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
