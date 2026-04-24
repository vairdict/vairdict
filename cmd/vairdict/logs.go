package main

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

var (
	logsFollowFlag bool
	logsLinesFlag  int
)

var logsCmd = &cobra.Command{
	Use:   "logs <task-id>",
	Short: "Show logs for a task in human-readable form",
	Long: `Pretty-prints the per-task log file at ~/.vairdict/logs/<task-id>.log.

The file is JSON per line (slog). This command formats each line as
  HH:MM:SS  [phase/state]  message
so tailing a run is readable without an external parser.

Flags:
  -n, --lines N   show only the last N lines (default 200; 0 for all)
  -f, --follow    keep reading as new log lines are written`,
	Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		return showLogs(args[0], logsLinesFlag, logsFollowFlag, os.Stdout)
	},
}

func init() {
	logsCmd.Flags().BoolVarP(&logsFollowFlag, "follow", "f", false, "follow the log as new lines are written (like tail -f)")
	logsCmd.Flags().IntVarP(&logsLinesFlag, "lines", "n", 200, "show the last N lines (0 for all)")
	rootCmd.AddCommand(logsCmd)
}

// showLogs opens the per-task log file, prints (optionally) the last N
// lines formatted, then optionally follows for new lines. Extracted so
// tests can point it at a temp file with an injected writer.
func showLogs(taskID string, lines int, follow bool, out io.Writer) error {
	path, err := logPathForTask(taskID)
	if err != nil {
		return err
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no log file for task %s (expected %s)", taskID, path)
		}
		return fmt.Errorf("opening log file: %w", err)
	}
	defer func() { _ = f.Close() }()

	// Print the last `lines` lines. When lines <= 0, print everything.
	// Done by reading once, buffering up to `lines` recent lines via a
	// ring, then flushing. For large logs this is O(n) in scan but
	// O(lines) in memory.
	if err := tailPrint(f, lines, out); err != nil {
		return err
	}

	if !follow {
		return nil
	}

	// Follow: re-read from current position as the file grows. Simple
	// poll loop (200ms) — good enough for a dev-tool tail and avoids a
	// platform-specific fsnotify dep.
	reader := bufio.NewReader(f)
	for {
		line, err := reader.ReadString('\n')
		if line != "" {
			_, _ = fmt.Fprint(out, formatLogLine(line))
		}
		if err == nil {
			continue
		}
		if !errors.Is(err, io.EOF) {
			return fmt.Errorf("reading log: %w", err)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// logPathForTask returns the absolute log path for a task id.
func logPathForTask(taskID string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolving home dir: %w", err)
	}
	return filepath.Join(home, ".vairdict", "logs", taskID+".log"), nil
}

// tailPrint emits the formatted form of the last `lines` lines from r.
// Non-positive lines means "print everything". Each line goes through
// formatLogLine so JSON events render as human-readable text.
func tailPrint(r io.Reader, lines int, out io.Writer) error {
	scanner := bufio.NewScanner(r)
	// Log messages include full prompts which can be large; bump the
	// default 64KB buffer to 1MB so scanning doesn't fail on a single
	// long line.
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	if lines <= 0 {
		for scanner.Scan() {
			_, _ = fmt.Fprint(out, formatLogLine(scanner.Text()+"\n"))
		}
		return scanner.Err()
	}

	ring := make([]string, 0, lines)
	for scanner.Scan() {
		if len(ring) < lines {
			ring = append(ring, scanner.Text())
			continue
		}
		// Shift left by one (cheap at these sizes) and append.
		copy(ring, ring[1:])
		ring[len(ring)-1] = scanner.Text()
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("scanning log: %w", err)
	}
	for _, l := range ring {
		_, _ = fmt.Fprint(out, formatLogLine(l+"\n"))
	}
	return nil
}

// formatLogLine turns one JSON slog line into a readable single line:
//
//	HH:MM:SS  [phase/state]  msg  key=val key=val
//
// Unknown fields (any beyond time/level/msg/phase/state/task_id) are
// appended as key=val pairs so debug info isn't lost, but common
// orchestration metadata is given a fixed slot at the front.
//
// If the line isn't valid JSON (e.g. stray stderr the runner wrote
// directly to the file), it is returned unchanged so nothing is
// silently dropped.
func formatLogLine(raw string) string {
	trimmed := strings.TrimRight(raw, "\n")
	if trimmed == "" {
		return raw
	}

	var event map[string]any
	if err := json.Unmarshal([]byte(trimmed), &event); err != nil {
		// Not JSON — pass through as-is.
		return raw
	}

	// Clock (just HH:MM:SS — date is in the filename / rotation).
	clock := "        "
	if ts, ok := event["time"].(string); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			clock = t.Local().Format("15:04:05")
		}
	}

	msg, _ := event["msg"].(string)

	phase, _ := event["phase"].(string)
	if phase == "" {
		phase, _ = event["task_phase"].(string)
	}
	stateStr, _ := event["state"].(string)
	if stateStr == "" {
		stateStr, _ = event["task_state"].(string)
	}

	var header string
	switch {
	case phase != "" && stateStr != "":
		header = fmt.Sprintf("[%s/%s]", phase, stateStr)
	case phase != "":
		header = fmt.Sprintf("[%s]", phase)
	case stateStr != "":
		header = fmt.Sprintf("[%s]", stateStr)
	default:
		if lvl, ok := event["level"].(string); ok && lvl != "" && lvl != "INFO" {
			header = fmt.Sprintf("[%s]", strings.ToLower(lvl))
		}
	}

	// Collect remaining key=val extras (skip well-known fields).
	skip := map[string]bool{
		"time": true, "level": true, "msg": true,
		"phase": true, "state": true,
		"task_phase": true, "task_state": true,
	}
	var extras []string
	for k, v := range event {
		if skip[k] {
			continue
		}
		extras = append(extras, fmt.Sprintf("%s=%v", k, v))
	}

	var b strings.Builder
	b.WriteString(clock)
	b.WriteString("  ")
	if header != "" {
		b.WriteString(header)
		b.WriteString("  ")
	}
	b.WriteString(msg)
	if len(extras) > 0 {
		b.WriteString("  ")
		b.WriteString(strings.Join(extras, " "))
	}
	b.WriteString("\n")
	return b.String()
}
