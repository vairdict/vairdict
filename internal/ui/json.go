package ui

import (
	"encoding/json"
	"fmt"
	"io"

	"github.com/vairdict/vairdict/internal/state"
)

// jsonRenderer emits one JSON object per event so callers can pipe vairdict
// output through jq or feed it into another tool. Every event has an "event"
// field naming the kind plus event-specific payload fields.
type jsonRenderer struct {
	enc *json.Encoder
	w   io.Writer
}

func newJSONRenderer(out io.Writer) *jsonRenderer {
	return &jsonRenderer{
		enc: json.NewEncoder(out),
		w:   out,
	}
}

func (r *jsonRenderer) Close() error { return nil }

// emit writes one event object. We tolerate encode errors silently — there
// is no useful recovery for a broken stdout sink.
func (r *jsonRenderer) emit(event string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["event"] = event
	_ = r.enc.Encode(payload)
}

func (r *jsonRenderer) RunStart(taskID, intent, logPath string) {
	r.emit("run_start", map[string]any{
		"task_id": taskID,
		"intent":  intent,
		"log":     logPath,
	})
}

func (r *jsonRenderer) Note(label, value string) {
	r.emit("note", map[string]any{
		"label": label,
		"value": value,
	})
}

func (r *jsonRenderer) PhaseStart(phase state.Phase) {
	r.emit("phase_start", map[string]any{"phase": string(phase)})
}

func (r *jsonRenderer) PhaseLoop(phase state.Phase, loop, max int, score float64, pass bool) {
	r.emit("phase_loop", map[string]any{
		"phase": string(phase),
		"loop":  loop,
		"max":   max,
		"score": score,
		"pass":  pass,
	})
}

func (r *jsonRenderer) PhaseLoopBlockingGaps(gaps []state.Gap) {
	if len(gaps) == 0 {
		return
	}
	var blocking []any
	for _, g := range gaps {
		if g.Blocking {
			blocking = append(blocking, map[string]any{
				"severity":    string(g.Severity),
				"description": g.Description,
			})
		}
	}
	if len(blocking) > 0 {
		r.emit("phase_loop_gaps", map[string]any{"gaps": blocking})
	}
}

func (r *jsonRenderer) PhaseDone(
	phase state.Phase,
	outcome PhaseOutcome,
	score float64,
	loops int,
	summary string,
	gaps []state.Gap,
) {
	r.emit("phase_done", map[string]any{
		"phase":   string(phase),
		"outcome": outcomeName(outcome),
		"score":   score,
		"loops":   loops,
		"summary": summary,
		"gaps":    gaps,
	})
}

func (r *jsonRenderer) PRCreated(url string) {
	r.emit("pr_created", map[string]any{"url": url})
}

func (r *jsonRenderer) VerdictPosted(score float64, pass bool) {
	r.emit("verdict_posted", map[string]any{"score": score, "pass": pass})
}

func (r *jsonRenderer) Escalation(taskID string, phase state.Phase, loops int, score float64, gaps []state.Gap) {
	r.emit("escalation", map[string]any{
		"task_id": taskID,
		"phase":   string(phase),
		"loops":   loops,
		"score":   score,
		"gaps":    gaps,
	})
}

func (r *jsonRenderer) RunComplete(taskID string) {
	r.emit("run_complete", map[string]any{"task_id": taskID})
}

func (r *jsonRenderer) Error(err error) {
	r.emit("error", map[string]any{"error": fmt.Sprintf("%v", err)})
}
