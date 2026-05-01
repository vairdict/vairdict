// Package main — completer.go resolves which LLM backend to use for the
// planner and judges (the "completer" roles, distinct from the "coder" role
// in internal/agents/claudecode which uses tools and edits the filesystem).
//
// Three values are accepted in vairdict.yaml under agents.planner /
// agents.judge (and the per-phase overrides agents.plan_judge,
// agents.code_judge, agents.quality_judge):
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
	"os/exec"

	"github.com/vairdict/vairdict/internal/agents/claude"
	"github.com/vairdict/vairdict/internal/agents/claudecli"
	"github.com/vairdict/vairdict/internal/config"
)

// completer is the narrow interface that the plan / quality judges and the
// plan phase's Planner all share. Both claude.Client and claudecli.Client
// satisfy it structurally. Model() is exposed so judges can stamp the
// verdict with the model that produced it without each call site
// having to plumb the resolved model through separately.
type completer interface {
	CompleteWithSystem(ctx context.Context, system, prompt string, target any) error
	CompleteWithTool(ctx context.Context, system, prompt string, tool claude.Tool, target any) error
	CompleteWithTools(ctx context.Context, system, prompt string, tools []claude.Tool, finalTool string, handlers map[string]claude.ToolHandler, target any) error
	Model() string
}

// completerRole names a completer slot in the orchestration pipeline.
// Each role reads its backend from a different field in agents.* (with
// per-phase judges falling back to agents.judge).
type completerRole string

const (
	rolePlanner      completerRole = "planner"
	rolePlanJudge    completerRole = "plan_judge"
	roleCodeJudge    completerRole = "code_judge"
	roleQualityJudge completerRole = "quality_judge"
)

// backendKind is the resolved backend identifier returned alongside the
// completer instance so it can be surfaced in CLI output and logs. Note
// this is the *resolved* kind — `claude` (smart) is never returned here;
// it has already collapsed to claude-cli or claude-api.
type backendKind string

const (
	backendClaudeCLI backendKind = "claude-cli" // local `claude -p`
	backendClaudeAPI backendKind = "claude-api" // HTTP API
)

// backendForRole reads the configured backend string for the given role
// from cfg.Agents, applying the per-phase fallback to Judge for the
// three judge slots.
func backendForRole(cfg *config.Config, role completerRole) string {
	switch role {
	case rolePlanner:
		return cfg.Agents.Planner
	case rolePlanJudge:
		return cfg.Agents.PlanJudgeBackend()
	case roleCodeJudge:
		return cfg.Agents.CodeJudgeBackend()
	case roleQualityJudge:
		return cfg.Agents.QualityJudgeBackend()
	default:
		return ""
	}
}

// modelForRole returns the model name the given role should pin. The
// planner shares the global agents.model — its output is consumed by
// a judge anyway, so swapping its model independently is out of scope
// for this knob. Each judge applies the
// {plan,code,quality}_judge_model → judge_model → model fallback so a
// user can grade plans with a stricter model than the one that
// produced them. Empty string means "let the underlying client pick
// its default".
func modelForRole(cfg *config.Config, role completerRole) string {
	switch role {
	case rolePlanner:
		return cfg.Agents.Model
	case rolePlanJudge:
		return cfg.Agents.PlanJudgeModelResolved()
	case roleCodeJudge:
		return cfg.Agents.CodeJudgeModelResolved()
	case roleQualityJudge:
		return cfg.Agents.QualityJudgeModelResolved()
	default:
		return ""
	}
}

// chooseBackend returns the resolved backend for the given config setting.
// `cliAvailable` is injected (via claudecli.IsAvailable in production) so
// the resolver is deterministic and unit-testable without touching PATH.
//
//	"", "claude" → claude-cli if PATH has it, else claude-api
//	"claude-cli" → claude-cli (caller errors later if PATH lookup fails)
//	"claude-api" → claude-api (caller errors later if no API key)
//	"auto"       → deprecated alias for "claude" — accepted with no warn
//
// roleName is included in the error message for unknown values so the
// user can see which slot needs fixing.
func chooseBackend(setting string, cliAvailable bool) (backendKind, error) {
	return chooseBackendForRole("agents.judge", setting, cliAvailable)
}

func chooseBackendForRole(roleName, setting string, cliAvailable bool) (backendKind, error) {
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
		return "", fmt.Errorf("unknown %s backend %q (want claude|claude-cli|claude-api)", roleName, setting)
	}
}

