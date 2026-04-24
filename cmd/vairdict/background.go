package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"
)

// spawnBackground re-executes the current binary with the given args
// in a detached session and returns the child's PID. The child's
// stdout/stderr are redirected to the per-task log file at
// ~/.vairdict/logs/<taskID>.log so the work proceeds identically to
// a foreground run.
//
// The parent then writes a short "started in background" banner to
// `banner` (os.Stdout for humans) and returns — the child keeps
// running after the parent exits.
//
// Not supported on non-unix platforms: `setsid` is a unix concept.
// Windows users can still run vairdict in the foreground.
func spawnBackground(taskID string, passthroughArgs []string, banner io.Writer) error {
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

	fmt.Fprintf(banner, "task %s running in background (pid %d)\n", taskID, cmd.Process.Pid)
	fmt.Fprintf(banner, "  status: vairdict status %s\n", taskID)
	fmt.Fprintf(banner, "  logs:   vairdict logs %s -f\n", taskID)
	fmt.Fprintf(banner, "  resume: vairdict resume %s\n", taskID)

	// Release the child so the parent can exit cleanly without waiting.
	if err := cmd.Process.Release(); err != nil {
		return fmt.Errorf("releasing background process: %w", err)
	}
	return nil
}

// shouldRunForeground reports whether the current invocation is either
// the re-exec child of a --background spawn, or a user-invoked
// foreground command. Used by `run` and `resume` to decide whether to
// detach or actually do the work.
func shouldRunForeground() bool {
	return os.Getenv("VAIRDICT_FOREGROUND") == "1"
}

// backgroundArgsForRun builds the argv to pass to the detached child so
// it re-runs `vairdict run` with the same flags and intent arguments,
// minus --background itself. Extracted for testability.
func backgroundArgsForRun(intents []string, issues []int, envName, priority string, dependsOn []string) []string {
	args := []string{"run"}
	for _, i := range issues {
		if i > 0 {
			args = append(args, "--issue", strconv.Itoa(i))
		}
	}
	if envName != "" {
		args = append(args, "--env", envName)
	}
	if priority != "" {
		args = append(args, "--priority", priority)
	}
	for _, d := range dependsOn {
		args = append(args, "--depends-on", d)
	}
	args = append(args, intents...)
	return args
}

// backgroundArgsForResume builds the argv for a detached resume.
func backgroundArgsForResume(taskID, envName string) []string {
	args := []string{"resume"}
	if envName != "" {
		args = append(args, "--env", envName)
	}
	args = append(args, taskID)
	return args
}
