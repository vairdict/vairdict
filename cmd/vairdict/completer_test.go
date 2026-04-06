package main

import "testing"

func TestChooseBackend(t *testing.T) {
	cases := []struct {
		name         string
		setting      string
		cliAvailable bool
		haveAPIKey   bool
		want         backendKind
		wantErr      bool
	}{
		// Auto: prefer CLI when available and no API key.
		{"auto cli only", "", true, false, backendClaudeCLI, false},
		{"auto alias cli only", "auto", true, false, backendClaudeCLI, false},
		// Auto: if API key is present, prefer HTTP even if CLI is there,
		// so CI runs are deterministic and scripted usage is unchanged.
		{"auto with api key", "", true, true, backendClaude, false},
		// Auto: no CLI → fall back to HTTP even without an API key.
		// claude.NewClient will then fail with a clear AuthError.
		{"auto no cli no key", "", false, false, backendClaude, false},
		{"auto no cli with key", "", false, true, backendClaude, false},
		// Explicit overrides.
		{"explicit claude", "claude", true, false, backendClaude, false},
		{"explicit claude-cli", "claude-cli", false, true, backendClaudeCLI, false},
		// Unknown value is a hard error so typos don't silently pick the
		// wrong backend.
		{"unknown", "openai", true, false, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseBackend(tc.setting, tc.cliAvailable, tc.haveAPIKey)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
