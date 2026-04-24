package interactive

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"time"
)

// RunLoop is the foreground input reader. It parses lines from `in`
// and dispatches to the four commands in the #91 AC:
//
//	status             — print current phase/loop/score/elapsed/paused
//	note <text>        — queue guidance for the next agent prompt
//	pause              — gate next phase/loop entry
//	continue           — resume after pause
//
// Returns when the context is cancelled (caller finishes orchestration)
// or stdin is closed. Unknown commands print a short help line — the
// loop never returns on a bad command.
//
// The loop is intentionally simple (`bufio.Scanner`, no readline,
// no TUI framework). The issue explicitly calls for this: a run
// already has a rich renderer emitting to stdout, and a fancier input
// UI would fight it on every repaint.
func RunLoop(ctx context.Context, s *State, in io.Reader, out io.Writer) {
	scanner := bufio.NewScanner(in)
	// Break out of scanner.Scan() when the context is cancelled so a
	// finishing run shuts the input goroutine down cleanly. Scanner
	// doesn't support context, so we close the underlying reader via
	// the caller; here we just re-check after each line.
	for scanner.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		handleCommand(line, s, out)
	}
}

// handleCommand parses and dispatches a single input line. Split out of
// RunLoop so unit tests can drive it without spinning the scanner.
func handleCommand(line string, s *State, out io.Writer) {
	cmd, rest := splitCmd(line)
	switch cmd {
	case "status":
		renderStatus(s.Status(), out)
	case "note":
		if rest == "" {
			_, _ = fmt.Fprintln(out, "usage: note <text to append to next agent prompt>")
			return
		}
		s.AddNote(rest)
		slog.Info("user note queued", "note", rest)
		_, _ = fmt.Fprintf(out, "note queued (%d pending): %s\n", s.Status().QueuedNotes, rest)
	case "pause":
		if s.IsPaused() {
			_, _ = fmt.Fprintln(out, "already paused")
			return
		}
		s.SetPaused(true)
		slog.Info("run paused by user")
		_, _ = fmt.Fprintln(out, "paused — orchestration will stop at the next phase/loop boundary. type 'continue' to resume.")
	case "continue", "resume":
		if !s.IsPaused() {
			_, _ = fmt.Fprintln(out, "not paused")
			return
		}
		s.SetPaused(false)
		slog.Info("run resumed by user")
		_, _ = fmt.Fprintln(out, "resuming…")
	case "help", "?":
		renderHelp(out)
	default:
		_, _ = fmt.Fprintf(out, "unknown command %q — type 'help' for the list.\n", cmd)
	}
}

// splitCmd splits "note hello there" into ("note", "hello there").
// Preserves internal whitespace in the argument so multi-word notes
// reach the agent intact.
func splitCmd(line string) (cmd, rest string) {
	i := strings.IndexByte(line, ' ')
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimSpace(line[i+1:])
}

// renderStatus prints a one-line snapshot. Intentionally terse so a
// user polling `status` between phases sees a compact diff.
func renderStatus(sn Snapshot, out io.Writer) {
	phase := string(sn.Phase)
	if phase == "" {
		phase = "pending"
	}
	pausedLabel := ""
	if sn.Paused {
		pausedLabel = " [PAUSED]"
	}
	scoreLabel := "-"
	if sn.LastScore > 0 {
		scoreLabel = fmt.Sprintf("%.0f", sn.LastScore)
	}
	loopLabel := fmt.Sprintf("%d/%d", sn.Loop, sn.MaxLoop)
	if sn.MaxLoop == 0 {
		loopLabel = fmt.Sprintf("%d", sn.Loop)
	}
	_, _ = fmt.Fprintf(out, "phase=%s loop=%s score=%s elapsed=%s notes=%d%s\n",
		phase, loopLabel, scoreLabel, formatElapsed(sn.Elapsed), sn.QueuedNotes, pausedLabel)
}

// formatElapsed prints a short HH:MM:SS-ish string. Kept out of
// time.Duration's default format to avoid "3m5.123456s" noise.
func formatElapsed(d time.Duration) string {
	d = d.Round(time.Second)
	h := int(d / time.Hour)
	m := int((d % time.Hour) / time.Minute)
	sec := int((d % time.Minute) / time.Second)
	if h > 0 {
		return fmt.Sprintf("%dh%02dm%02ds", h, m, sec)
	}
	if m > 0 {
		return fmt.Sprintf("%dm%02ds", m, sec)
	}
	return fmt.Sprintf("%ds", sec)
}

func renderHelp(out io.Writer) {
	_, _ = fmt.Fprintln(out, "interactive run commands:")
	_, _ = fmt.Fprintln(out, "  status            — show current phase, loop, score, elapsed time")
	_, _ = fmt.Fprintln(out, "  note <text>       — queue a note for the next agent prompt")
	_, _ = fmt.Fprintln(out, "  pause             — pause before the next phase/loop (does not kill an in-flight agent)")
	_, _ = fmt.Fprintln(out, "  continue          — resume after pause")
	_, _ = fmt.Fprintln(out, "  help              — show this list")
}
