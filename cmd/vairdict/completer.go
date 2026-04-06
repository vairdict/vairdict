// Package main — completer.go resolves which LLM backend to use for the
// planner and judges. Both the HTTP claude.Client and the local claudecli
// wrapper satisfy the same structural interface (CompleteWithSystem); the
// call-sites in run.go are typed against `completer` so either can be
// injected. The resolver prefers the local `claude` CLI when it is on PATH
// and no API key is configured — this is the zero-auth local-dev path.
// In CI, or when a user explicitly sets agents.judge, the resolver honors
// that choice.
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
}

// backendKind is the resolved backend identifier returned alongside the
// completer instance so it can be surfaced in CLI output and logs.
type backendKind string

const (
	backendClaude    backendKind = "claude"     // HTTP API
	backendClaudeCLI backendKind = "claude-cli" // local `claude -p`
)

// chooseBackend returns the backend that should be used given the user's
// agents.judge setting and the local environment. It does not construct any
// client; it only decides which one to build.
//
//	"", "auto"    → claude-cli if available and no API key set, else claude
//	"claude"      → claude (HTTP)
//	"claude-cli"  → claude-cli (local)
//	anything else → error
//
// The `cliAvailable` and `haveAPIKey` parameters are injected so the choice
// is deterministic and unit-testable without touching PATH or env.
func chooseBackend(setting string, cliAvailable bool, haveAPIKey bool) (backendKind, error) {
	switch setting {
	case "", "auto":
		if cliAvailable && !haveAPIKey {
			return backendClaudeCLI, nil
		}
		return backendClaude, nil
	case "claude":
		return backendClaude, nil
	case "claude-cli":
		return backendClaudeCLI, nil
	default:
		return "", fmt.Errorf("unknown agents.judge backend %q (want auto|claude|claude-cli)", setting)
	}
}

// resolveCompleter picks a backend per chooseBackend and constructs the
// matching client. The returned backendKind is informational (rendered as
// a `completer:` note in CLI mode).
func resolveCompleter(cfg *config.Config) (completer, backendKind, error) {
	kind, err := chooseBackend(
		cfg.Agents.Judge,
		claudecli.IsAvailable(),
		config.ResolveAPIKey() != "",
	)
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
	case backendClaude:
		c, err := claude.NewClient(cfg)
		if err != nil {
			return nil, "", fmt.Errorf("creating claude client: %w", err)
		}
		return c, kind, nil
	default:
		return nil, "", fmt.Errorf("unreachable backend kind: %s", kind)
	}
}
