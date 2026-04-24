package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/vairdict/vairdict/internal/interactive"
	"github.com/vairdict/vairdict/internal/state"
)

// TestOrchestration_DrainsNotesIntoTask verifies that when deps.interactive
// is wired in, notes queued on the shared State land on the task before
// each phase runs (and therefore reach the plan/code prompt builders).
func TestOrchestration_DrainsNotesIntoTask(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	iState := interactive.New()
	iState.AddNote("please write tests")

	deps := b.deps()
	deps.interactive = iState

	task := state.NewTask("t-notes", "build with notes")
	r := &fakeRenderer{}

	if err := runOrchestration(context.Background(), deps, task, r); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// The plan phase ran — it should have received task.Notes with the
	// user's queued note. We capture task.Notes at the time the fake
	// plan runner was called via a snapshot on the bundle.
	if b.plan.capturedNotes == nil {
		t.Fatal("plan runner should have seen task.Notes populated")
	}
	if len(b.plan.capturedNotes) != 1 || b.plan.capturedNotes[0] != "please write tests" {
		t.Errorf("plan runner got notes %v, want [\"please write tests\"]", b.plan.capturedNotes)
	}

	// Notes must be drained — a second call to DrainNotes comes back
	// empty so the same note isn't reinjected into subsequent phases.
	if got := iState.DrainNotes(); got != nil {
		t.Errorf("notes should be drained on first phase entry, still have %v", got)
	}
}

// TestOrchestration_PauseGateBlocksUntilResume verifies the pause
// semantics from the AC: pause gates the next phase entry, does not
// interrupt an in-flight agent call. We pause before the run starts,
// wait long enough that orchestration would have completed if not
// blocked, then resume and confirm it does finish.
func TestOrchestration_PauseGateBlocksUntilResume(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	iState := interactive.New()
	iState.SetPaused(true)

	deps := b.deps()
	deps.interactive = iState

	task := state.NewTask("t-pause", "build paused")
	r := &fakeRenderer{}

	done := make(chan error, 1)
	go func() { done <- runOrchestration(context.Background(), deps, task, r) }()

	// While paused, the plan runner must not be called.
	time.Sleep(100 * time.Millisecond)
	if b.plan.called {
		t.Fatal("plan runner should not have been called while paused")
	}

	// Resume — orchestration must unblock and finish.
	iState.SetPaused(false)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("orchestration did not finish within 1s after resume")
	}
	if !b.plan.called {
		t.Error("plan runner should have run after resume")
	}
}

// TestOrchestration_PauseCancelledContextReturnsError verifies that
// ctrl-c while paused actually aborts the run rather than hanging
// forever on the condition variable.
func TestOrchestration_PauseCancelledContextReturnsError(t *testing.T) {
	t.Parallel()
	b := newOrchBundle()
	iState := interactive.New()
	iState.SetPaused(true)

	deps := b.deps()
	deps.interactive = iState

	task := state.NewTask("t-pause-cancel", "cancel while paused")
	r := &fakeRenderer{}

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- runOrchestration(ctx, deps, task, r) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("expected context error, got nil")
		}
		if !strings.Contains(err.Error(), "context") {
			t.Errorf("expected context error, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("orchestration did not unblock on cancel")
	}
	if b.plan.called {
		t.Error("plan runner should not have been called after cancel while paused")
	}
}
