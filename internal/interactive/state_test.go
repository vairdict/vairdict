package interactive

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/vairdict/vairdict/internal/state"
)

func TestAddAndDrainNotes(t *testing.T) {
	s := New()

	if got := s.DrainNotes(); got != nil {
		t.Errorf("DrainNotes on empty state returned %v, want nil", got)
	}

	s.AddNote("first")
	s.AddNote("") // ignored
	s.AddNote("second")

	if n := s.Status().QueuedNotes; n != 2 {
		t.Errorf("QueuedNotes = %d, want 2 (empty string should be ignored)", n)
	}

	drained := s.DrainNotes()
	if len(drained) != 2 || drained[0] != "first" || drained[1] != "second" {
		t.Errorf("DrainNotes returned %v, want [first second]", drained)
	}

	// Drain is destructive — a second call should come back empty.
	if got := s.DrainNotes(); got != nil {
		t.Errorf("second DrainNotes returned %v, want nil (consumed once)", got)
	}
}

func TestSetPausedIdempotent(t *testing.T) {
	s := New()
	if s.IsPaused() {
		t.Fatal("new State should not be paused")
	}
	s.SetPaused(true)
	s.SetPaused(true) // second call is a no-op
	if !s.IsPaused() {
		t.Error("after SetPaused(true), IsPaused should be true")
	}
	s.SetPaused(false)
	if s.IsPaused() {
		t.Error("after SetPaused(false), IsPaused should be false")
	}
}

func TestWaitWhilePaused_NotPausedReturnsImmediately(t *testing.T) {
	s := New()
	// Not paused — WaitWhilePaused must return immediately with nil.
	done := make(chan error, 1)
	go func() { done <- s.WaitWhilePaused(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("unexpected error: %v", err)
		}
	case <-time.After(250 * time.Millisecond):
		t.Fatal("WaitWhilePaused blocked on an unpaused state")
	}
}

func TestWaitWhilePaused_WakesOnResume(t *testing.T) {
	s := New()
	s.SetPaused(true)

	var wg sync.WaitGroup
	wg.Add(1)
	var gotErr error
	go func() {
		defer wg.Done()
		gotErr = s.WaitWhilePaused(context.Background())
	}()

	// Give the goroutine time to enter the wait.
	time.Sleep(50 * time.Millisecond)
	s.SetPaused(false)
	wg.Wait()

	if gotErr != nil {
		t.Errorf("WaitWhilePaused returned %v on resume, want nil", gotErr)
	}
}

func TestWaitWhilePaused_CancelledContextUnblocks(t *testing.T) {
	// A cancelled context must wake WaitWhilePaused even if the user
	// never hits continue — otherwise ctrl-c can't abort a paused run.
	s := New()
	s.SetPaused(true)
	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() { done <- s.WaitWhilePaused(ctx) }()

	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Error("expected ctx.Err, got nil")
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("WaitWhilePaused did not wake on context cancel")
	}
}

func TestStatusSnapshot(t *testing.T) {
	s := New()
	s.UpdatePhase(state.PhaseCode, 2, 5)
	s.UpdateScore(87)
	s.AddNote("hi")

	sn := s.Status()
	if sn.Phase != state.PhaseCode {
		t.Errorf("Phase = %s, want code", sn.Phase)
	}
	if sn.Loop != 2 || sn.MaxLoop != 5 {
		t.Errorf("Loop/MaxLoop = %d/%d, want 2/5", sn.Loop, sn.MaxLoop)
	}
	if sn.LastScore != 87 {
		t.Errorf("LastScore = %v, want 87", sn.LastScore)
	}
	if sn.QueuedNotes != 1 {
		t.Errorf("QueuedNotes = %d, want 1", sn.QueuedNotes)
	}
	if sn.Paused {
		t.Error("Paused = true, want false")
	}
	if sn.Elapsed <= 0 {
		t.Errorf("Elapsed should be > 0, got %v", sn.Elapsed)
	}
}
