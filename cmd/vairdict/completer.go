// Package main — completer.go resolves which LLM backend to use for the
// planner and judges (the "completer" roles, distinct from the "coder" role
// in internal/agents/claudecode which uses tools and edits the filesystem).
//
// Three values are accepted in vairdict.yaml under agents.planner /
// agents.judge:
//
//	claude      — smart default: try claude-cli, fall back to claude-api
//	claude-cli  — strict local subprocess (errors if `claude` not on PATH)
//	claude-api  — strict HTTP API client (errors if no API key configured)
//
// The bare value `claude` exists so future families (gpt, gemini, …) can
// follow the same convention: bare = smart default for the family, suffixed
// = explicit transport.
package main

import (
	"context"
	"fmt"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/agents/claudecli"
	"github.com/vairdict/vairdict/internal/config"
)

// completer is the narrow interface that the plan / quality judges and the
// plan phase's Planner all share. Both claude.Client and claudecli.Client
// satisfy it structurally.
type completer interface {
	CompleteWithSystem(ctx context.Context, system, prompt string, target any) error
	CompleteWithTool(ctx context.Context, system, prompt string, tool claude.Tool, target any) error
}

// backendKind is the resolved backend identifier returned alongside the
// completer instance so it can be surfaced in CLI output and logs. Note
// this is the *resolved* kind — `claude` (smart) is never returned here;
// it has already collapsed to claude-cli or claude-api.
type backendKind string

const (
	backendClaudeCLI backendKind = "claude-cli" // local `claude -p`
	backendClaudeAPI backendKind = "claude-api" // HTTP API
)

// chooseBackend returns the resolved backend for the given config setting.
// `cliAvailable` is injected (via claudecli.IsAvailable in production) so
// the resolver is deterministic and unit-testable without touching PATH.
//
//	"", "claude" → claude-cli if PATH has it, else claude-api
//	"claude-cli" → claude-cli (caller errors later if PATH lookup fails)
//	"claude-api" → claude-api (caller errors later if no API key)
//	"auto"       → deprecated alias for "claude" — accepted with no warn
func chooseBackend(setting string, cliAvailable bool) (backendKind, error) {
	switch setting {
	case "", "claude", "auto":
		if cliAvailable {
			return backendClaudeCLI, nil
		}
		return backendClaudeAPI, nil
	case "claude-cli":
		return backendClaudeCLI, nil
	case "claude-api":
		return backendClaudeAPI, nil
	default:
		return "", fmt.Errorf("unknown agents.judge backend %q (want claude|claude-cli|claude-api)", setting)
	}
}

// resolveCompleter picks a backend per chooseBackend and constructs the
// matching client. The returned backendKind is informational (rendered as
// a `completer:` note in CLI mode).
func resolveCompleter(cfg *config.Config) (completer, backendKind, error) {
	kind, err := chooseBackend(cfg.Agents.Judge, claudecli.IsAvailable())
	if err != nil {
		return nil, "", err
	}

	switch kind {
	case backendClaudeCLI:
		// --dangerously-skip-permissions lets the subprocess call tools
		// without interactive prompts, matching the claudecode runner.
		return claudecli.New(
			claudecli.WithExtraArgs("--dangerously-skip-permissions"),
		), kind, nil
	case backendClaudeAPI:
		// Judges must be deterministic — temperature=0 removes sampling
		// variance so the same (prompt, diff) pair produces the same verdict
		// structure across runs. The planner shares this client, which is
		// fine: its output is consumed by a judge anyway, so determinism
		// flows through the whole pipeline.
		c, err := claude.NewClient(cfg, claude.WithTemperature(0))
		if err != nil {
			return nil, "", fmt.Errorf("creating claude client: %w", err)
		}
		return c, kind, nil
	default:
		return nil, "", fmt.Errorf("unreachable backend kind: %s", kind)
	}
}
