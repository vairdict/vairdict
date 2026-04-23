# VAIrdict

Development process engine that orchestrates and judges
AI-driven development across three phases: plan, code, and quality.

## Quick Context

- **Language:** Go 1.26 · module `github.com/vairdict/vairdict`
- **Current milestone:** M5 — Parallelism (see `plans/PROGRESS.md`)
- **Dogfood level:** full — every issue goes through `vairdict run`
- **State storage:** SQLite via `internal/state/`
- **CI:** GitHub Actions (`.github/workflows/`)

## What this is

VAIrdict runs tasks through three phases. Each phase has:
- A **producer** agent (creates/executes)
- A **judge** agent (scores, blocks, or passes)
- A **requeue loop** (max 3 attempts before human escalation)

### How a task flows

```
Intent → Plan Phase → [judge pass?] → Code Phase → [judge pass?] → Quality Phase → [judge pass?] → PR merged
              ↻ requeue (max 3)           ↻ requeue (max 3)             ↻ requeue (max 3)
                                                                              ↓ fail
                                                                        Escalation → Human
```

- The plan judge can issue a `ReturnTo` verdict that rewinds to a prior phase
- Failure context is propagated across rewinds (`state/rewind-context`)
- Concurrent tasks run in isolated git worktrees (`internal/workspace/`)

## Architecture

```
cmd/vairdict/          — CLI entrypoint (cobra)
  run.go               — main orchestration loop
  review.go            — `vairdict review <pr>` subcommand
  autovairdict.go      — auto-merge on passing verdict
  handle_comment.go    — @vairdict PR mention commands
  manifest.go          — manifest-based multi-task runs
internal/
  bootstrap/           — `vairdict init`, generates vairdict.yaml
  config/              — vairdict.yaml parsing + typed Config + overlays
  state/               — task state machine + SQLite + rewind logic
  phases/
    plan/              — plan phase orchestration
    code/              — code phase orchestration
    quality/           — quality phase orchestration
  agents/
    claude/            — Anthropic API client (direct API)
    claudecli/         — claude -p completer wrapper
    claudecode/        — Claude Code CLI runner (subprocess)
  judges/
    plan/              — plan judge: scores vs requirements
    code/              — code judge: calls spm ship, parses output
    quality/           — quality judge: e2e + intent check + inline review
    verdictschema/     — shared JSON schema for structured verdicts
  github/              — GitHub API, PRs, diff positioning, webhooks
  escalation/          — loop limit + human notification
  workspace/           — isolated git worktree per task
  deps/                — task dependency graph + priority queue
  conflicts/           — merge conflict detection between concurrent tasks
  standards/           — hardcoded non-negotiable engineering baselines
  ui/                  — CLI output, spinners, CI mode, JSON mode, log files
action/                — GitHub Action wrapper (marketplace)
skills/                — published to skillpkg registry
```

## CLI Subcommands

```
vairdict init                    — bootstrap vairdict.yaml in a repo
vairdict run "<intent>"          — run full plan→code→quality loop
vairdict run --manifest <file>   — run multiple tasks from manifest
vairdict review <pr-number>      — judge an existing PR
vairdict status                  — show current task state
vairdict version                 — print version
```

## Build Commands

```
make build    — build binary
make test     — run tests (go test ./...)
make lint     — golangci-lint run ./...
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

- Slack notifications (M7)
- Web UI dashboard (M8)
- Coder environment (M10)
- skillpkg runtime skill loading (M11)

Do not implement these before their milestone.

## Dogfood Level

This repo is built using VAIrdict itself. We are currently in **full dogfood**
(M3+): every issue goes through `vairdict run`, no PR merges without a
passing verdict, human only sees escalations and final PRs.

## Getting Oriented (new session checklist)

1. You're reading this file — good, you have the architecture
2. Read `plans/PROGRESS.md` — current milestone, what's in progress, what's ready
3. Run `git log --oneline -10` — see recent work and commit style
4. If picking up an issue: read it on GitHub (`gh issue view <n>`)
5. If continuing work: check the branch and `git diff` to see where you left off

## How to Work on VAIrdict

1. Find the first issue in "Ready to Start" in `plans/PROGRESS.md`
2. Read the full issue on GitHub — understand intent and acceptance criteria
3. Check dependencies — make sure they exist before starting
4. Write the code in the correct package per the architecture above
5. Write tests alongside the code — no exceptions
6. Move issue to "In Progress" in `plans/PROGRESS.md`
7. Open a PR linked to the issue
8. Move issue to "Done" when PR is merged
9. Move newly unblocked issues to "Ready to Start"

Do not start work on an issue that is still "Blocked" in `plans/PROGRESS.md`.
Do not exceed the scope defined in the issue.
Do not open a PR without a passing verdict (M3+).

## What NOT to do

- Do not commit directly to main — all code changes go through PRs
- Do not add dependencies without checking existing ones first
- Do not change vairdict.yaml schema without updating config/ parser
- Do not write judges that return raw strings — always typed structs
- Do not skip tests — every judge must have test coverage
- Do not implement features ahead of their milestone
- Do not merge a PR without a passing verdict (M3+)