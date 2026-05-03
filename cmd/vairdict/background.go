package main

import (
	"io"
	"os"
	"strconv"
)

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

// spawnBackground re-executes the current binary with the given args
// in a detached session and returns the child's PID. The child's
// stdout/stderr are redirected to the per-task log file at
// ~/.vairdict/logs/<taskID>.log so the work proceeds identically to
// a foreground run.
//
// The platform-specific bits (how to detach the child from the
// controlling terminal / process group) live in background_unix.go and
// background_windows.go. spawnBackground itself dispatches to
// spawnBackgroundOS, which is defined per platform.
func spawnBackground(taskID string, passthroughArgs []string, banner io.Writer) error {
	return spawnBackgroundOS(taskID, passthroughArgs, banner)
}
