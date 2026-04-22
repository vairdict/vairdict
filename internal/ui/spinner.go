package ui

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// spinnerFrames are the braille-dot animation frames shown while a phase
// is running. Each frame is a single rune so cursor-overwrite is simple.
var spinnerFrames = []string{"⠋", "⠙", "⠹", "⠸", "⠼", "⠴", "⠦", "⠧", "⠇", "⠏"}

// asciiSpinnerFrames are used when --ascii is set.
var asciiSpinnerFrames = []string{"|", "/", "-", "\\"}

// Spinner shows an animated progress indicator on a terminal line.
// Safe to call Stop multiple times. No-op if the writer is not a TTY.
type Spinner struct {
	w      io.Writer
	label  string
	pal    palette
	frames []string

	mu      sync.Mutex
	running bool
	done    chan struct{}
}

// NewSpinner creates a spinner that writes to w. Call Start() to begin
// animation and Stop() to clear the line.
func NewSpinner(w io.Writer, label string, pal palette, ascii bool) *Spinner {
	frames := spinnerFrames
	if ascii {
		frames = asciiSpinnerFrames
	}
	return &Spinner{
		w:      w,
		label:  label,
		pal:    pal,
		frames: frames,
		done:   make(chan struct{}),
	}
}

// SetLabel updates the spinner's label text while it's running.
func (s *Spinner) SetLabel(label string) {
	s.mu.Lock()
	s.label = label
	s.mu.Unlock()
}

// Start begins the spinner animation in a background goroutine.
func (s *Spinner) Start() {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return
	}
	s.running = true
	s.mu.Unlock()

	go s.loop()
}

func (s *Spinner) loop() {
	tick := time.NewTicker(80 * time.Millisecond)
	defer tick.Stop()

	i := 0
	for {
		select {
		case <-s.done:
			return
		case <-tick.C:
			s.mu.Lock()
			label := s.label
			s.mu.Unlock()
			frame := s.frames[i%len(s.frames)]
			// \r returns to start of line, print spinner + label, clear rest of line with \033[K
			_, _ = fmt.Fprintf(s.w, "\r   %s%s %s%s\033[K", s.pal.dim, frame, label, s.pal.reset)
			i++
		}
	}
}

// IsRunning reports whether the spinner is currently animating.
func (s *Spinner) IsRunning() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Stop halts the spinner and clears its line.
func (s *Spinner) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return
	}
	s.running = false
	close(s.done)
	// Clear the spinner line: \r + clear-to-end-of-line
	_, _ = fmt.Fprint(s.w, "\r\033[K")
}

// Reset prepares the spinner for reuse after Stop. Must not be called
// while the spinner is running.
func (s *Spinner) Reset() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.done = make(chan struct{})
}
