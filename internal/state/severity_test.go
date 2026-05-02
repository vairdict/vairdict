package state

import "testing"

// TestSeverityCanonicalConstants pins the canonical severity ladder used
// across the codebase from this point on: Critical / High / Medium / Low.
// The string values are lowercase and stable — they are written to the DB,
// surfaced in PR comments, and referenced from vairdict.yaml — so any future
// change to them must be a deliberate, migration-aware refactor.
func TestSeverityCanonicalConstants(t *testing.T) {
	cases := []struct {
		got  Severity
		want string
	}{
		{SeverityCritical, "critical"},
		{SeverityHigh, "high"},
		{SeverityMedium, "medium"},
		{SeverityLow, "low"},
	}
	for _, c := range cases {
		if string(c.got) != c.want {
			t.Errorf("severity constant %q: want %q, got %q", c.want, c.want, string(c.got))
		}
	}
}

// TestSeverityLegacyAliases verifies that the deprecated SeverityP0..P3
// constants still exist (so existing code keeps compiling during the
// migration) and resolve to the same string values as the new canonical
// constants. They must be drop-in equal.
func TestSeverityLegacyAliases(t *testing.T) {
	pairs := []struct {
		legacy, canonical Severity
		name              string
	}{
		{SeverityP0, SeverityCritical, "P0->Critical"},
		{SeverityP1, SeverityHigh, "P1->High"},
		{SeverityP2, SeverityMedium, "P2->Medium"},
		{SeverityP3, SeverityLow, "P3->Low"},
	}
	for _, p := range pairs {
		if p.legacy != p.canonical {
			t.Errorf("%s: legacy %q != canonical %q — deprecated aliases must equal their replacement", p.name, p.legacy, p.canonical)
		}
	}
}

// TestNormalizeSeverity covers the on-read shim that maps legacy "P0".."P3"
// strings (and any case-variant of the canonical names) to the canonical
// lowercase form. The DB store and the verdict-schema unmarshaller call
// this so old payloads transparently flow into the new ladder.
func TestNormalizeSeverity(t *testing.T) {
	cases := []struct {
		in   Severity
		want Severity
	}{
		// legacy -> canonical
		{"P0", SeverityCritical},
		{"P1", SeverityHigh},
		{"P2", SeverityMedium},
		{"P3", SeverityLow},
		// case variants of legacy
		{"p0", SeverityCritical},
		{"p3", SeverityLow},
		// canonical passes through
		{"critical", SeverityCritical},
		{"high", SeverityHigh},
		{"medium", SeverityMedium},
		{"low", SeverityLow},
		// case variants of canonical
		{"Critical", SeverityCritical},
		{"HIGH", SeverityHigh},
		// unknown values pass through unchanged so callers can flag them
		{"bogus", "bogus"},
		{"", ""},
	}
	for _, c := range cases {
		got := NormalizeSeverity(c.in)
		if got != c.want {
			t.Errorf("NormalizeSeverity(%q): want %q, got %q", c.in, c.want, got)
		}
	}
}

// TestSeverityIsBlocking encodes the new gate semantics: Critical and High
// block; Medium and Low do not. This replaces the implicit "P0/P1 are
// blocking" rule scattered across judges with a single source of truth.
func TestSeverityIsBlocking(t *testing.T) {
	cases := []struct {
		s    Severity
		want bool
	}{
		{SeverityCritical, true},
		{SeverityHigh, true},
		{SeverityMedium, false},
		{SeverityLow, false},
		// legacy strings normalize to the same answer
		{"P0", true},
		{"P1", true},
		{"P2", false},
		{"P3", false},
		// unknown severities are not blocking; the post-processor flags them
		{"bogus", false},
	}
	for _, c := range cases {
		if got := c.s.IsBlocking(); got != c.want {
			t.Errorf("Severity(%q).IsBlocking() = %v, want %v", c.s, got, c.want)
		}
	}
}

// TestSeverityDisplay covers the user-facing rendering of a severity.
// Stored values are lowercase ("critical"); rendered output uses Title
// Case ("Critical") so PR comment tables and inline comments read as
// English words rather than enum identifiers. Legacy "P0".."P3" inputs
// render under their canonical names so old DB rows display correctly.
func TestSeverityDisplay(t *testing.T) {
	cases := []struct {
		s    Severity
		want string
	}{
		{SeverityCritical, "Critical"},
		{SeverityHigh, "High"},
		{SeverityMedium, "Medium"},
		{SeverityLow, "Low"},
		// legacy
		{"P0", "Critical"},
		{"P1", "High"},
		{"P2", "Medium"},
		{"P3", "Low"},
		// case variants
		{"critical", "Critical"},
		{"HIGH", "High"},
		// unknown severities pass through unchanged so renderers can
		// surface whatever the judge produced rather than silently
		// substituting a real severity word.
		{"weird", "weird"},
		{"", ""},
	}
	for _, c := range cases {
		if got := c.s.Display(); got != c.want {
			t.Errorf("Severity(%q).Display() = %q, want %q", c.s, got, c.want)
		}
	}
}

// TestSeverityRank pins the ordering used when sorting gaps for display.
// Critical first, then High, Medium, Low. The notes section uses this to
// surface Medium above Low; the inline section uses it implicitly because
// only Critical and High are eligible.
func TestSeverityRank(t *testing.T) {
	cases := []struct {
		s    Severity
		rank int
	}{
		{SeverityCritical, 0},
		{SeverityHigh, 1},
		{SeverityMedium, 2},
		{SeverityLow, 3},
		// legacy normalizes
		{"P0", 0},
		{"P3", 3},
		// unknown severities sort last
		{"bogus", 99},
	}
	for _, c := range cases {
		if got := c.s.Rank(); got != c.rank {
			t.Errorf("Severity(%q).Rank() = %d, want %d", c.s, got, c.rank)
		}
	}
}
