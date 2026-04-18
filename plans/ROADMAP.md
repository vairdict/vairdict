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
- [x] #9 chore: repo infrastructure setup

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
- [x] #2 config: vairdict.yaml parsing + typed Config struct
- [x] #3 state: task state machine + SQLite persistence
- [x] #4 agents/claude: Anthropic API client + structured output
- [x] #1 bootstrap: init flow + vairdict.yaml generation
- [x] #5 judges/plan: plan judge + severity scoring
- [x] #6 phases/plan: plan phase orchestration
- [x] #7 cmd: cobra CLI (init, run, status, version)
- [x] #8 dogfood: run vairdict init on vairdict repo itself

---

## Milestone 2 — Code Phase
> Agents write code, judge verifies it using the ship skill.

**Definition of done:**
- All issues closed
- Plan phase + code phase run end to end on a real task
- spm ship skill called successfully from code judge
- PR opened automatically on passing code phase

**Issues:**
- [x] #19 agents/claudecode: Claude Code CLI runner
- [x] #20 judges/code: calls spm ship, parses output, returns verdict
- [x] #21 phases/code: code phase orchestration
- [x] #22 github: PR creation + comments via GitHub API
- [x] #23 dogfood: plan + code phases end to end on one vairdict task

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
- [x] #32 judges/quality: e2e + intent check vs original task
- [x] #33 phases/quality: quality phase orchestration
- [x] #34 escalation: loop limit + human notification
- [x] #38 github/verdict: post structured judge verdict as PR comment
- [ ] #48 cmd: `vairdict review <pr>` — judge an existing PR
- [ ] #51 test: orchestration coverage for runTask + runQualityPhase
- [ ] #36 dogfood: first full three-phase task on vairdict

---

## Milestone 4 — Distribution
> Ship VAIrdict so it can run on every commit, in any repo, without hand-holding.

**Definition of done:**
- Tagged releases (`v0.0.x`) built by GoReleaser for Mac + Linux
- `curl -fsSL <install-url> | sh` works on Mac + Linux
- GitHub Action published and installable from the marketplace
- Auto-review runs the judge on every PR push and posts the verdict as a comment
- Auto-merge gates merges on a passing verdict
- README documents quickstart + dogfooding story

**Out of scope (deferred to Early Users):**
- brew tap / `brew install vairdict`
- vairdict.com landing page + email signup
- Show HN / dev.to / ProductHunt launch posts

**Issues:**
- [ ] #39 cmd/auto-vairdict: auto-merge on passing verdict
- [ ] release: GoReleaser + signed `v0.0.x` artifacts + install script
- [ ] action: GitHub Action wrapper published to marketplace
- [ ] action/auto-review: run quality judge on every PR push, post verdict comment
- [ ] docs: README quickstart + dogfooding story

---

## Milestone 5 — Parallelism
> Multiple agents per phase, multiple tasks simultaneously, isolated workspaces.

**Definition of done:**
- 3+ tasks running in parallel without interference
- Each task runs in its own isolated workspace (git worktree or equivalent)
- Dependency graph respected (task B waits for task A)
- Merge conflicts detected and handled
- No performance degradation at 5 concurrent tasks

**Issues:**
- [ ] #72 judge/review: inline PR review comments on specific diff lines
- [ ] workspace: isolated workspace per task (git worktree)
- [ ] parallel: agent spawning per phase
- [ ] deps: task dependency graph
- [ ] queue: priority ordering + dependency resolution
- [ ] conflicts: merge conflict detection between agents
- [ ] perf: load test 5 concurrent tasks

---

## Milestone 6 — Pluggable Agents
> Claude Code is the default. Any CLI agent can replace it.

**Definition of done:**
- Codex CLI usable as a completer backend
- Gemini CLI usable as a completer backend
- Backend selectable per phase in vairdict.yaml
- Judge model swappable independently of completer model
- Auto-resolver picks the best available backend like the existing claude resolver

**Issues:**
- [ ] agents/codex: Codex CLI completer
- [ ] agents/gemini: Gemini CLI completer
- [ ] config: per-phase backend selection in vairdict.yaml
- [ ] judge/pluggable: swap judge model in vairdict.yaml
- [ ] resolver: extend auto backend resolver to all agents
- [ ] docs: agent backend selection guide

---

## Milestone 7 — skillpkg Deep Integration
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
- [ ] skills/judge-plan: standalone plan judge skill
- [ ] skills/judge-code: standalone code judge skill
- [ ] skills/judge-quality: standalone quality judge skill
- [ ] skills/judge-pr: standalone PR judge skill

