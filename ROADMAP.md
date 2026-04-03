# VAIrdict Roadmap

Each milestone has a clear definition of done.
Issues are linked — click to see full spec and acceptance criteria.
Progress is tracked in [PROGRESS.md](./PROGRESS.md).

---

## Milestone 0 — Infrastructure
> Set up the repo so agents can start implementing immediately.

**Definition of done:**
- `make build` runs clean
- `make test` runs clean
- `make lint` runs clean
- Empty directory structure matches CLAUDE.md architecture
- spm installed, ship skill available

**Issues:**
- [ ] #9 chore: repo infrastructure setup

---

## Milestone 1 — Foundation
> Build the core primitives everything else depends on.

**Definition of done:**
- All issues closed
- `make build` passes clean
- `make test` passes clean
- `vairdict init` runs on this repo and generates valid vairdict.yaml
- `vairdict version` prints version string

**Issues (in dependency order):**
- [ ] #2 config: vairdict.yaml parsing + typed Config struct
- [ ] #3 state: task state machine + SQLite persistence
- [ ] #4 agents/claude: Anthropic API client + structured output
- [ ] #1 bootstrap: init flow + vairdict.yaml generation
- [ ] #5 judges/plan: plan judge + severity scoring
- [ ] #6 phases/plan: plan phase orchestration
- [ ] #7 cmd: cobra CLI (init, run, status, version)
- [ ] #8 dogfood: run vairdict init on vairdict repo itself

---

## Milestone 2 — Code Phase
> Agents write code, judge verifies it using the ship skill.

**Definition of done:**
- All issues closed
- Plan phase + code phase run end to end on a real task
- spm ship skill called successfully from code judge
- PR opened automatically on passing code phase

**Issues:**
- [ ] agents/claudecode: Claude Code CLI runner
- [ ] judges/code: calls spm ship, parses output, returns verdict
- [ ] phases/code: code phase orchestration
- [ ] github: PR creation + comments via GitHub API
- [ ] dogfood: plan + code phases end to end on one vairdict task

---

## Milestone 3 — Quality Phase
> Full three-phase loop working end to end.

**Definition of done:**
- All issues closed
- Full loop runs: plan → code → quality → PR
- Requeue working: failed phase routes back correctly
- Escalation working: human notified after 3 loops
- Dogfood: first complete task run on vairdict itself

**Issues:**
- [ ] judges/quality: e2e + intent check vs original task
- [ ] phases/quality: quality phase orchestration
- [ ] escalation: loop limit + human notification
- [ ] requeue: cross-phase routing logic
- [ ] dogfood: first full three-phase task on vairdict

---

## Milestone 4 — Distribution
> Get VAIrdict in front of developers with zero friction.

**Definition of done:**
- GitHub Action published and installable
- `curl -fsSL vairdict.dev/install | sh` works on Mac + Linux
- `brew install vairdict` works
- vairdict.com live with email signup
- First external person has run VAIrdict successfully

**Issues:**
- [ ] action: GitHub Action wrapper published to marketplace
- [ ] release: GoReleaser + brew tap + install script
- [ ] web: vairdict.com landing page + email signup
- [ ] docs: README with quickstart + dogfooding story
- [ ] launch: Show HN post + dev.to article

---

## Milestone 5 — Early Users
> Validate with real teams outside your own repo.

**Definition of done:**
- 5 external repos running VAIrdict
- Feedback collected from each
- At least one skill published to skillpkg registry
- ProductHunt launched

**Issues:**
- [ ] feedback: outreach to 5 teams, collect structured feedback
- [ ] skills: judge-plan published to skillpkg registry
- [ ] skills: judge-code published to skillpkg registry
- [ ] skills: judge-quality published to skillpkg registry
- [ ] slack: basic escalation notifications only
- [ ] launch: ProductHunt + "Built with VAIrdict" badge

---

## Milestone 6 — Parallelism
> Multiple agents per phase, multiple tasks simultaneously.

**Definition of done:**
- 3+ tasks running in parallel without interference
- Dependency graph respected (task B waits for task A)
- Merge conflicts detected and handled
- No performance degradation at 5 concurrent tasks

**Issues:**
- [ ] parallel: agent spawning per phase
- [ ] deps: task dependency graph
- [ ] queue: priority ordering + dependency resolution
- [ ] conflicts: merge conflict detection between agents
- [ ] perf: load test 5 concurrent tasks

---

## Milestone 7 — Slack App (full)
> Slack as the primary entry point for engineering teams.

**Definition of done:**
- Task submitted via @vairdict in Slack
- Phase updates posted to Slack automatically
- Escalations sent to configured channel with context
- Slack app listed in directory

**Issues:**
- [ ] slack/intake: task submission via @vairdict
- [ ] slack/updates: phase status updates
- [ ] slack/escalation: escalation with approve/reject
- [ ] slack/status: vairdict status command in Slack
- [ ] slack/publish: submit to Slack app directory

---

## Milestone 8 — Web UI
> Visibility into what agents are doing without reading logs.

**Definition of done:**
- Board view shows tasks flowing through phases
- Judge scores visible per phase per task
- Loop history readable for any task
- Escalation manageable from web UI
- vairdict.com/dashboard live

