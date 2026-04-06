package ui

// palette is a small set of ANSI escape sequences. We deliberately keep this
// minimal — no dependency on lipgloss/termenv — because vairdict's CLI output
// only needs a handful of colors and the SGR codes never change.
type palette struct {
	reset  string
	bold   string
	dim    string
	red    string // failures, blocking gaps
	green  string // success, scores ≥ 80
	yellow string // borderline scores 70-79
	blue   string // info accents
	cyan   string // headers
}

func defaultPalette() palette {
	return palette{
		reset:  "\033[0m",
		bold:   "\033[1m",
		dim:    "\033[2m",
		red:    "\033[31m",
		green:  "\033[32m",
		yellow: "\033[33m",
		blue:   "\033[34m",
		cyan:   "\033[36m",
	}
}

// accessiblePalette swaps red↔orange and green↔blue so red-green color
// vision deficiency users can still distinguish pass/fail/borderline.
// Orange (38;5;208) and a brighter blue replace the failure / success
// codes; yellow stays as the borderline marker.
func accessiblePalette() palette {
	return palette{
		reset:  "\033[0m",
		bold:   "\033[1m",
		dim:    "\033[2m",
		red:    "\033[38;5;208m", // orange — failure
		green:  "\033[38;5;33m",  // bright blue — success
		yellow: "\033[33m",       // yellow stays
		blue:   "\033[38;5;39m",
		cyan:   "\033[36m",
	}
}

// noColorPalette is a zero-valued palette: every escape is the empty string,
// so writing colored text becomes a plain string concatenation that costs
// nothing and prints nothing extra.
func noColorPalette() palette {
	return palette{}
}

func paletteFor(scheme ColorScheme) palette {
	switch scheme {
	case ColorsAccessible:
		return accessiblePalette()
	case ColorsNone:
		return noColorPalette()
	default:
		return defaultPalette()
	}
}

// scoreColor returns the palette color appropriate for a numeric score.
// >=80 is success, 70-79 is borderline, <70 is failure.
func (p palette) scoreColor(score float64) string {
	switch {
	case score >= 80:
		return p.green
	case score >= 70:
		return p.yellow
	default:
		return p.red
	}
}
