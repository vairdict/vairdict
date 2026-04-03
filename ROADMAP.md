# VAIrdict Roadmap

## Milestone 0 — Infrastructure
> Set up the repo so Claude can start working immediately

- [ ] #9  chore: add .gitignore for Go
- [ ] #10 chore: go mod init + empty directory scaffold
- [ ] #11 chore: add Makefile (build, test, lint, install)
- [ ] #12 chore: add golangci-lint config
- [ ] #13 chore: add GoReleaser config
- [ ] spm installed and available in environment
- [ ] ship skill installed: spm install ship

## Milestone 1 — Foundation
> Build the core primitives everything else depends on

- [ ] #1 bootstrap: init flow + vairdict.yaml generation
- [ ] #2 config: yaml parsing + typed Config struct
- [ ] #3 state: task state machine + SQLite
- [ ] #4 agents/claude: Anthropic API client + structured output
- [ ] #5 judges/plan: plan judge + severity scoring
- [ ] #6 phases/plan: plan phase orchestration
- [ ] #7 cmd: cobra CLI (init, run, status, version)
- [ ] #8 dogfood: vairdict init on vairdict repo itself

## Milestone 2 — Code Phase
> Agents write code, judge verifies it

- [ ] agents/claudecode: Claude Code CLI runner
- [ ] judges/code: lint/test/build judge
- [ ] phases/code: code phase orchestration
- [ ] github: PR creation + comments
- [ ] dogfood: plan + code phases end to end on vairdict

## Milestone 3 — Quality Phase
> Full three-phase loop working end to end

- [ ] judges/quality: e2e + intent check vs original task
- [ ] phases/quality: quality phase orchestration
- [ ] escalation: loop limit + human notification
- [ ] full loop: plan → code → quality → PR
- [ ] requeue: phase-level and cross-phase routing
- [ ] dogfood: first full task run on vairdict itself

## Milestone 4 — Distribution
> Get VAIrdict in front of developers

- [ ] GitHub Action published to marketplace
- [ ] install script: curl -fsSL vairdict.dev/install | sh
- [ ] brew tap: brew install vairdict
- [ ] GoReleaser: binaries for Mac/Linux/Windows
- [ ] vairdict.com landing page live
- [ ] email signup for early access
- [ ] Show HN post
- [ ] dev.to article: "we built this tool using itself"

## Milestone 5 — Early Users
> Validate with real teams

- [ ] 3-5 friends/colleagues using it
- [ ] feedback loop: weekly calls
- [ ] basic Slack notifications (escalations only)
- [ ] skillpkg: judge-plan skill published to registry
- [ ] skillpkg: judge-code skill published to registry
- [ ] skillpkg: judge-quality skill published to registry
- [ ] ProductHunt launch
- [ ] "Built with VAIrdict" badge program

## Milestone 6 — Parallelism
> Multiple agents per phase, multiple tasks simultaneously

- [ ] parallel agent spawning per phase
- [ ] task dependency graph (task B blocks on task A)
- [ ] parallel PR handling
- [ ] merge conflict detection and resolution between agents
- [ ] task queue: priority, ordering, dependencies
- [ ] performance: 5+ concurrent tasks without degradation

## Milestone 7 — Slack App (full)
> Slack as the primary entry point for teams

- [ ] Slack app: task submission via @vairdict
- [ ] Slack app: phase status updates per task
- [ ] Slack app: escalation with context + approve/reject
- [ ] Slack app: vairdict status in channel
- [ ] Slack app directory submission
- [ ] natural language task input: no structured format required

## Milestone 8 — Web UI
> Visibility into what agents are doing

- [ ] board view: tasks flowing through phases
- [ ] judge scores visible per phase
- [ ] agent activity log per task
- [ ] loop history: what failed, what was tried
- [ ] escalation management dashboard
- [ ] assumption log: what P2s agents assumed
- [ ] "why did this fail 3 times" report
- [ ] vairdict.com/dashboard

## Milestone 9 — Team Features
> Multiple users, roles, projects

- [ ] multiple users per organization
- [ ] role-based access: who gets escalations, who can approve
- [ ] global org vairdict.yaml + project-level overrides
- [ ] task assignment: which human gets which escalations
- [ ] audit log: every agent action, every verdict
- [ ] team analytics: tasks completed, loop rates, escalation rate

## Milestone 10 — Coder Integration
> Isolated cloud environments per agent

- [ ] Coder as environment option in vairdict.yaml
- [ ] isolated workspace per agent, per task
- [ ] workspace templates for vairdict published to Coder registry
- [ ] agent permissions scoped per workspace
- [ ] Coder registry module published
- [ ] environment: local | github-actions | coder all working

## Milestone 11 — skillpkg Deep Integration
> VAIrdict as first-class skillpkg consumer and contributor

- [ ] VAIrdict pulls judge skills via spm at runtime
- [ ] skills versioned independently of VAIrdict core
- [ ] community skill contributions: custom judges
- [ ] skill: judge-pr (standalone, works without full VAIrdict)
- [ ] skill: severity-score
- [ ] skill: requeue-on-failure
- [ ] skill: plan-writer
- [ ] skill: assumption-logger
- [ ] VAIrdict dogfoods skillpkg for its own skills

## Milestone 12 — Advanced Judging
> Judges that learn and improve over time

- [ ] custom judge rules per project in vairdict.yaml
- [ ] judge learns from human overrides (you rejected a pass → why?)
- [ ] historical verdict analytics per project
- [ ] cross-task pattern detection (same failure recurring)
- [ ] judge confidence scores (how certain is the verdict)
- [ ] pluggable judge models (Claude | GPT-4 | custom)
- [ ] judge explainability: why did this score 73%?

## Milestone 13 — Monetization
> Sustainable business model

- [ ] free tier: 10 tasks/month, one repo
- [ ] pro tier: unlimited tasks, one repo, $X/month
- [ ] team tier: unlimited tasks, multiple repos, multiple users
- [ ] enterprise tier: self-hosted, Coder, SSO, audit logs
- [ ] usage dashboard: tasks, tokens, cost per task
- [ ] billing via Stripe
- [ ] fair use policy for free tier

## Milestone 14 — Platform
> VAIrdict as an ecosystem

- [ ] third-party agent plugins (Codex, Gemini CLI, Aider)
- [ ] public API: trigger tasks, read verdicts, webhooks
- [ ] VAIrdict action for Linear (trigger from issue)
- [ ] VAIrdict action for Jira (trigger from ticket)
- [ ] community judge marketplace
- [ ] "Built with VAIrdict" public dashboard
- [ ] VAIrdict runs VAIrdict for all external contributions

---

## Timeline

| Milestones | Period        | Theme                    |
|------------|---------------|--------------------------|
| M1 - M3    | Month 1 - 2   | Build the core loop      |
| M4 - M5    | Month 3       | Launch + first users     |
| M6 - M8    | Month 4 - 6   | Scale + visibility       |
| M9 - M11   | Month 7 - 9   | Teams + ecosystem        |
| M12 - M14  | Month 10 - 12 | Intelligence + platform  |

---

## Principles

**Judge everything** — every phase transition is gated by a judge, not just the final output.

**Language agnostic** — vairdict.yaml defines how to build/test/lint. VAIrdict never imports your code.

**Pluggable agents** — Claude Code is the default. Codex, Gemini, or any CLI agent can replace it.

**Dogfood first** — every VAIrdict feature is built using VAIrdict itself before being shipped.

**Skills over monolith** — judge behaviors are skills published to skillpkg. Anyone can contribute or override them.

**Human at the edges** — human provides intent at the start, reviews PR at the end. Everything in between is VAIrdict.