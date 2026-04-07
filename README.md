# VAIrdict

VAIrdict is a development process engine that orchestrates 
and judges AI-driven development across three phases: 
plan, code, and quality.

> This repository is built by the same system it implements.
> Every PR goes through VAIrdict's three-layer process.

## How it works
```
HUMAN: writes intent
         ↓
PLAN PHASE
├── Planner: expands intent into requirements + plan
└── Judge: scores plan, flags gaps, blocks if <85% coverage
         ↓
CODE PHASE
├── Coder: implements plan step by step
└── Judge: lint, test, build, scope check
         ↓
QUALITY PHASE
├── Reviewer: documents, e2e, integration tests
└── Judge: full system check vs original intent
         ↓
OUTPUT: verified, production-ready PR
```

After 3 failed loops → escalated to human.

## Quick start
```bash
# Install
curl -fsSL vairdict.dev/install | sh

# Initialize in your repo
vairdict init

# Run a task
vairdict run "add forgot password flow"

# Check status
vairdict status
```

## Agent roles & backends

Each phase has its own agent role with a different job. They are
configured separately under `agents:` in `vairdict.yaml`.

```yaml
agents:
  planner: claude       # generates the plan from the intent
  coder:   claude-code  # writes the code
  judge:   claude       # scores plan, code, and quality
```

There are two **kinds** of agent roles, and they accept different values:

### Completer roles — `planner`, `judge`

Stateless: prompt in, structured JSON out, no tools, no file edits.
Used by the planner and the three judges. Accepted values:

| value        | meaning |
|--------------|---------|
| `claude`     | smart default — try local CLI, fall back to HTTP API |
| `claude-cli` | strict: shell out to the local `claude` binary (errors if not on PATH) |
| `claude-api` | strict: HTTP call against api.anthropic.com (errors if no API key) |

`claude-cli` is the zero-auth path: if you have the `claude` binary
installed and logged in, vairdict reuses your subscription session — no
API key needed. CI environments without an interactive login set
`claude-api` (or use the `vairdict.ci.yaml` overlay).

### Coder role — `coder`

Agentic: reads files, edits files, runs shell commands, runs tests.
This is fundamentally different from a completer — the output is **side
effects on your working directory**, not a JSON struct. Currently the
only supported value is:

| value         | meaning |
|---------------|---------|
| `claude-code` | the local Claude Code agent (the `claude` binary in agentic mode) |

`claude-code` and `claude-cli` happen to invoke the same `claude`
binary, but they use it for entirely different jobs: `claude-cli` runs
it as a one-shot JSON function (`claude -p --output-format json`),
while `claude-code` runs it as a long-running autonomous agent that
modifies your filesystem (`claude -p --dangerously-skip-permissions`).
There is no HTTP equivalent of `claude-code` because no HTTP endpoint
edits your filesystem and runs your tests for you.

## How agents use this repo

See [AGENTS.md](./AGENTS.md) for how the three-layer
agent team operates on this codebase.

## Status

🚧 Active development — built by VAIrdict itself.

## License

MIT