// resolveCompleter picks a backend for the given role per chooseBackend
// and constructs the matching client. The returned backendKind is
// informational (rendered as a `completer:` note in CLI mode). The
// model is resolved via modelForRole so a user can swap the judge
// model independently of the producer model — the override flows into
// the underlying client (claude.WithModel / claudecli.WithModel) so
// the actual API call uses it.
func resolveCompleter(cfg *config.Config, role completerRole) (completer, backendKind, error) {
	setting := backendForRole(cfg, role)
	kind, err := chooseBackendForRole("agents."+string(role), setting, claudecli.IsAvailable())
	if err != nil {
		return nil, "", err
	}

	model := modelForRole(cfg, role)

	switch kind {
	case backendClaudeCLI:
		// --dangerously-skip-permissions lets the subprocess call tools
		// without interactive prompts, matching the claudecode runner.
		opts := []claudecli.Option{
			claudecli.WithExtraArgs("--dangerously-skip-permissions"),
		}
		if model != "" {
			opts = append(opts, claudecli.WithModel(model))
		}
		return claudecli.New(opts...), kind, nil
	case backendClaudeAPI:
		// Judges must be deterministic — temperature=0 removes sampling
		// variance so the same (prompt, diff) pair produces the same verdict
		// structure across runs. The planner shares this client, which is
		// fine: its output is consumed by a judge anyway, so determinism
		// flows through the whole pipeline.
		opts := []claude.Option{claude.WithTemperature(0)}
		if model != "" {
			opts = append(opts, claude.WithModel(model))
		}
		c, err := claude.NewClient(cfg, opts...)
		if err != nil {
			return nil, "", fmt.Errorf("creating claude client: %w", err)
		}
		return c, kind, nil
	default:
		return nil, "", fmt.Errorf("unreachable backend kind: %s", kind)
	}
}

// backendProbes injects the side-effects validateBackends needs (PATH
// lookups, API key probe). Production wires these to exec.LookPath and
// config.ResolveAPIKey; tests set deterministic stubs.
type backendProbes struct {
	cliAvailable  func(name string) bool
	apiKeyPresent func() bool
}

// defaultBackendProbes returns probes that hit the real environment.
// Kept as a constructor so callers don't have to re-import os/exec or
// the config package just to do a pre-flight check.
func defaultBackendProbes() backendProbes {
	return backendProbes{
		cliAvailable: func(name string) bool {
			_, err := exec.LookPath(name)
			return err == nil
		},
		apiKeyPresent: func() bool {
			return config.ResolveAPIKey() != ""
		},
	}
}

// validateBackends walks every resolved completer role plus the coder
// and verifies that the chosen backend is actually usable: CLI binary
// on PATH for *-cli / family CLIs, API key configured for *-api.
//
// Smart defaults ("", "claude", "auto") never trigger errors — they
// fall through to whichever family is available at runtime. Validation
// only fires for explicit pinned backends so users who want VAIrdict
// to "just work" with whichever family they happen to have aren't
// blocked by missing alternatives.
//
// The function is pure: no network, no slog, no global state — just
// probes injected via the parameter so tests can drive it.
func validateBackends(cfg *config.Config, probes backendProbes) error {
	checks := []struct {
		name    string
		setting string
	}{
		{"agents.planner", cfg.Agents.Planner},
		{"agents.plan_judge", cfg.Agents.PlanJudgeBackend()},
		{"agents.code_judge", cfg.Agents.CodeJudgeBackend()},
		{"agents.quality_judge", cfg.Agents.QualityJudgeBackend()},
	}
	for _, c := range checks {
		if err := validateCompleterBackend(c.name, c.setting, probes); err != nil {
			return err
		}
	}
	return validateCoderBackend(cfg.Agents.Coder, probes)
}

// validateCompleterBackend covers the planner / judge slots which all
// share the same {claude, claude-cli, claude-api, auto} taxonomy.
func validateCompleterBackend(roleName, setting string, probes backendProbes) error {
	switch setting {
	case "", "claude", "auto":
		// Smart default — let runtime fall through to whichever family
		// is available. AC explicitly requires no error here.
		return nil
	case "claude-cli":
		if !probes.cliAvailable("claude") {
			return fmt.Errorf("%s: claude-cli requires the `claude` binary on PATH", roleName)
		}
		return nil
	case "claude-api":
		if !probes.apiKeyPresent() {
			return fmt.Errorf("%s: claude-api requires ANTHROPIC_API_KEY (env var or ~/.config/vairdict/config.yaml)", roleName)
		}
		return nil
	default:
		return fmt.Errorf("%s: unknown backend %q (want claude|claude-cli|claude-api)", roleName, setting)
	}
}

// validateCoderBackend covers agents.coder, which today only supports
// claude-code (the family CLI runner used by internal/agents/claudecode).
// Listed separately because the taxonomy is different from the completer
// slots — claude-code is the only legal value, and it requires the
// `claude` binary to be on PATH.
func validateCoderBackend(setting string, probes backendProbes) error {
	switch setting {
	case "", "claude-code":
		if !probes.cliAvailable("claude") {
			return fmt.Errorf("agents.coder: claude-code requires the `claude` binary on PATH")
		}
		return nil
	default:
		return fmt.Errorf("agents.coder: unknown backend %q (want claude-code)", setting)
	}
}
