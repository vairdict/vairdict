# VAIrdict

Development process engine that orchestrates and judges 
AI-driven development across three phases: plan, code, quality.

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
    code/            — code judge: lint/test/build
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

## What NOT to do

- Do not add dependencies without checking existing ones first
- Do not change vairdict.yaml schema without updating config/ parser
- Do not write judges that return raw strings — always typed structs
- Do not skip tests — every judge must have test coverage