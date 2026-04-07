# VAIrdict

Development process engine that orchestrates and judges
AI-driven development across three phases: plan, code, and quality.

## What this is

VAIrdict runs tasks through three phases. Each phase has:
- A producer agent (creates/executes)
- A judge agent (scores, blocks, or passes)
- A requeue loop (max 3 attempts before human escalation)

## Architecture

```
cmd/vairdict/        — CLI entrypoint (cobra)
internal/
  bootstrap/         — init flow, generates vairdict.yaml
  config/            — vairdict.yaml parsing + typed Config
  state/             — task state machine + SQLite
  phases/
    plan/            — plan phase orchestration
    code/            — code phase orchestration
    quality/         — quality phase orchestration
  agents/
    claude/          — Anthropic API client
    claudecode/      — Claude Code CLI runner
  judges/
    plan/            — plan judge: scores vs requirements
    code/            — code judge: calls spm ship, parses output
    quality/         — quality judge: e2e + intent check
  github/            — GitHub API, PRs, webhooks
  escalation/        — escalation logic
action/              — GitHub Action wrapper
skills/              — published to skillpkg registry
```

## Commands

```
make build    — build binary
make test     — run tests
make lint     — golangci-lint
make install  — go install
```

## Conventions

- Errors: `fmt.Errorf("context: %w", err)`
- Logging: `slog` structured logging
- No global state — Config struct passed everywhere
- Judge output is always typed structs, never parsed strings
- Every phase returns a typed Result with Score, Pass bool, Feedback

### Config overlays

`vairdict.yaml` is the base config used in every environment. Drop
`vairdict.<env>.yaml` files next to it (e.g. `vairdict.dev.yaml`,
`vairdict.ci.yaml`) and select one with `vairdict run --env <env>`.
The selected overlay is merged on top of the base via
`config.LoadConfigWithOverlay`; only fields actually set in the overlay
override the base — everything else is preserved.

`vairdict.ci.yaml` is auto-loaded when `CI=true` (set by GitHub Actions,
GitLab, CircleCI, …) — no flag needed.

Typical use: local dev runs `agents.judge: claude` (CLI fallback to API);
the CI overlay sets `agents.judge: claude-api` and
`escalation.notify_via: github`. Env names must be simple identifiers
(`[a-zA-Z0-9_-]+`) — no path traversal, no slashes.

## Key types to know

```go
type Task struct {
    ID          string
    Intent      string
    State       TaskState
    Phase       Phase
    LoopCount   map[Phase]int
    Assumptions []Assumption
    Attempts    []Attempt
}

type Verdict struct {
    Score     float64
    Pass      bool
    Gaps      []Gap
    Questions []Question
}

type Gap struct {
    Severity    Severity // P0, P1, P2, P3
    Description string
    Blocking    bool
}
```

## skillpkg / spm

The ship skill handles lint, format, tests, and build
in the code phase judge. Do not reimplement this logic
— call spm instead:

```
spm exec ship
```

spm must be installed in the environment.
ship skill must be installed: `spm install ship`

Do NOT add spm dependencies before M2 code phase work.

## Not yet integrated

- Slack (M7)
- Web UI (M8)
- Coder environment (M10)
- skillpkg runtime skill loading (M11)

Do not implement these before their milestone.

## Development Process

This repo is built using VAIrdict itself.
The process changes as VAIrdict becomes more capable:

**M0-M1: manual**
VAIrdict does not exist yet.
Agents work issues directly.
Human acts as judge on all PRs.

**M2: partial dogfood**
Plan + code phases exist.
Use `vairdict run` for M2 issues where possible.
Human judges quality phase manually.

**M3: first full loop**
Complete three-phase loop exists.
All M3 issues go through VAIrdict.
`vairdict run "<issue intent>"` is the only way to open a PR.

**M4 and beyond: full dogfood**
Every issue is planned, coded, and judged by VAIrdict.
Human only sees escalations and final PR.
No PR is merged without a passing VAIrdict verdict.

## How to Work on VAIrdict

1. Read CLAUDE.md first — understand the architecture
2. Read PROGRESS.md — find the first issue in "Ready to Start"
3. Read the full issue on GitHub — understand intent and acceptance criteria
4. Check dependencies — make sure they exist before starting
5. Write the code in the correct package per the architecture above
6. Write tests alongside the code — no exceptions
7. Update PROGRESS.md — move issue to "In Progress"
8. Open a PR linked to the issue
9. Update PROGRESS.md — move issue to "Done" when PR is merged
10. Check dependency graph — move newly unblocked issues to "Ready to Start"

Do not start work on an issue that is still "Blocked" in PROGRESS.md.
Do not exceed the scope defined in the issue.
Do not open a PR without a passing verdict (M3+).

## What NOT to do

- Do not add dependencies without checking existing ones first
- Do not change vairdict.yaml schema without updating config/ parser
- Do not write judges that return raw strings — always typed structs
- Do not skip tests — every judge must have test coverage
- Do not implement features ahead of their milestone
- Do not merge a PR without a passing verdict (M3+)