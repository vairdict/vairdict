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
- Codex CLI usable as a completer **and** coder backend
- Gemini CLI usable as a completer **and** coder backend
- Backend selectable per phase in vairdict.yaml
- Judge model swappable independently of completer model
- Auto-resolver picks the best available backend for both completer
  and coder roles, like the existing claude resolver

**Issues:**
- [ ] agents/codex: Codex CLI completer
- [ ] agents/gemini: Gemini CLI completer
- [ ] agents/codex: Codex CLI coder (file-editing driver)
- [ ] agents/gemini: Gemini CLI coder (file-editing driver)
- [ ] config: per-phase backend selection in vairdict.yaml
- [ ] judge/pluggable: swap judge model in vairdict.yaml
- [ ] resolver: extend auto backend resolver to all agents (completer + coder)
- [ ] test/m6: cross-backend integration + e2e tests (gated)
- [ ] docs: agent backend selection guide

---

## AI-First Block (Milestones 7–13)
> Polarity flip: VAIrdict shifts from reactive PR judge to proactive
> issue-aware contributor. Entry point moves upstream from
> `pull_request.opened` to `issues.opened`. After Milestone 13,
> VAIrdict has a full v1 user-facing product.

Design reference: [PLAN-AI-FIRST.md](./PLAN-AI-FIRST.md).

These seven milestones implement the GitHub App surface end to end.
Milestone 14 onward resumes the internal-quality work (skillpkg,
learning system, judge intelligence) that the polarity flip pushed
back.

The AIF block ships in the order **aif-1 → aif-2 → aif-3 → aif-5 →
aif-4 → aif-6 → aif-7** per [PLAN-AI-FIRST.md §10](./PLAN-AI-FIRST.md):
cost ceilings (Milestone 10 = aif-5) ship before control commands
(Milestone 11 = aif-4) because caps are the primary safety net for
an autonomous pipeline.

**Learning compatibility.** AIF is designed so the learning system
(Milestones 15–19, originally Milestones 8–12) can plug in
*additively* without re-architecture. Two integration points are
reserved during AIF implementation: (a) state branch layout leaves
room for `learnings/` alongside `events/` and `state/`; (b) Planner
/ Coder / Judge prompt construction includes a "preface" slot where
retrieval results can land at Milestone 15 and beyond. Deferring
learning behind AIF is intentional — AIF is the *delivery
mechanism*; without it learning has no surface.

### Runtime decision

The GitHub App webhook server lives in this Go module at
`cmd/vairdict-server/`, sharing `internal/` packages directly with
the existing `cmd/vairdict/` CLI. Same `make build` / `make test`,
same release pipeline (GoReleaser extended to build a second
binary). Deploy target deferred to Milestone 7 implementation time
— Fly.io machines, Cloud Run, or self-hosted all work.

Why Go-only and not Cloudflare Workers (the other candidate listed
in [PLAN-AI-FIRST.md §7](./PLAN-AI-FIRST.md)): Workers' CPU-time
limits (~30s per request) make Coder/Judge inner loops infeasible
inside the Worker. Going Worker means *two* runtimes (Worker for
fast webhook ack + a separate long-running runtime for inner-loop
work) and a serialization contract between them. Go-only keeps it
one runtime, one language, zero contract — direct package imports
from `internal/`.

---

## Milestone 7 — AIF-1: Issue triage + context-aware Judge (the wedge)
> Polarity-flip wedge. Make Judge context-aware, add `issues.opened`
> as a new entry point with the duplicate-detection gate only.

Design reference: [PLAN-AI-FIRST.md milestone-aif-1](./PLAN-AI-FIRST.md).

**Definition of done:**
- Webhook handler at `cmd/vairdict-server/` receives `issues.opened`
  and verifies signatures
- Triager runs `is_duplicate` gate against open issues + recent merged PRs
- Triage status comment posted within 30 seconds of issue open
- Outer Judge gains a context-pass: surfaces top-3 related items inline in the verdict
- Embeddings cache on state branch, computed per main commit (cold-start: inline compute)
- Gate framework module (composable, used by Triager and Judge)
- Status comment module (one-source-of-parsing pattern, supports `triage` kind)

**Out of scope:** Planner trigger, Coder execution, fix flow, all other gates.

