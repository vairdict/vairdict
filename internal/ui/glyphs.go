package ui

import "github.com/vairdict/vairdict/internal/state"

// glyphSet maps semantic icons to their unicode and ASCII forms. cli mode
// picks one map at construction based on the --ascii flag.
type glyphSet struct {
	logo       string
	plan       string
	code       string
	quality    string
	pass       string
	fail       string
	escalate   string
	blocking   string
	bullet     string
	rule       string
	prCreated  string
	branchIcon string
}

func unicodeGlyphs() glyphSet {
	return glyphSet{
		logo:       "⚖️",
		plan:       "📋",
		code:       "⌨️",
		quality:    "🔍",
		pass:       "✅",
		fail:       "❌",
		escalate:   "⚠️",
		blocking:   "⛔",
		bullet:     "•",
		rule:       "─",
		prCreated:  "✅",
		branchIcon: "↳",
	}
}

func asciiGlyphs() glyphSet {
	return glyphSet{
		logo:       "[V]",
		plan:       "[PLAN]",
		code:       "[CODE]",
		quality:    "[JUDGE]",
		pass:       "[OK]",
		fail:       "[FAIL]",
		escalate:   "[!]",
		blocking:   "[X]",
		bullet:     "*",
		rule:       "-",
		prCreated:  "[OK]",
		branchIcon: "->",
	}
}

func (g glyphSet) phase(p state.Phase) string {
	switch p {
	case state.PhasePlan:
		return g.plan
	case state.PhaseCode:
		return g.code
	case state.PhaseQuality:
		return g.quality
	}
	return g.bullet
}

func phaseTitle(p state.Phase) string {
	switch p {
	case state.PhasePlan:
		return "Plan phase"
	case state.PhaseCode:
		return "Code phase"
	case state.PhaseQuality:
		return "Quality phase"
	}
	return string(p)
}
