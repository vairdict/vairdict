package interactive

import (
	"bytes"
	"context"
	"strings"
	"testing"
	"time"
)

func TestHandleCommand_Status(t *testing.T) {
	s := New()
	s.UpdatePhase("code", 2, 5)
	s.UpdateScore(73)

	var out bytes.Buffer
	handleCommand("status", s, &out)

	got := out.String()
	for _, want := range []string{"phase=code", "loop=2/5", "score=73", "notes=0"} {
		if !strings.Contains(got, want) {
			t.Errorf("status output missing %q\n---\n%s", want, got)
		}
	}
}

func TestHandleCommand_Note(t *testing.T) {
	s := New()
	var out bytes.Buffer
	handleCommand("note remember to add tests", s, &out)

	if s.Status().QueuedNotes != 1 {
		t.Errorf("note count = %d, want 1", s.Status().QueuedNotes)
	}
	drained := s.DrainNotes()
	if len(drained) != 1 || drained[0] != "remember to add tests" {
		t.Errorf("drained = %v, want [\"remember to add tests\"]", drained)
	}
	if !strings.Contains(out.String(), "note queued") {
		t.Errorf("missing echo line: %q", out.String())
	}
}

func TestHandleCommand_NoteRequiresArg(t *testing.T) {
	s := New()
	var out bytes.Buffer
	handleCommand("note", s, &out)

	if s.Status().QueuedNotes != 0 {
		t.Error("bare 'note' should not queue anything")
	}
	if !strings.Contains(out.String(), "usage:") {
		t.Errorf("missing usage line: %q", out.String())
	}
}

func TestHandleCommand_PauseAndContinue(t *testing.T) {
	s := New()
	var out bytes.Buffer

	handleCommand("pause", s, &out)
	if !s.IsPaused() {
		t.Fatal("pause command should set paused=true")
	}

	// Second pause should be a no-op with a 'already paused' line.
	out.Reset()
	handleCommand("pause", s, &out)
	if !strings.Contains(out.String(), "already paused") {
		t.Errorf("double pause should warn, got: %q", out.String())
	}

	out.Reset()
	handleCommand("continue", s, &out)
	if s.IsPaused() {
		t.Fatal("continue command should clear paused")
	}

	out.Reset()
	handleCommand("continue", s, &out)
	if !strings.Contains(out.String(), "not paused") {
		t.Errorf("continue when not paused should warn, got: %q", out.String())
	}
}

func TestHandleCommand_Unknown(t *testing.T) {
	s := New()
	var out bytes.Buffer
	handleCommand("foo bar", s, &out)
	if !strings.Contains(out.String(), "unknown command") {
		t.Errorf("unknown command should be reported: %q", out.String())
	}
}

func TestRunLoop_ExitsOnEOF(t *testing.T) {
	// When the input stream closes, RunLoop must return — otherwise
	// piped stdin or a closed terminal would hang the run forever.
	s := New()
	in := strings.NewReader("status\nnote hello\n")
	var out bytes.Buffer

	done := make(chan struct{})
	go func() {
		RunLoop(context.Background(), s, in, &out)
		close(done)
	}()

	select {
	case <-done:
		// Both commands should have been processed before EOF.
		if s.Status().QueuedNotes != 1 {
			t.Errorf("expected 1 queued note after loop, got %d", s.Status().QueuedNotes)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("RunLoop did not return on EOF")
	}
}