**Issues:**
- [ ] aif/server: cmd/vairdict-server/ webhook handler + signature verification
- [ ] aif/triager: Triager role + `is_duplicate` gate
- [ ] aif/embeddings: embeddings cache on state branch (per main commit)
- [ ] aif/state-comment: status comment module (one-source-of-parsing)
- [ ] aif/gates: gate framework module (composable, per-stage)
- [ ] aif/judge-context: outer Judge context-pass with related-items surface

---

## Milestone 8 — AIF-2: Plan→Code→PR pipeline with approval gates
> Issue triage extends to full plan→code→PR autonomy with explicit
> human gates (`@vairdict plan` and `@vairdict go`).

Design reference: [PLAN-AI-FIRST.md milestone-aif-2](./PLAN-AI-FIRST.md).

**Definition of done:**
- `@vairdict plan` command on issues with write-access authorization
- Planner produces structured plan with size, affected_files, trade-offs,
  acceptance criteria, `created_against_sha`
- `plan_is_fresh` gate (intersect main drift with `affected_files`)
- `@vairdict go` required to proceed to coding
- `vairdict/issue-N` branch created on `go`
- Coder ↔ Judge inner loop on the branch, up to 5 iterations (3 in first-time mode)
- Cancellation context plumbed through all roles from day one
- Inner-loop gates: `build_passes`, `within_plan_scope`
- `plan-needs-revision` exit, capped at 2 revisions per issue
- PR opens against main on convergence, body = plan + drift notes
- Outer Judge runs on PR with context pass from Milestone 7

**Out of scope:** fix flow, auto-merge, sub-issue splitting, cost ceilings.

**Issues:**
- [ ] aif/cmd-plan: `@vairdict plan` on issues + auth check
- [ ] aif/planner: structured plan output schema + status-comment kind=plan
- [ ] aif/gate-fresh: `plan_is_fresh` gate (sha drift × affected_files)
- [ ] aif/cmd-go: `@vairdict go` + branch creation
- [ ] aif/inner-loop: Coder ↔ Judge loop with cancellation context
- [ ] aif/gate-build: `build_passes` gate
- [ ] aif/gate-scope: `within_plan_scope` gate
- [ ] aif/plan-revision: `plan-needs-revision` exit + 2-revision cap
- [ ] aif/pr-open: PR creation with plan body + drift notes

---

## Milestone 9 — AIF-3: Fix loop on PR review comments
> `@vairdict fix` works on review comments (VAIrdict's PRs and
> human-authored PRs), with conflict detection and parallel execution.

Design reference: [PLAN-AI-FIRST.md milestone-aif-3](./PLAN-AI-FIRST.md).

**Definition of done:**
- `@vairdict fix` command on `pull_request_review_comment.created` replies
- `no_conflicts_with_other_fixes` gate (file + line-range overlap + semantic check)
- `fix_queue` status comment kind with reaction-state on the trigger comment
- Fix-size classifier (trivial vs. substantive); `--thorough` / `--quick` overrides
- Parallel worktrees for non-conflicting fixes (cap 2-3 concurrent)
- Sequential merge-back via rebase
- Idempotency: re-fix on already-handled comment replies with commit link
- Fork PR fallback deferred to milestone-aif-fork

**Out of scope:** fix-batch optimization, `@vairdict review` on demand.

**Issues:**
- [ ] aif/cmd-fix: `@vairdict fix` on review comment replies
- [ ] aif/gate-fix-conflict: `no_conflicts_with_other_fixes` gate
- [ ] aif/fix-queue: `fix_queue` status comment + reaction-state
- [ ] aif/fix-classifier: trivial vs. substantive classifier + flag overrides
- [ ] aif/parallel-worktrees: non-conflicting fixes in parallel
- [ ] aif/merge-back: sequential rebase merge-back

---

## Milestone 10 — AIF-5: Cost ceilings and failure templates
> Production-grade cost discipline and trustworthy failure UX.
> Shipped before AIF-4 (control commands) — caps are the primary
> safety net, not pause/stop.

Design reference: [PLAN-AI-FIRST.md milestone-aif-5](./PLAN-AI-FIRST.md).

**Definition of done:**
- Per-run hard caps enforced check-after-call (S/M/L defaults configurable)
- Per-repo monthly cap (opt-in), soft warn at 80%, hard stop at 100%
- LLM and compute cost tracked + reported separately
- Failure-mode template module (shared format, parameterization, dedup logic)
- Failure templates shipped: `iterations_exhausted`, `gate_blocked_<name>`,
  `plan_needs_revision`, `fix_conflict`, `cost_cap_run`, `cost_cap_monthly`,
  `plan_stale`, `api_unavailable`
