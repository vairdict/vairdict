// Package interactive implements the in-flight steering interface for
// `vairdict run`. A foreground readline loop lets the user query status,
// queue notes for the next agent prompt, and pause/continue the outer
// orchestration without opening a second terminal.
//
// Two goroutines share this package: the orchestration goroutine reads
// pause state and drains note queues at phase boundaries; the input
// goroutine mutates them in response to user commands. All shared
// state lives behind a mutex + condition variable in State.
package interactive

import (
	"context"
	"sync"
	"time"

	"github.com/vairdict/vairdict/internal/state"
)

// State is the mutex-guarded shared memory between the orchestration
// goroutine and the foreground input loop. Constructed once per run
// and handed to both goroutines.
//
// Invariants:
//   - All public methods are safe for concurrent use.
//   - WaitWhilePaused must be called from the orchestration goroutine,
//     not the input goroutine (blocks on the pause condition).
//   - DrainNotes is destructive: a note is observed exactly once by the
//     orchestration side, matching the issue AC ("queued notes are
//     consumed once and logged").
type State struct {
	mu sync.Mutex
	// cond signals when `paused` flips false so WaitWhilePaused can
	// wake. Using a condition variable over a channel lets the pause
	// state be toggled any number of times cheaply.
	cond *sync.Cond

	paused bool
	notes  []string

	phase     state.Phase
	loop      int
	maxLoop   int
	lastScore float64
	startTime time.Time
}

// New constructs an empty state with the wall-clock start time captured
// for status elapsed-time reporting.
func New() *State {
	s := &State{
		startTime: time.Now(),
	}
	s.cond = sync.NewCond(&s.mu)
	return s
}

// Snapshot is a point-in-time read of the state, safe to print from
// the input goroutine without holding the state mutex past the call.
type Snapshot struct {
	Phase       state.Phase
	Loop        int
	MaxLoop     int
	LastScore   float64
	Elapsed     time.Duration
	Paused      bool
	QueuedNotes int
}

// Status returns a Snapshot for `status` command rendering.
func (s *State) Status() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		Phase:       s.phase,
		Loop:        s.loop,
		MaxLoop:     s.maxLoop,
		LastScore:   s.lastScore,
		Elapsed:     time.Since(s.startTime),
		Paused:      s.paused,
		QueuedNotes: len(s.notes),
	}
}

// AddNote queues guidance text from the user for the next agent call.
// Empty strings are ignored so a blank `note` command is a no-op rather
// than polluting the next prompt.
func (s *State) AddNote(text string) {
	if text == "" {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.notes = append(s.notes, text)
}

// DrainNotes returns every queued note and clears the queue. Called by
// the orchestration goroutine at phase boundaries; the AC requires
// notes to be consumed once, so each call is destructive.
func (s *State) DrainNotes() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.notes) == 0 {
		return nil
	}
	out := s.notes
	s.notes = nil
	return out
}

// SetPaused sets the paused flag and broadcasts a wakeup so any
// WaitWhilePaused caller that was blocked wakes up when `paused` goes
// false. No-op on idempotent sets — callers may safely pause-when-
// paused or continue-when-running.
func (s *State) SetPaused(paused bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.paused == paused {
		return
	}
	s.paused = paused
	s.cond.Broadcast()
}

// IsPaused reports whether pause is currently set. Used by tests and
// the status renderer.
func (s *State) IsPaused() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.paused
}

// WaitWhilePaused blocks until the paused flag is false or ctx is
// cancelled. Returns ctx.Err() on cancellation, nil when it resumed
// cleanly. Safe to call when not paused — the condition is checked
// before blocking so the common case is lock-drop-drop.
//
// Phase-boundary gating (`pause` before next loop entry) is the single
// caller today; the function is exposed on the State so other gates
// can reuse it without duplicating the cond-var dance.
func (s *State) WaitWhilePaused(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for s.paused {
		// Release the mutex while waiting and reacquire on wakeup —
		// ctx cancellation is surfaced via a goroutine that broadcasts
		// when the context fires, otherwise the wait would block
		// forever if the user cancels mid-pause.
		done := make(chan struct{})
		go func() {
			select {
			case <-ctx.Done():
				s.cond.Broadcast()
			case <-done:
			}
		}()
		s.cond.Wait()
		close(done)
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	return nil
}

// UpdatePhase records the phase the orchestration is about to enter
// along with its loop budget. Called before each plan/code/quality
// phase so `status` reflects live progress.
func (s *State) UpdatePhase(phase state.Phase, loop, maxLoop int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.phase = phase
	s.loop = loop
	s.maxLoop = maxLoop
}

// UpdateScore records the last verdict score observed at the current
// phase so `status` can show the trend within a phase.
func (s *State) UpdateScore(score float64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastScore = score
}
