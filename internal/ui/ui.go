// Package ui owns all human-facing terminal output for vairdict.
//
// Three rendering modes are supported via the Renderer interface:
//
//   - cli  — colored, sectioned, emoji-decorated output for interactive use
//   - ci   — slog-style structured logs for non-interactive runs (CI / pipes)
//   - json — one JSON object per event for programmatic consumption
//
// The default mode is auto-detected: cli if stdout is a TTY, otherwise ci.
//
// Callers (cmd/vairdict/run.go) construct a Renderer via New(opts) and call
// methods like PhaseStart, PhaseLoop, PhaseDone instead of using fmt.Println
// or slog directly. This keeps presentation concerns out of orchestration
// code and makes it possible to test the rendered output in isolation.
package ui

import (
	"fmt"
	"io"

	"github.com/vairdict/vairdict/internal/state"
)

// Mode is the high-level output style.
type Mode string

const (
	ModeCLI  Mode = "cli"
	ModeCI   Mode = "ci"
	ModeJSON Mode = "json"
)

// ParseMode validates a user-supplied mode string. An empty input means
// "auto" — the caller decides based on TTY detection.
func ParseMode(s string) (Mode, error) {
	switch Mode(s) {
	case "":
		return "", nil
	case ModeCLI, ModeCI, ModeJSON:
		return Mode(s), nil
	default:
		return "", fmt.Errorf("unknown output mode %q (want: cli, ci, json)", s)
	}
}

// ColorScheme controls ANSI coloring in cli mode. The accessible scheme uses
// blue/orange/yellow instead of red/green for users with red-green color
// vision deficiency. NoColor disables ANSI escapes entirely.
type ColorScheme string

const (
	ColorsDefault    ColorScheme = "default"
	ColorsAccessible ColorScheme = "accessible"
	ColorsNone       ColorScheme = "no-color"
)

// ParseColorScheme validates a user-supplied color flag. An empty input
// means "auto" — the caller decides based on TTY detection and the NO_COLOR
// environment variable.
func ParseColorScheme(s string) (ColorScheme, error) {
	switch ColorScheme(s) {
	case "":
		return "", nil
	case ColorsDefault, ColorsAccessible, ColorsNone:
		return ColorScheme(s), nil
	default:
		return "", fmt.Errorf("unknown colors %q (want: default, accessible, no-color)", s)
	}
}

// PhaseOutcome describes how a phase exited. Renderers use this to pick
// the right glyph and colour and to decide whether to print a gaps block.
type PhaseOutcome int

const (
	OutcomePass PhaseOutcome = iota
	OutcomeFail
	OutcomeEscalate
	OutcomeRequeueToCode
)

// Options holds the construction parameters for a Renderer.
type Options struct {
	Mode        Mode
	Colors      ColorScheme
	ASCII       bool
	IsTTY       bool // whether Out is a terminal
	NoColorEnv  bool // value of NO_COLOR env (if set, force ColorsNone)
	TerminalCol int  // 0 means unknown / unbounded
	Out         io.Writer
}

// Renderer is the abstract surface every output mode must satisfy. Methods
// are intentionally chunky (one per logical event) so each implementation
// can choose its own representation.
type Renderer interface {
	// RunStart prints the run header (task id, intent, log file path).
	RunStart(taskID, intent, logPath string)

	// Note prints a low-priority informational line (e.g. "Branch: foo").
	Note(label, value string)

	// PhaseStart prints the section header for a phase.
	PhaseStart(phase state.Phase)

	// PhaseLoop prints one loop's progress line within a phase.
	PhaseLoop(phase state.Phase, loop, max int, score float64, pass bool)

	// PhaseDone prints the closing summary block for a phase: outcome,
	// score, loops, narrative summary (if any), and gaps (if any).
	PhaseDone(
		phase state.Phase,
		outcome PhaseOutcome,
		score float64,
		loops int,
		summary string,
		gaps []state.Gap,
	)

	// PRCreated prints the success line for PR creation.
	PRCreated(url string)

	// VerdictPosted prints the success line for posting the verdict comment.
	VerdictPosted(score float64, pass bool)

	// Escalation prints the escalation block, used when a phase fails
	// after max loops or returns RequeueToCode.
	Escalation(taskID string, phase state.Phase, loops int, score float64, gaps []state.Gap)

	// RunComplete prints the final success line.
	RunComplete(taskID string)

	// Error prints an error block (rendered red in cli mode).
	Error(err error)

	// Close flushes any buffered output. Always safe to call.
	Close() error
}

// New constructs a Renderer for the given options. If Mode is empty, it
// auto-detects: cli for TTYs, ci otherwise. If Colors is empty, it
// auto-detects: ColorsNone if not a TTY or NO_COLOR is set, otherwise
// ColorsDefault.
func New(opts Options) Renderer {
	if opts.Out == nil {
		panic("ui.New: Out writer is required")
	}

	mode := opts.Mode
	if mode == "" {
		if opts.IsTTY {
			mode = ModeCLI
		} else {
			mode = ModeCI
		}
	}

	colors := opts.Colors
	if colors == "" {
		if !opts.IsTTY || opts.NoColorEnv {
			colors = ColorsNone
		} else {
			colors = ColorsDefault
		}
	}
	if opts.NoColorEnv {
		colors = ColorsNone
	}

	switch mode {
	case ModeCLI:
		return newCLIRenderer(opts.Out, colors, opts.ASCII)
	case ModeJSON:
		return newJSONRenderer(opts.Out)
	default:
		return newCIRenderer(opts.Out)
	}
}