- Failure-mode dedup: same `failure_kind` on same context within 10 min
  updates existing comment instead of posting new
- API/billing failures use `api_unavailable` template, no durable mute

**Out of scope:** dynamic cap adjustment, per-user caps, org-aggregate cap (deferred to Milestone 13).

**Issues:**
- [ ] aif/cap-per-run: per-run cost cap, check-after-call enforcement
- [ ] aif/cap-monthly: per-repo monthly cap, soft + hard thresholds
- [ ] aif/cost-tracking: LLM vs compute split, telemetry plumbing
- [ ] aif/failure-templates: shared module + 8 templates listed above
- [ ] aif/failure-dedup: 10-min same-kind dedup logic

---

## Milestone 11 — AIF-4: Pause / Stop / Help / Status / Usage
> Control surface and discoverability commands.

Design reference: [PLAN-AI-FIRST.md milestone-aif-4](./PLAN-AI-FIRST.md).

**Definition of done:**
- `@vairdict pause` / `@vairdict resume` per-PR (durable mute, abort in-flight)
- `@vairdict stop` one-shot abort with stashed work recoverable
- `@vairdict help [verb]` always-on, dynamic content
- `@vairdict status` always-on, current-state reporting
- `@vairdict usage` repo-scoped, by-phase + by-outcome breakdown
- Unknown command handling: ❓ + threaded reply
- Help works during pause; authorization gates respected regardless of pause

**Out of scope:** repo-wide freeze (milestone-aif-freeze), `@vairdict review` on demand.

**Issues:**
- [ ] aif/cmd-pause: pause/resume per-PR with durable mute state
- [ ] aif/cmd-stop: one-shot abort with stash recovery
- [ ] aif/cmd-help: dynamic help, always-on, pause-aware
- [ ] aif/cmd-status: dynamic status reporting
- [ ] aif/cmd-usage: per-repo cost breakdown by phase + outcome
- [ ] aif/cmd-unknown: ❓ + threaded reply for unknown commands

---

## Milestone 12 — AIF-6: Onboarding and welcome flow
> First-impression UX and first-time-mode safety net.

Design reference: [PLAN-AI-FIRST.md milestone-aif-6](./PLAN-AI-FIRST.md).

**Definition of done:**
- `installation.created` webhook handler
- Welcome issue: 3-line summary + default `vairdict.yaml` stub +
  four key commands + invitation to try
- Embeddings bootstrap (background) on install
- First-time mode: plan banner, halved iteration cap (5→3), auto-merge disabled
  for first 3 VAIrdict PRs (counted via GitHub API, not telemetry)
- Welcome issue is user-owned; not edited after graduation
- Org-tier discovery: check `<org>/.vairdict`, reference in welcome
- `installation.deleted` handler: stop in-flight runs, leave history untouched

**Out of scope:** repo scanning to propose `sensitive_paths`, suggested first
issue, org-level summary repo, journal artifact.

**Issues:**
- [ ] aif/install-handler: `installation.created` webhook
- [ ] aif/welcome-issue: welcome issue template + yaml stub generation
- [ ] aif/embeddings-bootstrap: background pre-compute on install
- [ ] aif/first-time-mode: banner + halved cap + auto-merge disable for first 3
- [ ] aif/install-cleanup: `installation.deleted` graceful shutdown

---

## Milestone 13 — AIF-7: Telemetry, rollback signal, soft branch ownership
> Observability foundation and the missing safety primitives.

Design reference: [PLAN-AI-FIRST.md milestone-aif-7](./PLAN-AI-FIRST.md).

**Definition of done:**
- Single events module with pluggable sink interface (state branch + org-mirror sinks day one)
- Per-run event files at `events/<YYYY-MM>/<run_id>.json` (no write contention)
- Org-mirror sink writes to `<org>/.vairdict/events/<repo>/...` when org-tier exists
- Cross-run org-config cache (5-min TTL keyed on `<org>/.vairdict@<sha>`)
- Embeddings cache retention: keep last 10 main commits' embeddings
- `@vairdict org usage` command with org-aggregated cost breakdown
- Aggregate org-level cost cap enforcement
- `human_action_after` watcher: dual-mode (webhook + daily scheduled job)
- Rollback detection: revert commits within 7 days mark run `reverted`
- Webhook delivery idempotency via `state/deliveries/<delivery-id>` markers
- GitHub API rate-limit handling: caching, ETags, automatic backoff
- Soft branch ownership: pre-push HEAD check, abort + stash on conflict
- `human_pushed_during_run` and `human_takeover` failure templates

