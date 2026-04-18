<p align="center">
  <img src="assets/logo.png" alt="VAIrdict" height="80">
</p>

<h3 align="center">Development process engine that judges AI-driven development</h3>

<p align="center">
  <a href="https://github.com/vairdict/vairdict/releases/latest"><img src="https://img.shields.io/github/v/release/vairdict/vairdict?label=release" alt="Latest Release"></a>
  <a href="https://github.com/vairdict/vairdict/actions/workflows/release.yml"><img src="https://github.com/vairdict/vairdict/actions/workflows/release.yml/badge.svg" alt="Release"></a>
  <a href="https://github.com/vairdict/vairdict/actions/workflows/vairdict.yml"><img src="https://github.com/vairdict/vairdict/actions/workflows/vairdict.yml/badge.svg" alt="VAIrdict Review"></a>
</p>

---

VAIrdict runs tasks through three judged phases — **plan**, **code**, and **quality** — with automatic requeue on failure and human escalation after 3 loops. Every phase transition is gated by a judge, not just the final output.

```
HUMAN: writes intent
         |
   PLAN PHASE
   |-- Planner: expands intent into requirements + plan
   '-- Judge: scores plan, flags gaps, blocks if incomplete
         |
   CODE PHASE
   |-- Coder: implements plan step by step
   '-- Judge: lint, test, build, scope check
         |
   QUALITY PHASE
   |-- Reviewer: checks diff against original intent
   '-- Judge: full system check vs original intent
         |
   OUTPUT: verified, production-ready PR
```

After 3 failed loops in any phase, the task escalates to a human.

## Install

**curl (Mac / Linux):**
```bash
curl -fsSL https://raw.githubusercontent.com/vairdict/vairdict/main/scripts/install.sh | sh
```

**Go:**
```bash
go install github.com/vairdict/vairdict/cmd/vairdict@latest
```

**GitHub Action** (review every PR automatically):
```yaml
# .github/workflows/vairdict.yml
name: VAIrdict Review
on:
  pull_request:
    types: [opened, synchronize, reopened]
permissions:
  contents: read
  pull-requests: write
  issues: read
jobs:
  review:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: vairdict/vairdict@main
        with:
          anthropic-api-key: ${{ secrets.ANTHROPIC_API_KEY }}
```

## Quickstart

```bash
# Initialize in your repo — generates vairdict.yaml
vairdict init

# Run a full plan -> code -> quality loop
vairdict run "add forgot password flow"

# Or run from a GitHub issue
vairdict run --issue 42

# Judge an existing PR without the full loop
vairdict review 73
```

## Configuration

VAIrdict is configured via `vairdict.yaml` at the repo root. `vairdict init` generates one for you.

```yaml
project:
  name: my-app
  language: go

agents:
  planner: claude       # generates the plan
  coder:   claude-code  # writes the code
  judge:   claude       # scores every phase

commands:
  build: make build
  test:  make test
  lint:  make lint

phases:
  plan:
    max_loops: 3
  code:
    max_loops: 3
  quality:
    max_loops: 3
    e2e_required: false
```

### Environment overlays

Drop `vairdict.<env>.yaml` next to the base config to override settings per environment:

```bash
vairdict run --env ci "deploy the thing"
```

`vairdict.ci.yaml` is auto-loaded when `CI=true`. Typical use: local dev uses `claude` (CLI fallback), CI uses `claude-api` with an API key.

### Agent backends

**Completer roles** (`planner`, `judge`) — stateless prompt-in, JSON-out:

| Value | Meaning |
|-------|---------|
| `claude` | Smart default — try local CLI, fall back to HTTP API |
| `claude-cli` | Local `claude` binary only (no API key needed) |
| `claude-api` | HTTP API only (requires `ANTHROPIC_API_KEY`) |

**Coder role** — agentic, edits files, runs commands:

| Value | Meaning |
|-------|---------|
| `claude-code` | Claude Code agent in autonomous mode |

## Architecture

```
cmd/vairdict/        CLI entrypoint (cobra)
internal/
  phases/
    plan/            Plan phase orchestration
    code/            Code phase orchestration
    quality/         Quality phase orchestration
  judges/
    plan/            Plan judge: scores vs requirements
    code/            Code judge: lint, test, build
    quality/         Quality judge: diff vs intent
  agents/
    claude/          Anthropic API client
    claudecli/       Claude CLI wrapper
    claudecode/      Claude Code agent runner
  github/            PR creation, verdict comments
  escalation/        Loop limit + human notification
  config/            vairdict.yaml parsing
  state/             Task state machine + SQLite
```

## Built with VAIrdict

This repository is built by the same system it implements. Every PR goes through VAIrdict's three-phase process.

| Milestone | What happened |
|-----------|--------------|
| **M0-M1** | VAIrdict doesn't exist yet — human judges all PRs manually |
| **M2** | Plan + code phases working — agents write code, ship skill judges it |
| **M3** | Full loop — quality judge gates every PR, escalation works, first dogfood task runs end-to-end |
| **M4** | Distribution — GoReleaser, GitHub Action, auto-review on every PR to this repo |

From M3 onwards, `vairdict run` is the only way to open a PR. No PR merges without a passing verdict. The judge has caught real bugs during development — including shell injection vulnerabilities in the GitHub Action that were fixed in the same review loop.

## Contributing

Read [CLAUDE.md](./CLAUDE.md) for architecture, conventions, and how to pick up issues. Progress is tracked in [PROGRESS.md](./plans/PROGRESS.md).

## License

MIT
