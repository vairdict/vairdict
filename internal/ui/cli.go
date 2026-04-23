package ui

import (
	"bufio"
	"fmt"
	"io"
	"strings"

	"github.com/vairdict/vairdict/internal/state"
)

// cliRenderer prints sectioned, colored, emoji-decorated output for human
// consumption in a terminal. It buffers via bufio for efficient writes and
// flushes on every public method so output appears immediately even if the
// caller forgets to Close().
type cliRenderer struct {
	w       *bufio.Writer
	pal     palette
	glyphs  glyphSet
	colors  ColorScheme
	useASCI bool
}

func newCLIRenderer(out io.Writer, colors ColorScheme, ascii bool) *cliRenderer {
	r := &cliRenderer{
		w:       bufio.NewWriter(out),
		pal:     paletteFor(colors),
		colors:  colors,
		useASCI: ascii,
	}
	if ascii {
		r.glyphs = asciiGlyphs()
	} else {
		r.glyphs = unicodeGlyphs()
	}
	return r
}

// flush flushes the buffer; ignored errors are written to /dev/stderr would
// just deadlock the user, so we silently swallow on a fully-broken writer.
func (r *cliRenderer) flush() {
	_ = r.w.Flush()
}

func (r *cliRenderer) Close() error {
	return r.w.Flush()
}

// printf is a small helper that writes to the buffered writer; ignoring the
// error is fine for a terminal sink — there is no useful recovery.
func (r *cliRenderer) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.w, format, args...)
}

func (r *cliRenderer) println(s string) {
	_, _ = r.w.WriteString(s)
	_, _ = r.w.WriteString("\n")
}

// ── Renderer methods ──────────────────────────────────────────────────────

func (r *cliRenderer) RunStart(taskID, intent, logPath string) {
	defer r.flush()
	r.println("")
	r.printf("%s%s  vairdict run %s· task %s%s\n",
		r.pal.bold, r.glyphs.logo, r.pal.dim, taskID, r.pal.reset)
	if intent != "" {
		title, body := splitIntent(intent)
		r.printf("   %s%s%s\n", r.pal.bold, title, r.pal.reset)
		if body != "" {
			for _, line := range truncateBody(body, 4) {
				r.printf("   %s%s%s\n", r.pal.dim, line, r.pal.reset)
			}
		}
	}
	if logPath != "" {
		r.printf("   %slogs: %s%s\n", r.pal.dim, logPath, r.pal.reset)
	}
}

func (r *cliRenderer) Note(label, value string) {
	defer r.flush()
	if value == "" {
		r.printf("   %s%s%s\n", r.pal.dim, label, r.pal.reset)
		return
	}
	r.printf("   %s%s:%s %s\n", r.pal.dim, label, r.pal.reset, value)
}

func (r *cliRenderer) PhaseStart(phase state.Phase) {
	defer r.flush()
	r.println("")
	r.printf("%s%s %s%s\n", r.pal.bold, r.glyphs.phase(phase), phaseTitle(phase), r.pal.reset)
}

func (r *cliRenderer) PhaseLoop(phase state.Phase, loop, max int, score float64, pass bool) {
	defer r.flush()
	mark := r.glyphs.fail
	if pass {
		mark = r.glyphs.pass
	}
	scoreColor := r.pal.scoreColor(score)
	if !pass {
		scoreColor = r.pal.red
	}
	rule := strings.Repeat(r.glyphs.rule, 12)
	r.printf("   Loop %d/%d %s %s%.0f%%%s %s\n",
		loop, max, rule, scoreColor, score, r.pal.reset, mark)
}

func (r *cliRenderer) PhaseLoopBlockingGaps(gaps []state.Gap) {
	defer r.flush()
	// Only show blocking gaps inline to keep output concise.
	var blocking []state.Gap
	for _, g := range gaps {
		if g.Blocking {
			blocking = append(blocking, g)
		}
	}
	if len(blocking) == 0 {
		return
	}
	for _, g := range blocking {
		r.printf("     %s%s%s [%s] %s\n", r.pal.dim, r.glyphs.blocking, r.pal.reset, g.Severity, g.Description)
	}
}

func (r *cliRenderer) PhaseDone(
	phase state.Phase,
	outcome PhaseOutcome,
	score float64,
	loops int,
	summary string,
	gaps []state.Gap,
) {
	defer r.flush()

	if summary != "" {
		r.println("")
		r.renderSummary(summary)
	}

	if outcome != OutcomePass && len(gaps) > 0 {
		r.println("")
		r.renderGaps(gaps)
	}
}

