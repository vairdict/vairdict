package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestFormatLogLine_JSON(t *testing.T) {
	raw := `{"time":"2026-04-24T15:04:05Z","level":"INFO","msg":"plan loop","phase":"plan","state":"planning","task_id":"a1b2c3d4","loop":2}` + "\n"

	got := formatLogLine(raw)
	// Time portion will vary by local tz; only assert the bracketed
	// section and message, which are tz-independent.
	if !strings.Contains(got, "[plan/planning]") {
		t.Errorf("missing phase/state header: %q", got)
	}
	if !strings.Contains(got, "plan loop") {
		t.Errorf("missing message: %q", got)
	}
	if !strings.Contains(got, "loop=2") {
		t.Errorf("missing extras: %q", got)
	}
	if !strings.Contains(got, "task_id=a1b2c3d4") {
		t.Errorf("missing task_id: %q", got)
	}
	if !strings.HasSuffix(got, "\n") {
		t.Errorf("output must end in newline: %q", got)
	}
}

func TestFormatLogLine_NotJSON(t *testing.T) {
	// Stray stderr written directly to the log should pass through
	// unchanged so no information is silently dropped.
	raw := "panic: runtime error\n"
	if got := formatLogLine(raw); got != raw {
		t.Errorf("non-JSON input mutated: want %q got %q", raw, got)
	}
}

func TestFormatLogLine_NoPhaseNoState(t *testing.T) {
	// Events without phase/state still format, using level as a
	// fallback header for non-INFO.
	raw := `{"time":"2026-04-24T15:04:05Z","level":"WARN","msg":"stale pid"}` + "\n"
	got := formatLogLine(raw)
	if !strings.Contains(got, "[warn]") {
		t.Errorf("expected [warn] fallback header, got %q", got)
	}
	if !strings.Contains(got, "stale pid") {
		t.Errorf("missing message: %q", got)
	}
}

func TestTailPrint_LastN(t *testing.T) {
	lines := []string{
		`{"time":"2026-04-24T15:00:00Z","msg":"one"}`,
		`{"time":"2026-04-24T15:00:01Z","msg":"two"}`,
		`{"time":"2026-04-24T15:00:02Z","msg":"three"}`,
		`{"time":"2026-04-24T15:00:03Z","msg":"four"}`,
	}
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	if err := tailPrint(in, 2, &out); err != nil {
		t.Fatalf("tailPrint: %v", err)
	}
	if strings.Contains(out.String(), "one") || strings.Contains(out.String(), "two") {
		t.Errorf("first two lines should be dropped: %s", out.String())
	}
	if !strings.Contains(out.String(), "three") || !strings.Contains(out.String(), "four") {
		t.Errorf("last two lines should be present: %s", out.String())
	}
}

func TestTailPrint_All(t *testing.T) {
	// lines <= 0 means print everything.
	lines := []string{
		`{"time":"2026-04-24T15:00:00Z","msg":"first"}`,
		`{"time":"2026-04-24T15:00:01Z","msg":"second"}`,
	}
	in := strings.NewReader(strings.Join(lines, "\n") + "\n")
	var out bytes.Buffer
	if err := tailPrint(in, 0, &out); err != nil {
		t.Fatalf("tailPrint: %v", err)
	}
	if !strings.Contains(out.String(), "first") || !strings.Contains(out.String(), "second") {
		t.Errorf("expected both lines, got: %s", out.String())
	}
}
