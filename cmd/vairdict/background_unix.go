//go:build unix

package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

// spawnBackgroundOS is the unix implementation: re-exec the binary in
// a new session (Setsid) so the child outlives the controlling
// terminal and the parent. Stdout/stderr go to the per-task log file
// so the detached run produces the same telemetry as a foreground
// run.
func spawnBackgroundOS(taskID string, passthroughArgs []string, banner io.Writer) error {
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("resolving own binary: %w", err)
	}

	logPath, err := logPathForTask(taskID)
	if err != nil {
		return fmt.Errorf("resolving log path: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(logPath), 0o700); err != nil {
		return fmt.Errorf("creating log dir: %w", err)
	}
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file for background run: %w", err)
	}

	cmd := exec.Command(self, passthroughArgs...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	// Detach: new session so the child outlives the controlling terminal.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	// Inherit VAIRDICT_FOREGROUND=1 into the child so it bypasses the
	// --background re-exec and runs the real work.
	cmd.Env = append(os.Environ(), "VAIRDICT_FOREGROUND=1")

	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return fmt.Errorf("starting background process: %w", err)
	}

	// The parent keeps the file descriptor to the log just long enough
	// to finish Start — the child has its own copy via fork. Close ours
	// so the parent doesn't hold the file open unnecessarily.
	_ = logFile.Close()

	_, _ = fmt.Fprintf(banner, "task %s running in background (pid %d)\n", taskID, cmd.Process.Pid)
	_, _ = fmt.Fprintf(banner, "  status: vairdict status %s\n", taskID)
	_, _ = fmt.Fprintf(banner, "  logs:   vairdict logs %s -f\n", taskID)
	_, _ = fmt.Fprintf(banner, "  resume: vairdict resume %s\n", taskID)

	// Release the child so the parent can exit cleanly without waiting.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("releasing background process: %w", err)
	}
	return nil
}
