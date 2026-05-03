//go:build windows

package main

import (
	"errors"
	"io"
)

// spawnBackgroundOS on Windows returns an explicit error: the unix
// implementation relies on syscall.SysProcAttr.Setsid (a new
// process-session primitive) to detach the child from the controlling
// terminal. Windows uses different process-group / console-detach
// primitives and the existing background flow has not been adapted to
// them. Until that work happens, the --background flag is unsupported
// on Windows and surfaces with a clear actionable message rather than
// silently doing the wrong thing.
//
// Windows users can still run vairdict in the foreground; only
// detached background spawning is unavailable.
func spawnBackgroundOS(_ string, _ []string, _ io.Writer) error {
	return errors.New("--background is not supported on Windows; run vairdict in the foreground instead")
}