---

## Milestone 8 — Learning Foundation
> VAIrdict remembers what went wrong and surfaces it next time.

Design reference: [LEARNING-SYSTEM.md](./LEARNING-SYSTEM.md) · Detailed scope: [LEARNING-ROADMAP.md](./LEARNING-ROADMAP.md) milestones 1a + 1b.

**Definition of done:**
- Content-entry schema and stats-record schema locked in
- Multi-entry content file format (`<!-- LEARNING -->` marker) implemented
- Machine-tier storage at `~/.vairdict/learnings/` working
- Config under `learnings:` key in `vairdict.yaml`
- CLI commands: `vairdict learning add/list/show/remove/edit/set-grade`
- `LearnableSignal` extension to `Gap` struct
- Automated write path: trigger detection → Planner → Coder → Judge (fresh context)
- Retrieval for Planner role with adaptive K, Jaccard similarity, token budget
- Batch-and-flush stats infrastructure in place

**Issues:**
- [ ] learning/schema: content entry + stats record schemas
- [ ] learning/parser: multi-entry file format with `<!-- LEARNING -->` marker
- [ ] learning/storage: machine-tier file layout + manifest/index generation
- [ ] learning/config: `learnings:` section in vairdict.yaml
- [ ] learning/cli: `vairdict learning` subcommands (add, list, show, remove, edit, set-grade)
- [ ] learning/signal: `LearnableSignal` extension to `Gap` struct
- [ ] learning/write: automated write path (trigger detection + three-agent sub-phase)
- [ ] learning/retrieve: relevance scoring + adaptive K retrieval for Planner
- [ ] learning/stats: batch-and-flush stats update infrastructure
- [ ] learning/dogfood: 10+ manual learnings created, 2+ auto-generated from real tasks

---

## Milestone 9 — Learning Repo Tier
> Learnings become team artifacts reviewed in PRs.

Design reference: [LEARNING-ROADMAP.md](./LEARNING-ROADMAP.md) milestone 2.

**Definition of done:**
- Repo-tier storage at `.vairdict/learnings/` (content git-tracked)
- Stats via `vairdict-bot` commits (direct or dedicated branch)
- Learnings land in PRs alongside code changes
- `@vairdict` PR commands: add/drop/revise/set-grade learnings
- `vairdict promote-learning` for machine → repo promotion
- All three roles (Planner, Coder, Judge) consume learnings
- Token budget accounting per role

**Issues:**
- [ ] learning/repo-tier: repo-level file layout + manifest
- [ ] learning/bot-commits: stats commit strategy with retry-on-conflict
- [ ] learning/three-role: extend retrieval to Coder and Judge with role-specific weights
- [ ] learning/pr-flow: learnings in PR description + `@vairdict` comment commands
- [ ] learning/promote: `vairdict promote-learning` CLI command
- [ ] learning/triggers: inner-loop rejections, planning iteration, human `add-learning`

---

## Milestone 10 — Learning Monorepo Support
> Multi-service monorepos without cross-service noise.

Design reference: [LEARNING-ROADMAP.md](./LEARNING-ROADMAP.md) milestone 3.

**Definition of done:**
- `repo_type: monorepo | single-service` config working
- Scope-based file layout: `_shared/`, `_repo/`, per-service dirs
- Retrieval respects scope boundaries
- CODEOWNERS integration documented

**Issues:**
- [ ] learning/monorepo-config: `repo_type` + `services` config
- [ ] learning/monorepo-layout: scope-based directory structure
- [ ] learning/monorepo-retrieval: scope-aware filtering
- [ ] learning/monorepo-codeowners: CODEOWNERS integration

---

## Milestone 11 — Judge Intelligence
> Judges get smarter and more transparent — custom rules, confidence, explainability.

**Definition of done:**
- Custom judge rules definable per project in vairdict.yaml
- Historical verdict analytics per project available
- Confidence scores on every verdict
- Judge explains its reasoning clearly

**Issues:**
- [ ] judge/custom: custom rules in vairdict.yaml
- [ ] judge/analytics: historical verdict data
- [ ] judge/patterns: cross-task failure patterns
- [ ] judge/confidence: confidence scores per verdict
- [ ] judge/explainability: why did this score X?

---

## Milestone 12 — Learning Org Tier
> Cross-repo knowledge, precedence rules, and grade reinforcement.

Design reference: [LEARNING-ROADMAP.md](./LEARNING-ROADMAP.md) milestone 4.