**Out of scope:** trust ladder enforcement (milestone-aif-trust), org-wide pause.

**Issues:**
- [ ] aif/events-module: pluggable sink interface + per-run JSON files
- [ ] aif/org-mirror: org-tier event mirroring + 5-min config cache
- [ ] aif/org-usage: `@vairdict org usage` command + auth model
- [ ] aif/cap-org: aggregate org-level monthly cap enforcement
- [ ] aif/rollback-watcher: webhook + daily job for `human_action_after`
- [ ] aif/delivery-idempotency: webhook delivery ID markers + daily prune
- [ ] aif/rate-limits: GH API caching, ETags, backoff
- [ ] aif/soft-ownership: pre-push HEAD check + stash + named-author message
- [ ] aif/branch-templates: `human_pushed_during_run` / `human_takeover` templates

---

### AI-First future milestones (deferred, named)

These are scoped in [PLAN-AI-FIRST.md §9](./PLAN-AI-FIRST.md) and
pulled in opportunistically once Milestone 13 primitives exist. Not
numbered in the global roadmap until trigger conditions hit.

- **milestone-aif-trust** — trust ladder enforcement (rolling
  merge/revert ratio drives autonomy levels). Depends on
  Milestone 13 telemetry. Pull in when first external installs land.
- **milestone-aif-perf** — latency optimization (persistent warm
  workspace). Trigger: observed fix latency exceeds threshold.
- **milestone-aif-freeze** — `@vairdict freeze` repo-wide pause for
  release windows. Depends on Milestone 11 + Milestone 13.
- **milestone-aif-review** — `@vairdict review` on-demand passive
  Judge pass. Depends on Milestone 11.
- **milestone-aif-fork** — fork PR fallback via suggested-changes
  blocks. Depends on Milestone 9.
- **milestone-aif-multitenant** — per-tenant telemetry isolation.
  Trigger: first non-operator install.
- **milestone-aif-operator-audit** — operator-side cross-install
  audit + observability. Depends on Milestone 13 + multitenant.
  Trigger: first external install.
- **milestone-aif-journal** — long-term journal artifact.
- **milestone-aif-dashboard** — web UI for gate config + telemetry
  browsing. Triggered by usage scale.
- **milestone-aif-scout** — proactive opportunity detection.
  Explicitly deferred indefinitely.
- **milestone-aif-docs** — holistic documentation pass after v1
  surface stabilizes (post-Milestone 13).

---

## Milestone 14 — skillpkg Deep Integration
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

## Milestone 15 — Learning Foundation
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

## Milestone 16 — Learning Repo Tier
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

## Milestone 17 — Learning Monorepo Support
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

## Milestone 18 — Judge Intelligence
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

## Milestone 19 — Learning Org Tier
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

## Milestone 20 — Monetization
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

## Milestone 21 — Platform
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

## Milestone 22 — Early Users
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

## Milestone 23 — Slack App
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

## Milestone 24 — Team Features
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

## Milestone 25 — Coder Integration
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
> Post-v1 learning system enhancements. Prioritize based on real-world usage after Milestone 19.

Design reference: [LEARNING-ROADMAP.md](./LEARNING-ROADMAP.md) milestones 5–10.

All depend on Milestone 19 (Learning Org Tier). Independent of each other — order by demand.

- **MCP Server** (learning-5): Expose learnings to external agents via MCP protocol (`search_learnings`, `get_learning`, `propose_learning`).
- **Multi-Model Grading** (learning-6): Independent grading by a second model; disagreement flagging and resolution.
- **SPM Consumption Skill** (learning-7): Portable skill for non-VAIrdict agents to read learnings from `.vairdict/learnings/`.
- **Cross-Repo Pattern Detection** (learning-8): Auto-detect similar learnings across repos; propose org-tier promotion PRs.
- **Rich Resolvers** (learning-9): `resolves_when` support for `version` (package manifest pins) and `ticket` (GitHub/Linear/Jira issue closure).
- **Automatic Eviction** (learning-10): Maintenance PRs proposing removal of dormant, low-grade entries; `vairdict revive-learning` for undo.

---

## Web UI
> Visibility into what agents are doing without reading logs.

Not scheduled. Slot in once early users ask for it (likely between Milestone 18 and Milestone 19).

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
