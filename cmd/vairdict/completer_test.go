package main

import "testing"

func TestChooseBackend(t *testing.T) {
	cases := []struct {
		name         string
		setting      string
		cliAvailable bool
		want         backendKind
		wantErr      bool
	}{
		// Default (empty / "claude"): try cli, fall back to api.
		{"default cli available", "", true, backendClaudeCLI, false},
		{"default no cli", "", false, backendClaudeAPI, false},
		{"claude cli available", "claude", true, backendClaudeCLI, false},
		{"claude no cli", "claude", false, backendClaudeAPI, false},
		// "auto" is a deprecated alias for "claude" — accepted silently.
		{"auto cli available", "auto", true, backendClaudeCLI, false},
		{"auto no cli", "auto", false, backendClaudeAPI, false},
		// Strict modes — chooseBackend itself doesn't gate on availability.
		// claude-cli failing on a host without claude is reported by the
		// callsite when the subprocess actually runs.
		{"strict cli", "claude-cli", false, backendClaudeCLI, false},
		{"strict cli with cli", "claude-cli", true, backendClaudeCLI, false},
		{"strict api", "claude-api", true, backendClaudeAPI, false},
		{"strict api no cli", "claude-api", false, backendClaudeAPI, false},
		// Typos surface as a hard error so users don't silently get the
		// wrong backend.
		{"unknown value", "openai", true, "", true},
		{"old http alias rejected", "claude-http", true, "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := chooseBackend(tc.setting, tc.cliAvailable)
			if (err != nil) != tc.wantErr {
				t.Fatalf("err=%v wantErr=%v", err, tc.wantErr)
			}
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
