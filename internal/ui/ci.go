package ui

import (
	"fmt"
	"io"
	"log/slog"

	"github.com/vairdict/vairdict/internal/state"
)

// ciRenderer is the non-interactive renderer used in CI / pipes. It writes a
// concise plain-text trace of every event to the configured Writer using a
// stable, grep-friendly format. Detailed structured logging still happens
// via the global slog handler — this renderer only adds the high-level
// progress markers a CI log reader actually needs.
type ciRenderer struct {
	w io.Writer
}

func newCIRenderer(out io.Writer) *ciRenderer {
	return &ciRenderer{w: out}
}

func (r *ciRenderer) printf(format string, args ...any) {
	_, _ = fmt.Fprintf(r.w, format, args...)
}

func (r *ciRenderer) Close() error { return nil }

func (r *ciRenderer) RunStart(taskID, intent, logPath string) {
	r.printf("vairdict: run start task=%s intent=%q log=%s\n", taskID, oneLine(intent), logPath)
	slog.Info("run start", "task", taskID, "log", logPath)
}

func (r *ciRenderer) Note(label, value string) {
	r.printf("vairdict: %s=%s\n", label, value)
}

func (r *ciRenderer) PhaseStart(phase state.Phase) {
	r.printf("vairdict: phase start phase=%s\n", phase)
}

func (r *ciRenderer) PhaseLoop(phase state.Phase, loop, max int, score float64, pass bool) {
	r.printf("vairdict: phase loop phase=%s loop=%d/%d score=%.0f pass=%v\n", phase, loop, max, score, pass)
}

func (r *ciRenderer) PhaseLoopGaps(gaps []state.Gap) {
	for _, g := range gaps {
		if g.Blocking {
			r.printf("vairdict: gap [%s] %s\n", g.Severity, g.Description)
		}
	}
}

func (r *ciRenderer) PhaseDone(
	phase state.Phase,
	outcome PhaseOutcome,
	score float64,
	loops int,
	summary string,
	gaps []state.Gap,
) {
	r.printf("vairdict: phase done phase=%s outcome=%s score=%.0f loops=%d gaps=%d\n",
		phase, outcomeName(outcome), score, loops, len(gaps))
	if summary != "" {
		// One log entry, multi-line is fine — CI log readers handle it.
		r.printf("vairdict: phase summary phase=%s\n%s\n", phase, summary)
	}
}

func (r *ciRenderer) PRCreated(url string) {
	r.printf("vairdict: pr created url=%s\n", url)
}

func (r *ciRenderer) VerdictPosted(score float64, pass bool) {
	r.printf("vairdict: verdict posted score=%.0f pass=%v\n", score, pass)
}

func (r *ciRenderer) Escalation(taskID string, phase state.Phase, loops int, score float64, gaps []state.Gap) {
	r.printf("vairdict: escalation task=%s phase=%s loops=%d score=%.0f gaps=%d\n",
		taskID, phase, loops, score, len(gaps))
}

func (r *ciRenderer) RunComplete(taskID string) {
	r.printf("vairdict: run complete task=%s\n", taskID)
}

func (r *ciRenderer) Error(err error) {
	r.printf("vairdict: error %s\n", err.Error())
}

func outcomeName(o PhaseOutcome) string {
	switch o {
	case OutcomePass:
		return "pass"
	case OutcomeFail:
		return "fail"
	case OutcomeEscalate:
		return "escalate"
	case OutcomeRequeueToCode:
		return "requeue_to_code"
	case OutcomeRequeueToPlan:
		return "requeue_to_plan"
	}
	return "unknown"
}
