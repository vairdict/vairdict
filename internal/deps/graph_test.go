package deps

import (
	"errors"
	"strings"
	"testing"
)

func buildGraph(t *testing.T, edges map[string][]string) *Graph {
	t.Helper()
	g := New()
	for id, depsOn := range edges {
		if err := g.Add(id, depsOn); err != nil {
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
