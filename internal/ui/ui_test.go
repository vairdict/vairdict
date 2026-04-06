package ui

import (
	"bytes"
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/vairdict/vairdict/internal/state"
)

func TestParseMode(t *testing.T) {
	cases := []struct {
		in      string
		want    Mode
		wantErr bool
	}{
		{"", "", false},
		{"cli", ModeCLI, false},
		{"ci", ModeCI, false},
		{"json", ModeJSON, false},
		{"html", "", true},
	}
	for _, tc := range cases {
		got, err := ParseMode(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseMode(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("ParseMode(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestParseColorScheme(t *testing.T) {
	cases := []struct {
		in      string
		want    ColorScheme
		wantErr bool
	}{
		{"", "", false},
		{"default", ColorsDefault, false},
		{"accessible", ColorsAccessible, false},
		{"no-color", ColorsNone, false},
		{"rainbow", "", true},
	}
	for _, tc := range cases {
		got, err := ParseColorScheme(tc.in)
		if (err != nil) != tc.wantErr {
			t.Errorf("ParseColorScheme(%q) err=%v wantErr=%v", tc.in, err, tc.wantErr)
		}
		if got != tc.want {
			t.Errorf("ParseColorScheme(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestNewAutoDetectsMode(t *testing.T) {
	// TTY → cli
	var buf bytes.Buffer
	r := New(Options{Out: &buf, IsTTY: true})
	if _, ok := r.(*cliRenderer); !ok {
		t.Errorf("expected cliRenderer for TTY, got %T", r)
	}
	// non-TTY → ci
	r = New(Options{Out: &buf, IsTTY: false})
	if _, ok := r.(*ciRenderer); !ok {
		t.Errorf("expected ciRenderer for non-TTY, got %T", r)
	}
	// explicit json
	r = New(Options{Out: &buf, Mode: ModeJSON, IsTTY: true})
	if _, ok := r.(*jsonRenderer); !ok {
		t.Errorf("expected jsonRenderer, got %T", r)
	}
}

func TestNewColorAutoDetect(t *testing.T) {
	var buf bytes.Buffer
	// TTY without NO_COLOR → default palette (non-empty escape)
	r := New(Options{Out: &buf, IsTTY: true}).(*cliRenderer)
	if r.pal.reset == "" {
		t.Error("expected non-empty reset on default palette")
	}
	// NO_COLOR env forces no-color even on TTY
	r = New(Options{Out: &buf, IsTTY: true, NoColorEnv: true}).(*cliRenderer)
	if r.pal.reset != "" {
		t.Error("NO_COLOR should force empty palette")
	}
}

func TestCLIRendererWritesOutput(t *testing.T) {
	var buf bytes.Buffer
	r := New(Options{Out: &buf, Mode: ModeCLI, Colors: ColorsNone})
	r.RunStart("abc123", "fix bug", "/tmp/log")
	r.PhaseStart(state.PhasePlan)
	r.PhaseLoop(state.PhasePlan, 1, 3, 92, true)
	r.PhaseDone(state.PhasePlan, OutcomePass, 92, 1, "## Decided\n- a thing", nil)
	r.RunComplete("abc123")
	_ = r.Close()

	out := buf.String()
	for _, want := range []string{"abc123", "fix bug", "/tmp/log", "Plan phase", "Loop 1/3", "Decided", "a thing"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q\n---\n%s", want, out)
		}
	}
}

func TestJSONRendererEmitsValidJSON(t *testing.T) {
	var buf bytes.Buffer
	r := New(Options{Out: &buf, Mode: ModeJSON})
	r.RunStart("abc", "intent", "/tmp/x.log")
	r.PhaseLoop(state.PhaseCode, 2, 3, 75.5, false)
	r.PhaseDone(state.PhaseCode, OutcomeFail, 75.5, 2, "", []state.Gap{{Severity: state.SeverityP1, Description: "oops"}})
	r.Error(errors.New("boom"))

	dec := json.NewDecoder(&buf)
	for dec.More() {
		var m map[string]any
		if err := dec.Decode(&m); err != nil {
			t.Fatalf("invalid JSON: %v", err)
		}
		if _, ok := m["event"]; !ok {
			t.Errorf("missing event field: %v", m)
		}
	}
}

func TestASCIIGlyphSwap(t *testing.T) {
	var buf bytes.Buffer
	r := New(Options{Out: &buf, Mode: ModeCLI, Colors: ColorsNone, ASCII: true}).(*cliRenderer)
	if r.glyphs.logo != "[V]" {
		t.Errorf("ascii logo = %q, want [V]", r.glyphs.logo)
	}
	r.RunStart("t1", "intent", "")
	if strings.Contains(buf.String(), "⚖") {
		t.Error("ASCII mode should not emit unicode emoji")
	}
}

func TestPaletteSelection(t *testing.T) {
	if paletteFor(ColorsDefault).red != "\033[31m" {
		t.Error("default palette red wrong")
	}
	if paletteFor(ColorsAccessible).red != "\033[38;5;208m" {
		t.Error("accessible palette should use orange for red")
	}
	if paletteFor(ColorsNone).red != "" {
		t.Error("no-color palette should have empty escapes")
	}
}

func TestScoreColor(t *testing.T) {
	p := defaultPalette()
	if p.scoreColor(90) != p.green {
		t.Error("90 should be green")
	}
	if p.scoreColor(75) != p.yellow {
		t.Error("75 should be yellow")
	}
	if p.scoreColor(50) != p.red {
		t.Error("50 should be red")
	}
}