**Definition of done:**
- Org repo (`<org>/.vairdict`) configurable and working
- `vairdict org init/enable/sync` CLI commands
- Precedence: repo-wins (default), org-wins for sticky tags (security, compliance)
- Repo-level overrides with rationale + auto-PR to org repo
- Grade reinforcement: asymptotic update on use, decay classes, protected tier
- Graceful degradation on org tier failures

**Issues:**
- [ ] learning/org-repo: org-tier storage + `vairdict org` CLI commands
- [ ] learning/precedence: repo-wins / org-wins / scope specificity rules
- [ ] learning/overrides: repo override entries with auto-PR to org
- [ ] learning/reinforcement: asymptotic grade updates + decay classes
- [ ] learning/degradation: four failure modes handled gracefully

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
- "Built with VAIrdict" public dashboard

**Issues:**
- [ ] platform/api: public REST API
- [ ] platform/linear: trigger from Linear issue
- [ ] platform/jira: trigger from Jira ticket
- [ ] platform/marketplace: community judge marketplace
- [ ] platform/badge: "Built with VAIrdict" public dashboard

---

## Milestone 15 — Early Users
> Validate with real teams outside your own repo.

**Definition of done:**
- brew tap live, `brew install vairdict` works
- vairdict.com landing page + email signup live
- 5 external repos running VAIrdict
- Structured feedback collected from each
- ProductHunt + Show HN launched

**Issues:**
- [ ] release/brew: brew tap + formula automation
- [ ] web: vairdict.com landing page + email signup
- [ ] feedback: outreach to 5 teams, collect structured feedback
- [ ] launch: Show HN + dev.to article
- [ ] launch: ProductHunt + "Built with VAIrdict" badge

---

## Milestone 16 — Slack App
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

## Milestone 17 — Team Features
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

## Milestone 18 — Coder Integration
> Isolated cloud environments per agent.

**Definition of done:**
- Coder selectable as environment in vairdict.yaml
- Each agent gets isolated cloud workspace per task
- Coder registry module published
- local | github-actions | coder all working

**Issues:**
- [ ] coder/env: Coder as environment option
- [ ] coder/workspace: isolated cloud workspace per agent
- [ ] coder/permissions: scoped permissions per workspace
- [ ] coder/registry: module published to Coder registry
- [ ] coder/docs: setup guide for Coder users

---

## Learning Extensions
> Post-v1 learning system enhancements. Prioritize based on real-world usage after M12.

Design reference: [LEARNING-ROADMAP.md](./LEARNING-ROADMAP.md) milestones 5–10.

All depend on M12 (Learning Org Tier). Independent of each other — order by demand.

- **MCP Server** (learning-5): Expose learnings to external agents via MCP protocol (`search_learnings`, `get_learning`, `propose_learning`).
- **Multi-Model Grading** (learning-6): Independent grading by a second model; disagreement flagging and resolution.
- **SPM Consumption Skill** (learning-7): Portable skill for non-VAIrdict agents to read learnings from `.vairdict/learnings/`.
- **Cross-Repo Pattern Detection** (learning-8): Auto-detect similar learnings across repos; propose org-tier promotion PRs.
- **Rich Resolvers** (learning-9): `resolves_when` support for `version` (package manifest pins) and `ticket` (GitHub/Linear/Jira issue closure).
- **Automatic Eviction** (learning-10): Maintenance PRs proposing removal of dormant, low-grade entries; `vairdict revive-learning` for undo.

---

## Web UI
> Visibility into what agents are doing without reading logs.

Not scheduled. Slot in once early users ask for it (likely between M11 and M12).

**Likely scope:**
- Kanban-style task board
- Judge scores + verdict details per phase per task
- Loop history + assumption log per task
- Escalation management from the UI
- vairdict.com/dashboard live

---

## Open-Issue Backlog
> Drained continuously between milestones — not its own milestone.

Open GitHub issues that don't belong to a specific milestone are picked up
opportunistically between milestone work. Bugs and small papercuts found
during dogfooding land here.

---

## Principles

**Judge everything** — every phase transition is gated by a judge, not just the final output.

**Language agnostic** — vairdict.yaml defines how to build/test/lint. VAIrdict never imports your code.

**Pluggable agents** — Claude Code is the default. Codex, Gemini, or any CLI agent can replace it.

**Dogfood first** — every VAIrdict feature is built using VAIrdict itself before being shipped.

**Learn from mistakes** — every rejection is a chance to capture a generalizable lesson. The learning system compounds over time.

**Skills over monolith** — judge behaviors are skills published to skillpkg. Anyone can contribute or override them.

**Human at the edges** — human provides intent at the start, reviews PR at the end. Everything in between is VAIrdict.
