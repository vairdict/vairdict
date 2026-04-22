// Package verdictschema defines the shared tool-use schema used by every
// judge that invokes an LLM to produce a Verdict, plus the deterministic
// scoring function all judges share. Centralising both keeps verdict shape
// and scoring logic consistent across plan and quality judges.
package verdictschema

import (
	"encoding/json"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/state"
)

// ToolName is the name of the single tool every judge exposes to the model.
// Forcing a specific tool via tool_choice guarantees a structured response.
const ToolName = "submit_verdict"

// inputSchema is the JSON Schema the model must conform to when invoking the
// submit_verdict tool. It intentionally omits "score", "pass", and per-gap
// "blocking" — those are computed deterministically from severities after the
// model returns, so the model never estimates them.
const inputSchema = `{
  "type": "object",
  "properties": {
    "summary": {
      "type": "string",
      "description": "Markdown-ish narrative rendered under the phase header. Use the section structure described in the system prompt."
    },
    "gaps": {
      "type": "array",
      "description": "Concrete findings. Each gap maps to one row in the verdict comment.",
      "items": {
        "type": "object",
        "properties": {
          "severity": {
            "type": "string",
            "enum": ["P0", "P1", "P2", "P3"],
            "description": "P0/P1 are blocking; P2/P3 are advisory."
          },
          "description": {"type": "string"},
          "file": {"type": "string", "description": "Optional file path when the gap maps to a specific diff location."},
          "line": {"type": "integer", "description": "Optional line number in the new file when the gap maps to a specific diff location."},
          "suggestion": {"type": "string", "description": "Optional replacement code for the line(s) at file:line. When provided, rendered as a GitHub suggestion block that the author can apply with one click. Only set when you can offer a concrete, correct fix — omit for design concerns or when the fix is non-trivial."}
        },
        "required": ["severity", "description"],
        "additionalProperties": false
      }
    },
    "questions": {
      "type": "array",
      "description": "Genuine uncertainties that cannot be stated as a finding.",
      "items": {
        "type": "object",
        "properties": {
          "text": {"type": "string"},
          "priority": {"type": "string", "enum": ["high", "medium", "low"]}
        },
        "required": ["text", "priority"],
        "additionalProperties": false
      }
    },
    "return_to": {
      "type": "string",
      "enum": ["", "code", "plan", "escalate"],
      "description": "Quality-judge-only. On a failing verdict, the phase the outer loop should rewind to. Omit or leave empty on passing verdicts and on judges that do not drive cross-phase routing."
    }
  },
  "required": ["summary", "gaps", "questions"],
  "additionalProperties": false
}`

// VerdictTool returns the tool definition passed to CompleteWithTool.
func VerdictTool(description string) claude.Tool {
	return claude.Tool{
		Name:        ToolName,
		Description: description,
		InputSchema: json.RawMessage(inputSchema),
	}
}

// Severity weights for deterministic scoring. P0 and P1 are blocking, so even
// a single occurrence forces a clear fail.
const (
	weightP0 = 40.0
	weightP1 = 20.0
	weightP2 = 10.0
	weightP3 = 5.0
)

// ComputeScore returns a deterministic score in [0, 100] from the gap list.
// Every gap contributes a fixed penalty by severity; the model never sets the
// score itself. Blocking severities (P0/P1) produce the largest penalties so a
// single blocker pushes the score well below any reasonable threshold.
func ComputeScore(gaps []state.Gap) float64 {
	penalty := 0.0
	for _, g := range gaps {
		switch g.Severity {
		case state.SeverityP0:
			penalty += weightP0
		case state.SeverityP1:
			penalty += weightP1
		case state.SeverityP2:
			penalty += weightP2
		case state.SeverityP3:
			penalty += weightP3
		}
	}
	score := 100.0 - penalty
	switch {
	case score < 0:
		return 0
	case score > 100:
		return 100
	default:
		return score
	}
}

// ApplyBlocking sets the Blocking flag on each gap based on severity, using
// the caller-supplied set of severities that count as blocking. Passing a nil
// set means the default (P0 and P1) applies.
func ApplyBlocking(gaps []state.Gap, blockOn map[string]bool) {
	if blockOn == nil {
		blockOn = map[string]bool{
			string(state.SeverityP0): true,
			string(state.SeverityP1): true,
		}
	}
	for i := range gaps {
		gaps[i].Blocking = blockOn[string(gaps[i].Severity)]
	}
}

// HasBlockingGap reports whether any gap in the list is marked blocking.
func HasBlockingGap(gaps []state.Gap) bool {
	for _, g := range gaps {
		if g.Blocking {
			return true
		}
	}
	return false
}