**Issues:**
- [ ] ui/board: kanban-style task board
- [ ] ui/scores: judge scores + verdict details
- [ ] ui/history: loop history + assumption log
- [ ] ui/escalation: escalation management
- [ ] ui/deploy: vairdict.com/dashboard live

---

## Milestone 9 — Team Features
> Multiple users, roles, and projects.

**Definition of done:**
- Multiple users can belong to one organization
- Escalations route to correct person by role
- Org-level vairdict.yaml + project overrides working
- Audit log captures every agent action

**Issues:**
- [ ] teams/users: multi-user organizations
- [ ] teams/roles: role-based access control
- [ ] teams/config: org-level + project-level yaml merge
- [ ] teams/routing: escalation routing by role
- [ ] teams/audit: full audit log of agent activity
- [ ] teams/analytics: tasks, loop rates, escalation rates

---

## Milestone 10 — Coder Integration
> Isolated cloud environments per agent.

**Definition of done:**
- Coder selectable as environment in vairdict.yaml
- Each agent gets isolated workspace per task
- Coder registry module published
- local | github-actions | coder all working

**Issues:**
- [ ] coder/env: Coder as environment option
- [ ] coder/workspace: isolated workspace per agent
- [ ] coder/permissions: scoped permissions per workspace
- [ ] coder/registry: module published to Coder registry
- [ ] coder/docs: setup guide for Coder users

---

## Milestone 11 — skillpkg Deep Integration
> VAIrdict as a first-class skillpkg consumer and contributor.

**Definition of done:**
- VAIrdict pulls judge skills via spm at runtime
- Skills versioned independently of VAIrdict core
- Community can contribute custom judge skills
- All built-in judge logic extractable as skills

**Issues:**
- [ ] spm/runtime: pull skills via spm at runtime
- [ ] spm/versioning: skill versions in vairdict.yaml
- [ ] spm/community: custom judge skill interface
- [ ] skills/judge-pr: standalone PR judge skill
- [ ] skills/severity-score: severity scoring skill
- [ ] skills/requeue: requeue-on-failure skill
- [ ] skills/plan-writer: plan writer skill
- [ ] skills/assumption-logger: assumption logger skill

---

## Milestone 12 — Advanced Judging
> Judges that learn and improve over time.

**Definition of done:**
- Custom judge rules definable per project
- Human overrides captured and influence future verdicts
- Historical analytics per project available
- Judge explains its reasoning clearly

**Issues:**
- [ ] judge/custom: custom rules in vairdict.yaml
- [ ] judge/learning: capture human overrides
- [ ] judge/analytics: historical verdict data
- [ ] judge/patterns: cross-task failure patterns
- [ ] judge/confidence: confidence scores per verdict
- [ ] judge/explainability: why did this score X?
- [ ] judge/pluggable: swap judge model in vairdict.yaml

---

## Milestone 13 — Monetization
> Sustainable business model.

**Definition of done:**
- Free tier live and enforced
- Pro tier live with Stripe billing
- Team tier live
- Usage dashboard showing tasks/tokens/cost

**Issues:**
- [ ] billing/stripe: Stripe integration
- [ ] billing/tiers: free | pro | team | enterprise
- [ ] billing/usage: usage dashboard
- [ ] billing/limits: free tier enforcement
- [ ] billing/enterprise: self-hosted license

---

## Milestone 14 — Platform
> VAIrdict as an ecosystem.

**Definition of done:**
- Public API live and documented
- Linear + Jira integrations working
- Community judge marketplace live
- Third-party agent plugins (Codex, Gemini) working

**Issues:**
- [ ] platform/api: public REST API
- [ ] platform/linear: trigger from Linear issue
- [ ] platform/jira: trigger from Jira ticket
- [ ] platform/marketplace: community judge marketplace
- [ ] platform/agents: Codex + Gemini CLI plugins
- [ ] platform/badge: "Built with VAIrdict" public dashboard

---

## Timeline

| Milestone | Period       | Theme                   |
|-----------|--------------|-------------------------|
| M0        | Week 1       | Infrastructure          |
| M1        | Week 1-2     | Foundation              |
| M2        | Week 3-4     | Code phase              |
| M3        | Week 5-6     | Full loop               |
| M4        | Week 7-8     | Distribution            |
| M5        | Week 9-10    | Early users             |
| M6        | Month 4-5    | Parallelism             |
| M7        | Month 5-6    | Slack                   |
| M8        | Month 6      | Web UI                  |
| M9        | Month 7      | Teams                   |
| M10       | Month 8      | Coder                   |
| M11       | Month 8-9    | skillpkg                |
| M12       | Month 9-10   | Advanced judging        |
| M13       | Month 10-11  | Monetization            |
| M14       | Month 11-12  | Platform                |

---

## Principles

**Judge everything** — every phase transition is gated by a judge, not just the final output.

**Language agnostic** — vairdict.yaml defines how to build/test/lint. VAIrdict never imports your code.

**Pluggable agents** — Claude Code is the default. Codex, Gemini, or any CLI agent can replace it.

**Dogfood first** — every VAIrdict feature is built using VAIrdict itself before being shipped.

**Skills over monolith** — judge behaviors are skills published to skillpkg. Anyone can contribute or override them.

**Human at the edges** — human provides intent at the start, reviews PR at the end. Everything in between is VAIrdict.