func (r *cliRenderer) PRCreated(url string) {
	defer r.flush()
	r.println("")
	r.printf("%s%s PR opened%s\n", r.pal.bold, r.glyphs.prCreated, r.pal.reset)
	r.printf("   %s\n", url)
}

func (r *cliRenderer) VerdictPosted(score float64, pass bool) {
	defer r.flush()
	tag := "FAIL"
	if pass {
		tag = "PASS"
	}
	color := r.pal.scoreColor(score)
	if !pass {
		color = r.pal.red
	}
	r.printf("   verdict: %s%.0f%% %s%s\n", color, score, tag, r.pal.reset)
}

func (r *cliRenderer) Escalation(taskID string, phase state.Phase, loops int, score float64, gaps []state.Gap) {
	defer r.flush()
	r.println("")
	r.printf("%s%s%s Escalation%s\n", r.pal.bold, r.pal.red, r.glyphs.escalate, r.pal.reset)
	r.printf("   task:    %s\n", taskID)
	r.printf("   phase:   %s\n", phaseTitle(phase))
	r.printf("   loops:   %d\n", loops)
	r.printf("   score:   %s%.0f%%%s\n", r.pal.red, score, r.pal.reset)
	if len(gaps) > 0 {
		r.println("")
		r.renderGaps(gaps)
	}
	r.println("")
	r.printf("   %sNo PR was created. Human intervention required.%s\n", r.pal.dim, r.pal.reset)
}

func (r *cliRenderer) RunComplete(taskID string) {
	defer r.flush()
	r.println("")
	r.printf("%s%s done%s · task %s\n", r.pal.bold, r.glyphs.pass, r.pal.reset, taskID)
}

func (r *cliRenderer) Error(err error) {
	defer r.flush()
	r.println("")
	r.printf("%s%s%s error%s: %s\n", r.pal.bold, r.pal.red, r.glyphs.fail, r.pal.reset, err.Error())
}

// ── helpers ───────────────────────────────────────────────────────────────

// renderSummary prints a multi-section narrative summary. The summary
// format is freeform markdown-ish text with `## Section` headers and
// `- item` bullets. Anything else is printed as-is, indented.
func (r *cliRenderer) renderSummary(summary string) {
	for line := range strings.SplitSeq(strings.TrimRight(summary, "\n"), "\n") {
		trimmed := strings.TrimSpace(line)
		switch {
		case trimmed == "":
			r.println("")
		case strings.HasPrefix(trimmed, "## "):
			r.printf("   %s%s%s\n", r.pal.bold, strings.TrimPrefix(trimmed, "## "), r.pal.reset)
		case strings.HasPrefix(trimmed, "- "):
			r.printf("     %s %s\n", r.glyphs.bullet, strings.TrimPrefix(trimmed, "- "))
		default:
			r.printf("   %s\n", trimmed)
		}
	}
}

func (r *cliRenderer) renderGaps(gaps []state.Gap) {
	r.printf("   %sGaps%s\n", r.pal.bold, r.pal.reset)
	for _, g := range gaps {
		marker := "  "
		if g.Blocking {
			marker = r.pal.red + r.glyphs.blocking + r.pal.reset
		}
		r.printf("     %s [%s] %s\n", marker, g.Severity, g.Description)
	}
}

// oneLine collapses any newlines in s to spaces so multi-line intents render
// on a single header line.
func oneLine(s string) string {
	return strings.ReplaceAll(strings.ReplaceAll(s, "\r\n", " "), "\n", " ")
}

// splitIntent splits an intent string into a title (first non-empty line)
// and a body (everything after, trimmed). For issue-sourced intents the
// format is "title\n\nbody".
func splitIntent(s string) (title, body string) {
	s = strings.TrimSpace(s)
	if t, b, ok := strings.Cut(s, "\n"); ok {
		title = strings.TrimSpace(t)
		body = strings.TrimSpace(b)
	} else {
		title = s
	}
	return
}

// truncateBody returns up to maxLines non-empty lines from a body string,
// trimming markdown noise (## headers become plain text). If more lines
// exist, a "..." line is appended.
func truncateBody(body string, maxLines int) []string {
	var lines []string
	for raw := range strings.SplitSeq(body, "\n") {
		line := strings.TrimSpace(raw)
		if line == "" {
			continue
		}
		// Strip markdown header prefixes for cleaner display.
		line = strings.TrimLeft(line, "#")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		// Truncate long lines.
		if len(line) > 80 {
			line = line[:77] + "..."
		}
		lines = append(lines, line)
		if len(lines) >= maxLines {
			lines = append(lines, "...")
			break
		}
	}
	return lines
}
