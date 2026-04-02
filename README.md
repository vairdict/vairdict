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

## How agents use this repo

See [AGENTS.md](./AGENTS.md) for how the three-layer 
agent team operates on this codebase.

## Status

🚧 Active development — built by VAIrdict itself.

## License

MIT