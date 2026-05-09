// Package verdictschema defines the shared tool-use schema used by every
// judge that invokes an LLM to produce a Verdict, plus the deterministic
// scoring function all judges share. Centralising both keeps verdict shape
// and scoring logic consistent across plan and quality judges.
package verdictschema

import (
	"encoding/json"
	"strings"

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
            "enum": ["critical", "high", "medium", "low"],
            "description": "critical/high are blocking; medium/low are advisory. Legacy P0/P1/P2/P3 strings are still accepted by the post-processor (NormalizeSeverity) for backward compatibility, but new judge calls should emit the canonical lowercase wording."
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
    },
    "checklist": {
      "type": "array",
      "description": "Per-acceptance-criteria audit. When the prompt provides a numbered AC list, emit ONE entry per AC item using the exact name (ac_1, ac_2, …) the prompt assigns. Set passed=true only when the diff actually satisfies the criterion — populate reason with concrete evidence (file:line). Set passed=false when the diff does not satisfy it AND populate reason with a deferral note (e.g. 'blocked on #N', 'needs upstream X'). An unpassed item with empty reason is treated as 'not done, no excuse' and blocks the verdict. Omit the array (or send []) when the prompt did not supply an AC list.",
      "items": {
        "type": "object",
        "properties": {
          "name": {"type": "string", "description": "Stable id from the AC list in the prompt (ac_1, ac_2, …). Echo it verbatim."},
          "passed": {"type": "boolean", "description": "true iff the diff satisfies this acceptance criterion."},
          "reason": {"type": "string", "description": "When passed=true: file:line evidence. When passed=false: why this item is being deferred. Empty when passed=false blocks the verdict."}
        },
        "required": ["name", "passed"],
        "additionalProperties": false
      }
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
		case state.SeverityCritical:
			penalty += weightP0
		case state.SeverityHigh:
			penalty += weightP1
		case state.SeverityMedium:
			penalty += weightP2
		case state.SeverityLow:
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
// set means the default (Critical and High — formerly P0/P1) applies.
//
// Both the map keys and each gap's stored severity are run through
// state.NormalizeSeverity, so vairdict.yaml configs that still spell the
// blocking set as ["P0","P1"] continue to gate the new "critical"/"high"
// gaps without any user-visible change.
func ApplyBlocking(gaps []state.Gap, blockOn map[string]bool) {
	if blockOn == nil {
		blockOn = map[string]bool{
			string(state.SeverityCritical): true,
			string(state.SeverityHigh):     true,
		}
	}
	normalized := make(map[state.Severity]bool, len(blockOn))
	for k, v := range blockOn {
		normalized[state.NormalizeSeverity(state.Severity(k))] = v
	}
	for i := range gaps {
		gaps[i].Blocking = normalized[state.NormalizeSeverity(gaps[i].Severity)]
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

// IsReflagged reports whether a gap is substantially similar to an
// already-acknowledged assumption. It uses bidirectional case-insensitive
// substring matching: if the assumption description appears in the gap
// description or vice versa, the gap is considered a re-flag.
func IsReflagged(gap state.Gap, acknowledged []state.Assumption) bool {
	if gap.Description == "" {
		return false
	}
	gapLower := strings.ToLower(gap.Description)
	for _, a := range acknowledged {
		if a.Description == "" {
			continue
		}
		aLower := strings.ToLower(a.Description)
		if strings.Contains(gapLower, aLower) || strings.Contains(aLower, gapLower) {
			return true
		}
	}
	return false
}

// ComputeScoreWithAcknowledged returns a deterministic score like
// ComputeScore, but halves the penalty for gaps that match an
// already-acknowledged assumption. This prevents re-flagged advisory
// gaps from dragging the score below the pass threshold on retries.
func ComputeScoreWithAcknowledged(gaps []state.Gap, acknowledged []state.Assumption) float64 {
	if len(acknowledged) == 0 {
		return ComputeScore(gaps)
	}

	penalty := 0.0
	for _, g := range gaps {
		w := 0.0
		switch g.Severity {
		case state.SeverityCritical:
			w = weightP0
		case state.SeverityHigh:
			w = weightP1
		case state.SeverityMedium:
			w = weightP2
		case state.SeverityLow:
			w = weightP3
		}
		if IsReflagged(g, acknowledged) {
			w /= 2
		}
		penalty += w
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
