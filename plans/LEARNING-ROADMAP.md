# Roadmap additions — learning system (milestone-learning-1 through 10)

The following milestone entries extend `ROADMAP.md` with the learning system work. Each milestone is independently shippable and independently useful. Design reference: [`docs/LEARNING-SYSTEM.md`](./LEARNING-SYSTEM.md).

Milestones 1–4 constitute the v1 learning system. Milestone 1 is split into two sub-phases (1a and 1b) because the original scope was too large for a single "prove it" milestone — 1a validates the data model manually before 1b adds automated write and retrieval. Milestones 5–10 build on that foundation and can be reprioritized based on real-world usage and feedback.

---

## milestone-learning-1a — Schema, storage, and manual entry

**Goal:** Validate the data model and file format with manual entries before adding any automation.

**Scope:**
- Full content-entry schema defined and locked in (`id` with 8-hex-char hash, `content`, `rationale`, `type`, `tags.topic`, `tags.scope`, `decay_class`, `override_policy`, `resolves_when`, `overrides`, `contradicts`, `supersedes`, `triggers_on`, `applies_to`, `created_at`, `updated_at`, `source_pr`, `original_source`).
- Stats record schema defined (`grade`, `retrieval_count`, `successful_uses`, `unsuccessful_uses`, `last_retrieved_at`, `stats_updated_at`).
- **Multi-entry content file format** per design doc §5.2: each entry begins with `<!-- LEARNING -->` marker followed by YAML frontmatter and markdown body. Parser and writer both implement this format from day one.
- Content/stats split implemented: content in markdown with YAML frontmatter, stats in JSON files under `stats/` subdirectory.
- Machine-tier storage at `~/.vairdict/learnings/` (gitignored, personal). No bot commits at this tier — just local file writes.
- Config lives under `learnings:` key in existing `vairdict.yaml` (project) and `~/.vairdict/config.yaml` (machine-tier). No separate config files introduced.
- CLI commands for manual learning management:
  - `vairdict learning add` (interactive draft, opens editor)
  - `vairdict learning list [--topic <topic>] [--scope <scope>]`
  - `vairdict learning show <id>`
  - `vairdict learning remove <id>`
  - `vairdict learning edit <id>` (opens editor for in-place refinement)
  - `vairdict learning set-grade <id> <grade>` (machine tier only; human override)
- `resolves_when` with `file-exists` and `file-absent` kinds supported; `version` and `ticket` reserved but warn-and-skip.
- `manifest.json` generation on any write; `index.json` derivation on first read with invalidation triggers (manifest SHA change, mtime check, explicit reindex, session start).

**Out of scope:** Judge integration, automated write path, retrieval, three-role consumption, repo tier, PR flow.

