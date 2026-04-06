package ui

import (
	"os"

	"github.com/mattn/go-isatty"
)

// IsTerminal returns true if the given file descriptor is attached to a TTY.
// Wrapped here so callers don't need to import go-isatty directly and so it
// can be stubbed in tests.
func IsTerminal(f *os.File) bool {
	if f == nil {
		return false
	}
	return isatty.IsTerminal(f.Fd()) || isatty.IsCygwinTerminal(f.Fd())
}

// NoColorEnv returns true when the user has disabled color via the
// NO_COLOR environment variable. We follow the convention at no-color.org:
// any non-empty value disables color.
func NoColorEnv() bool {
	return os.Getenv("NO_COLOR") != ""
}