**Exit criteria:** Maintainer has manually created 10+ learnings via CLI across 3+ topics and 2+ scopes, with at least one topic file containing multiple entries (validates the multi-entry format end-to-end). Schema feels right for the diversity of learnings produced. Content/stats split has been exercised (grade updates via `vairdict learning set-grade` don't pollute content files). File format and parser are committed and won't change going forward.

**Depends on:** existing VAIrdict infrastructure. **Verify before starting:** `config.LoadConfigWithOverlay` must handle nested struct merging correctly for the new `learnings:` section (so a CI overlay setting `learnings.stats.commit_strategy: branch` doesn't wipe out `learnings.retrieval.token_budget`). If the existing merge is shallow, fix it as the first step of this milestone.

---

## milestone-learning-1b — Automated write path and retrieval (single-role)

**Goal:** Close the loop — Judge produces learnings automatically, subsequent loops retrieve them.

**Scope:**
- Extension to Judge output: `Gap` gains an optional `Learnable *LearnableSignal` field (`Pattern`, `Generalization`, `Scope`, `Confidence`) per design doc §6.
- Automated write path (plan/do/judge on learnings themselves):
  - Trigger detection: **converged outer-loop rejections** (rejected then passed) and **escalated outer-loop rejections** (exhausted retries). Escalation is explicitly in scope because it is the highest-signal trigger; the learning sub-phase runs before `runOrchestration` returns on escalation per design doc §6.
  - Inner rejections, planning iteration, and human `add-learning` are deferred to milestone-learning-2 (they depend on PR flow and user-interaction surfaces that don't exist yet).
  - Planner analyzes failure delta + LearnableSignal, drafts candidate.
  - Coder writes to the machine-tier content file using the multi-entry format per design doc §5.2 (`<!-- LEARNING -->` marker).
  - Judge (fresh context invocation) grades the draft, assigns initial grade (0.3–0.8), writes to machine-tier stats file.
- Retrieval for **Planner only** (single-role consumption in 1b; three-role is milestone-learning-2):
  - Task context computation from issue + repo state.
  - Adaptive K retrieval bounded by token budget and relevance floor.
  - Relevance scoring per design doc §7 (Jaccard on topics, binary-with-partial on scope with sticky-bypass clause, 60/40 weighting).
  - Content + stats join at retrieval time.
- **Batch-and-flush stats infrastructure:** stats updates are queued during a loop (`queue_stats_update(id, deltas)`) and flushed on loop completion as a single atomic operation. In milestone-learning-1b the flush is a local file write (no bot, no commits), but the queuing and single-flush semantics must be in place because milestone-learning-2 and later milestones build on them. This is the first implementation of the pattern; get the interface right.
- Static grade reinforcement in machine tier: `successful_uses` / `unsuccessful_uses` counters bump, but grade decay and asymptotic cross-loop reinforcement are deferred to milestone-learning-4.

**Out of scope:** repo tier, PR commands, monorepo, three-role consumption, grade reinforcement with asymptotic updates, decay, bot commits, concurrent-loop handling.

**Exit criteria:** Maintainer has used VAIrdict on 3–5 real tasks. Judge has emitted at least 2 `LearnableSignal`s that produced learning drafts. Planner has retrieved learnings in a subsequent loop and the retrieved content has visibly influenced the plan. Schema is stable — no field changes during this milestone. Relevance scoring produces reasonable rankings (manual spot-check).

**Depends on:** milestone-learning-1a. Requires Judge output extension (listed in scope above as a prerequisite within this milestone).

---

## milestone-learning-2 — Repo tier with PR flow and reviewer commands

**Goal:** Shift from personal tool to team tool. Learnings become team artifacts reviewed in PRs.

**Scope:**
- Repo-tier storage at `.vairdict/learnings/` (content files git-tracked).
- Repo-tier stats at `.vairdict/learnings/stats/` with `vairdict-bot` commit strategy (direct to default branch or dedicated `vairdict-stats` branch per config).
- Single-file flat layout for single-service repos.
- `manifest.json` per tier, committed; `index.json` derived, not committed.
- Learnings land in the same PR as the code change that produced them (content only; stats are bot commits).
- PR description auto-lists added learnings with IDs.
- `@vairdict` PR comment commands:
  - `add-learning [body]`
  - `drop-learning <id>`
  - `drop-all-learnings`
  - `revise-learning <id>: <content>`
  - `set-grade <id> <grade> [reason: <rationale>]` (human grade override)
- `vairdict promote-learning <id> [--scope <scope>]` CLI command for machine → repo promotion (opens a PR adding the machine-tier learning to the current repo).
- Judge evaluates its own learnings in a fresh context window.
- Trigger expansion from milestone-learning-1b: adds inner-loop rejections (user rejected plan), planning iteration (≥2 revisions before execution converged), and human `add-learning`. Converged outer-loop rejection and escalation triggers are already shipped in 1b and continue working.
- All three roles (Planner, Coder, Judge) consume learnings with role-specific framings and type weights.
- Token budget accounting: per-role fixed budgets (default 3000 tokens); design acknowledges this as a first-pass implementation with cross-role caching as a follow-up optimization.
- Error handling for bot commit permission issues (clear error message, pointer to config options per design doc §11).

**Out of scope:** monorepo service dirs, org tier, asymptotic grade reinforcement, decay, automatic eviction.

**Exit criteria:** Learnings flow through PRs with real reviewers. Content PRs are clean (no stats noise). Stats files update correctly via bot commits without polluting PR review. All five `@vairdict` commands work. All three loop roles consume learnings. `vairdict promote-learning` successfully moves a machine-tier learning to repo tier via PR. At least one external contributor has reviewed or authored a learning via the flow.

**Decisions needed at implementation:** see design doc §12.

**Implementation sequencing:** this milestone is large. Recommended internal order to keep each landable increment coherent, even if the milestone only "ships" when all four are complete:

1. **Repo tier + content files + manifest regeneration policy.** Get the file-based storage and merge-conflict story working with no behavior change yet.
2. **Stats infrastructure with bot commits.** Implement retry-on-conflict, permission failure handling, and the branch-vs-direct strategy config. Separate from PR flow because stats mechanics are orthogonal to user-facing PR commands.
3. **Three-role consumption with token budgets.** Extend retrieval from Planner-only (milestone-learning-1b) to all three roles. This is mostly prompt and orchestration work on top of existing retrieval.
4. **PR commands, `promote-learning`, and human-facing workflow.** The surface reviewers interact with. Build last so the underlying write/read paths are proven before exposing commands that invoke them.

Landing these in order means each sub-phase is dogfoodable and the surface area for regressions stays bounded.

**Depends on:** milestone-learning-1b.

---

## milestone-learning-3 — Monorepo support and scope-based layout

**Goal:** Handle multi-service monorepos without cross-service noise in retrieval or review.

**Scope:**
- `repo_type: monorepo | single-service` config in `vairdict.yaml`.
- `services:` config listing service names and paths.
- Monorepo file layout: `_shared/`, `_repo/`, and per-service directories under `.vairdict/learnings/` (all coexisting).
- File location derived from `tags.scope` (rule in design doc §5).
- Retrieval respects scope: loads service dir + `_shared/` + `_repo/`, not other services' dirs.
- CODEOWNERS integration documented (service teams own their learning dirs).
- Index building scope-aware.
- Lenient handling of unknown services (put in `_shared/`, warn).

**Out of scope:** org tier, reinforcement, decay, file splitting within a service dir.

**Exit criteria:** VAIrdict runs cleanly in a monorepo with 3+ services. A multi-scope learning lives correctly in `_shared/`. Retrieval for a task in service A does not surface service B's entries. CODEOWNERS routes learning reviews to the right teams.

**Depends on:** milestone-learning-2.

---

## milestone-learning-4 — Org tier, precedence, reinforcement, and grade lifecycle

**Goal:** The learning system "compounds" — cross-repo knowledge, repo/org precedence, and grade-on-use reinforcement turn it from storage into something that improves over time.

**Scope:**
- Support for a separate org-level repo (default: `<org-name>/.vairdict`, custom name allowed) configured via `org_repo:` in `vairdict.yaml`.
- `vairdict org init <org-name>` CLI command to create the org repo with standard layout. Default name is `<org-name>/.vairdict` (following the `.github` meta-repo convention); `--name <custom>` flag overrides for hosts that don't support leading-dot names or for teams that prefer a different convention.
- `vairdict org enable <org-name>` CLI command to connect an existing org repo.
- `vairdict org sync` CLI command to process pending org PRs when org becomes available.
- Four failure-mode handling: missing, no-write-permission, unreachable, override-without-org.
- Read-only degradation when write permission is missing.
- Pending org PR persistence in `.vairdict/pending-org-prs/`.
- Dual SHA checking via per-tier `manifest.json` comparison.
- Precedence rules:
  - Repo-wins as default for non-sticky tags.
  - Org-wins for sticky tags (configurable, defaults include `security`, `compliance`, `license`).
  - More-specific scope wins within a tier.
- Explicit repo-level override entries (`overrides: [<org-id>]`) with required rationale.
- Auto-PR against the org repo when a repo-level override is created.
- Contradiction detection at retrieval time with logging.
- Grade reinforcement on use:
  - Asymptotic update on successful use: `grade += 0.05 * (1 - grade)`.
  - Asymptotic decrement on ignored/contradicted use.
  - Judge explicit citation check at PR completion.
- Decay classes (`none`, `slow`, `normal`, `fast`) applied opportunistically.
- Protected tier with floor grade.
- Principle: core VAIrdict never blocks on org tier failures.

**Out of scope:** MCP server, multi-model grading, promotion tooling, automatic eviction.

**Exit criteria:** `vairdict org init` creates a working org repo. An org repo is configured and in use on at least one project. All four failure modes have been tested (missing repo, read-only permissions, unreachable, pending PR). Repo overrides of org sticky-tag entries work with explicit rationale. Auto-PR from override creation lands in the org repo with expected metadata. Grades visibly move over the course of real usage. At least one security sticky-tag scenario has been exercised end-to-end.

**Decisions needed at implementation:** see design doc §12.

**Depends on:** milestone-learning-3.

---

## milestone-learning-5 — MCP server for external-agent consumption

**Goal:** Make VAIrdict's learnings consumable by external agents (Claude Code, Cursor, Copilot, CI) via a standard protocol.

**Scope:**
- MCP server exposing three tools:
  - `search_learnings(tags, role?, token_budget?)` — returns ranked learnings matching the query.
  - `get_learning(id)` — returns a specific learning's full content.
  - `propose_learning(content, context)` — write-back tool; proposals go through the same Judge grading as internal learnings before landing.
- Authentication model: GitHub App token or PAT, scoped to the repos VAIrdict is installed on.
- Reads from the same file layout as the core system — no separate store.
- Search applies the same ranking and filtering as internal retrieval (§7 of design doc).
- Integration documented for Claude Code, Cursor, and Copilot.
- No write access for propose_learning beyond the same PR flow — external agents can suggest, but landing still requires Judge and human review.

**Out of scope:** authentication UI, multi-tenant hosting, external-agent-initiated bulk ingestion.

**Exit criteria:** A developer using Claude Code on a repo with VAIrdict configured can call `search_learnings` and see relevant entries. A proposed learning from an external agent lands via a PR with Judge grading applied. Documentation includes working examples for at least two external agents.

**Depends on:** milestone-learning-4.

---

## milestone-learning-6 — Multi-model grading for `learning_judge`

**Goal:** Enable independent grading of learnings on a different model from the code Judge, improving grade quality through model diversity.

**Scope:**
- `learning_judge` role in `vairdict.yaml` becomes separately configurable (model, provider).
- Dual-grading execution path:
  - Code Judge grades the learning as primary grader.
  - `learning_judge` (if configured on a different model) grades independently.
  - Both grades recorded on the entry.
- Resolution strategy for grader disagreement:
  - If grades agree within threshold delta (e.g., 0.15): use average.
  - If grades disagree beyond threshold: flag for human review and store both grades separately; learning proceeds with minimum grade as effective grade.
- Grade display in PR descriptions shows both graders when they differ significantly.
- Metrics on grader disagreement rate to help tune the resolution threshold.

**Out of scope:** running N > 2 graders simultaneously; grader-weighted voting.

**Exit criteria:** An org can configure Claude-opus as code Judge and a different provider (e.g., GPT-5, Gemini, etc.) as `learning_judge`. Grade disagreements surface in PR descriptions. Flagged-disagreement entries are reviewed by humans and the resolution is reflected in the final stored grade.

**Decisions needed at implementation:** confirm the disagreement threshold and exact resolution strategy based on early data.

**Depends on:** milestone-learning-4 (for the grade lifecycle to exist) and milestone-learning-5 (not strictly, but parallel infrastructure).

---

## milestone-learning-7 — Consumption skill for external agents (via SPM)

**Goal:** Let developers using non-VAIrdict coding agents (Claude Code, Cursor, Copilot, Windsurf) benefit from a VAIrdict repo's accumulated learnings without running a VAIrdict loop themselves.

**Scope:**
- A portable consumption skill packaged via SPM (e.g., `@vairdict/consume-learnings`).
- Skill reads `.vairdict/learnings/` at session start (or on-demand trigger).
- Surfaces relevant entries filtered by current task context (files being edited, branch name, issue context).
- Works across any agent that speaks skills (Claude Code, Cursor, etc.).
- Can optionally connect to the MCP server (milestone-learning-5) for full retrieval, falling back to file-based reads.
- Documentation for install and configure in each supported agent.

**Out of scope:** write-back via the skill (external agents still propose via the MCP server, not via the skill itself).

**Exit criteria:** The skill is published on SPM. A developer using Claude Code on a VAIrdict-enabled repo sees relevant learnings surfaced automatically. The same skill works in Cursor with equivalent behavior.

**Depends on:** milestone-learning-3 (for the file layout to be stable) and milestone-learning-5 (optional enhancement via MCP).

---

## milestone-learning-8 — Automated cross-repo pattern detection and org promotion

**Goal:** Automatically detect when the same learning is emerging independently across multiple repos in an org, and propose promotion to org tier.

**Scope:**
- Cross-repo observation: a mechanism for VAIrdict to scan multiple repos in an org that share an `org_repo` configuration.
- Similarity detection: identify when N (configurable, default 3) repo-tier learnings have substantially overlapping content, tags, and rationale.
- Auto-PR proposal against the org repo:
  - Title: `Consider promoting pattern: <short description>`
  - Body: lists the N source learnings across repos, highlights common elements, proposes a consolidated org-tier entry.
  - Labels: `vairdict`, `learning-review`, `promotion-proposed`.
- CODEOWNERS review process: merge promotes (and optionally cleans up source repos), close rejects, request-changes iterates.
- Cleanup: if the org promotion lands, VAIrdict optionally opens follow-up PRs in source repos to remove the now-redundant repo-level learnings.

**Out of scope:** automatic acceptance of promotions without human review; cross-org promotion.

**Exit criteria:** A scenario where 3 repos in an org have independently created a similar learning triggers an auto-promotion PR within a reasonable detection window. The promotion flow has been exercised end-to-end with human review.

**Depends on:** milestone-learning-4. Benefits significantly from milestone-learning-5 (MCP server as the cross-repo observation backbone).

---

## milestone-learning-9 — Richer `resolves_when` (`version` and `ticket`)

**Goal:** Let learnings declare dependency on external conditions — version pins, ticket resolution — so they can auto-resolve when those conditions change.

**Scope:**
- `version` resolver:
  - Parser for language-specific manifests: package.json, go.mod, Cargo.toml, pyproject.toml.
  - Expression language: `"lodash>=5.0.0"`, `"go>=1.22"`, etc.
  - Check runs on manifest changes in the repo.
- `ticket` resolver:
  - Connectors for GitHub Issues (first), Linear (second), Jira (third).
  - Auth configuration in `vairdict.yaml` or per-user env.
  - Check runs on a schedule (e.g., daily) or on-demand.
- Resolution triggers:
  - When a `resolves_when` condition is satisfied, VAIrdict opens a cleanup PR removing the learning.
  - PR includes context about why the condition is considered resolved.
- Backward-compatible with existing entries using `file-exists` / `file-absent`.

**Out of scope:** custom resolver plugins; real-time webhook-driven resolution.

**Exit criteria:** A learning with `resolves_when: {kind: version, ref: "lodash>=5.0.0"}` auto-resolves when the repo's lodash dependency reaches v5. A learning with `resolves_when: {kind: ticket, ref: "gh:acme/app#1234"}` auto-resolves when the issue closes. Both flows have been tested end-to-end.

**Decisions needed at implementation:** language and ticket-system ordering based on actual user demand; expression syntax for version constraints.

**Depends on:** milestone-learning-4.

---

## milestone-learning-10 — Automatic eviction with maintenance PRs

**Goal:** Prevent unbounded growth of `.vairdict/learnings/` by auto-proposing removal of dormant, low-graded entries.

**Scope:**
- Eviction policy:
  - Entry is eligible for eviction if: grade below threshold AND not protected AND not retrieved in N days (configurable).
  - Security/invariant entries never auto-evict regardless of grade.
- Maintenance PR flow:
  - VAIrdict opens a consolidated eviction PR (weekly or on-demand) listing all eligible entries.
  - PR body summarizes why each entry is proposed for removal.
  - Humans review, can keep individual entries via `@vairdict keep-learning <id>` comments.
  - Merging removes the entries from storage.
- Audit trail: evicted entries retained in git history (no data loss, just removed from active search).
- Undo: `vairdict revive-learning <id>` can restore an entry from git history if it was removed prematurely.

**Out of scope:** automatic deletion without PR review; eviction from git history.

**Exit criteria:** A repo with 100+ learnings, some dormant for months, receives a maintenance PR proposing evictions. Humans review, selectively preserve some entries, merge the rest. Revive flow has been tested.

**Depends on:** milestone-learning-4.

---

## Sequencing notes

- Milestones 1a → 1b → 2 → 3 → 4 have strict dependencies; do not reorder. The 1a/1b split exists because milestone-learning-1a (manual entry + schema validation) must succeed before milestone-learning-1b (automated write path) locks the data model in.
- Milestones 5, 6, 7, 8, 9, 10 depend on milestone-learning-4 being complete but are otherwise independent; prioritize based on real-world pain points.
- milestone-learning-7 benefits from milestone-learning-5 but can ship without it (fallback to file-based reads).
- milestone-learning-8 benefits significantly from milestone-learning-5; consider sequencing 5 before 8.
- Dogfooding between milestones is a feature, not a gap — each milestone's exit criteria include real usage, which informs the next milestone's scope.
- Schema changes are cheapest in milestone-learning-1a (local files, manual entry, single user) and most expensive in milestone-learning-4 (multi-tier with manifests, stats files, bot commits). Get the data model right early. Milestone-learning-1a exists specifically to force schema validation before automation locks it in.
- Milestones 5–10 are not strictly ordered after milestone-learning-4; the ordering reflects expected value but should be adjusted based on actual demand.
