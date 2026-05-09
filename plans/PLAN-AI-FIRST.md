# PLAN: AI-First VAIrdict

> **Status:** Design complete, implementation not started
> **Scope:** Shifts VAIrdict from reactive PR judge to proactive issue-aware contributor
> **Audience:** VAIrdict contributors (currently @almog27), and Claude operating on this repo
> **Related docs:** `ROADMAP.md` (global), `AGENTS.md` (role definitions), `PROGRESS.md` (status tracking)

---

## 1. Vision

Today VAIrdict is **reactive**: it waits for PR events and judges them. AI-first VAIrdict is **proactive**: it engages from the moment an issue is opened, runs the Plan→Code→Judge pipeline autonomously through to PR, and continues to engage with review comments to deliver fixes.

The polarity flip is at the **entry point**: VAIrdict's pipeline starts at `issues.opened` and `issue_comment.created`, not just at `pull_request.opened`. Issues are where work originates; PRs are the output of work. By moving the entry point upstream, VAIrdict participates in the full development cycle rather than only the final review step.

**This is achieved without inventing new signals.** Humans file the issues; VAIrdict reasons about whether and how to act on them. No "Scout" agent that proposes work itself (that remains a future possibility, intentionally deferred).

### Guiding principles

- **Contributor, not owner.** VAIrdict participates on branches and PRs alongside humans. It never locks branches, never overwrites human work, always yields on conflict. Soft branch ownership.
- **Trust is earned per repo.** First-time-mode caps autonomy until merges accumulate. Reverts contract trust. The autonomy ladder is per-repo, observed from history, not configured.
- **Honest about limits.** Failure-mode comments name the mechanism that stopped work. No vague "encountered difficulties." Cost and capability are visible.
- **Cheap by default, expensive on signal.** Most work is small; pipeline depth scales with declared size and derived criticality. No over-engineering common cases.
- **Reactions for state, comments for new information.** Deduplication discipline keeps PRs readable.

---

## 2. Architecture overview

### 2.1 Roles

VAIrdict has four logical roles, all running under one GitHub App identity (`vairdict[bot]`). Roles are distinguished in comment headers and commit trailers, not by separate bot users.

| Role     | Purpose                                                     | Comment header     |
|----------|-------------------------------------------------------------|--------------------|
| Triager  | Evaluates new issues against gates (duplicate, conflicts, sufficient context) | 🔍 Triager — vairdict[bot] |
| Planner  | Produces structured plans (architecture, trade-offs, affected files) | 🧠 Planner — vairdict[bot] |
| Coder    | Implements plans and fixes via Claude Code                  | ⌨️ Coder — vairdict[bot]   |
| Judge    | Two-loop verdict: agent-facing (inner) and human-facing (outer) | ⚖️ Judge — vairdict[bot]  |

Triager is new in this plan; Planner/Coder/Judge already exist and gain new responsibilities and entry points.

### 2.2 Two-loop judging

The same Judge role runs in two distinct contexts:

- **Inner loop (Coder ↔ Judge, pre-PR):** structured, agent-facing. Output is a feedback packet for Coder to ingest in the next iteration. Examples: "test X failed at line 42," "build broke," "function signature doesn't match plan." Capped at N iterations (default 5, halved in first-time mode).
- **Outer loop (Judge on open PR, human-facing):** prose-formatted, narrative. Explains what was built, how it tracks the plan, what trade-offs were made. Posts the verdict comment that humans review.

Same role, same code, different prompts and output format. The split avoids the failure mode where one loop's prompt over-fits the other's audience.

### 2.3 Gates (composable, cross-stage)

Gates are first-class. Each gate has a name, an interface (`evaluate(context) → pass | fail | skip`, with a reason on fail/skip), and a list of stages it applies to. Stages compose gates declaratively in `vairdict.yaml`.

**`skip` semantics.** A gate may legitimately not apply to the work at hand — e.g., `within_plan_scope` on a human-authored PR with no plan. In that case the gate returns `skip` with a reason ("no plan exists for this PR; scope check not applicable"). `skip` does **not** mean `pass`. In Judge verdicts and status comments, gate results render explicitly:
- `✓ <gate>` — pass
- `✗ <gate>` — fail (with reason)
- `— <gate>` — skip (with reason)

Honest reporting that the gate didn't gate, and why. Humans reviewing the verdict can see at a glance which gates ran, which blocked, and which were inapplicable. Skipping is never silent.

**Gate inventory (initial set):**

- `is_duplicate` — issue or PR has substantial semantic overlap with existing open work
- `has_sufficient_context` — issue has enough information to plan against
- `conflicts_with_in_flight` — issue/PR overlaps another in-flight work item
- `plan_is_fresh` — plan's `created_against_sha` is current relative to repo state
- `within_plan_scope` — Coder's diff stays within plan's `affected_files` and acceptance criteria
- `no_conflicts_with_other_fixes` — fix doesn't touch files/lines that another queued fix is also targeting
- `build_passes` — repo's standard build/test commands succeed
- `requires_human_approval` — work touches `sensitive_paths` and auto-merge is gated on explicit `@vairdict go`

**Stage composition example:**

```yaml
stages:
  triage:           # on issues.opened
    gates: [is_duplicate, has_sufficient_context, conflicts_with_in_flight]
  fix_preflight:    # before each fix run
    gates: [plan_is_fresh, no_conflicts_with_other_fixes]
  inner_loop:       # between Coder iterations
    gates: [build_passes, within_plan_scope]
  outer_judge:      # on PR ready-for-review
    gates: [build_passes, within_plan_scope, requires_human_approval]
```

The same gate (`build_passes`) runs at multiple stages with different inputs. Gate logic lives once; composition is config.

### 2.4 Size and criticality

Two orthogonal dimensions, set independently:

- **Size (S/M/L)** — Planner's judgment of work scope. Drives pipeline depth (skip Planner for trivial S, fewer iterations for M, full pipeline for L). Set in plan output.
- **Criticality** — derived from `affected_files` matching `sensitive_paths` (configured in `vairdict.yaml`). Drives gate strictness (criticality-true work requires `requires_human_approval`, never auto-merges).

A small change to `auth/` is `size: S, critical: true` — quick path, strict gates, no auto-merge. A large UI refactor is `size: L, critical: false` — full pipeline, normal gates, auto-merge eligible on green.

### 2.5 Soft branch ownership

VAIrdict never locks branches. It observes HEAD between operations and adapts. All write operations follow: `read HEAD → do work → attempt push → on conflict, stand down with named-author message`. Humans always win races. Coder's in-progress work is preserved on `vairdict/issue-N-stashed-<timestamp>` branches when stood down, with the status comment linking to the stash.

### 2.6 Configuration tiers

VAIrdict supports two-tier configuration following the `.github` meta-repo convention:

- **Org tier:** `<org-name>/.vairdict` repo containing `vairdict.yaml` and shared learnings. Acts as defaults for all repos in the org. Optional — most installs have only repo-tier config.
- **Repo tier:** `vairdict.yaml` in the repo root. Committed to git, reviewed via PR like any code change. Overrides org-tier defaults.

**Precedence:**
- For **caps** (`monthly_cap_usd`, `cost_cap_per_run`): **stricter wins**, regardless of tier. Org sets $100/month, repo sets $200 → effective is $100. Org sets $50/month, repo sets $30 → effective is $30. Repos can be more conservative than the org, never less. Caps are a *constraint*; constraints flow down strictly.
- For **other scalars** (model choice, individual gate thresholds): repo wins. These are *preferences*; preferences allow override.
- For **lists** (`sensitive_paths`, `authorized_users`): merge.

**Welcome flow integration:** on install, VAIrdict checks for `<org>/.vairdict` and references it in the welcome issue. The repo-level `vairdict.yaml` stub generated by the welcome flow notes inherited org defaults and shows the override pattern.

---

## 3. Workflows

### 3.1 Issue → Plan → Code → PR

```
┌─────────────────┐
│ Issue opened    │
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Triager         │  Gates: is_duplicate, has_sufficient_context,
│                 │  conflicts_with_in_flight
└────────┬────────┘
         │
         ├─── any gate fails ────► failure-mode comment, stop
         │
         ▼ all pass
┌─────────────────┐
│ Wait for trigger│  Default: explicit @vairdict plan required.
│                 │  Optional auto-trigger via vairdict.yaml:
│                 │    triage.auto_plan: true            (all triage-passing)
│                 │    triage.auto_plan_on_labels: [...] (label-gated)
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Planner         │  Produces plan comment with size, affected_files,
│                 │  trade-offs, acceptance criteria
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Wait for go     │  @vairdict go (required before any code)
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Coder ↔ Judge   │  Inner loop on vairdict/issue-N branch.
│ (inner loop)    │  Up to N iterations. Cost-capped per run.
└────────┬────────┘
         │
         ├─── iterations exhausted ──► failure-mode comment, stop
         ├─── plan-needs-revision ───► back to Planner gate
         │
         ▼ converged
┌─────────────────┐
│ PR opened       │  Branch: vairdict/issue-N. PR body = plan + drift
│                 │  notes. Status comment kind: run_progress.
└────────┬────────┘
         │
         ▼
┌─────────────────┐
│ Outer Judge     │  Posts human-facing verdict. May auto-merge if
│                 │  not critical and gates pass (requires explicit
│                 │  config + post-first-time-mode).
└─────────────────┘
```

**Concurrency:** GitHub Actions `concurrency:` keyword, group keyed on `vairdict-pr-${pr_number}` for PR work and `vairdict-issue-${issue_number}` for triage/planning. `cancel-in-progress: false` for Coder/fix runs (queue them); `cancel-in-progress: true` for Planner runs (newest plan request wins).

**Re-planning mid-code:** Coder can emit a `plan-needs-revision` exit code from the inner loop. This pauses Coder, posts a comment requesting Planner re-run, and waits for `@vairdict plan` (which the human can run, or VAIrdict suggests). Avoids shipping plan-faithful but actually-broken code. **Capped at `plan_revision_limit` revisions per issue** (default 2; configurable). Counting: original plan + N revisions = N+1 total plans before the cap fires. With default 2, a third revision request (which would be the fourth plan) triggers a failure-mode comment and stops — the third re-plan almost always indicates the issue itself needs human attention.

### 3.2 Fix flow on review comments

```
Review comment by @alice ────► @bob replies "@vairdict fix"
                                        │
                                        ▼
                             👀 reaction immediately on the trigger comment
                             (acknowledgment, not gate-passed signal)
                                        │
                                        ▼
                             ┌──────────────────────┐
                             │ Authorization check  │  bob has write access?
                             └──────────┬───────────┘
                                        │ pass (fail → ❌ + threaded reply)
                                        ▼
                             ┌──────────────────────┐
                             │ Fix preflight gates  │  Status comment updates as
                             │                      │  gates evaluate (visible
                             │                      │  progress, not silence).
                             └──────────┬───────────┘
                                        │
                                        ├── conflict ──► ❌ on both, decline both
                                        │
                                        ▼ pass
                             ┌──────────────────────┐
                             │ Worktree allocation  │  Parallel if files disjoint
                             │                      │  from in-flight fix
                             └──────────┬───────────┘
                                        │
                                        ▼
                             ┌──────────────────────┐
                             │ Fix-size classifier  │  Trivial → 1 pass. Substantive
                             │                      │  → inner loop with gates.
                             └──────────┬───────────┘
                                        │
                                        ▼
                             ┌──────────────────────┐
                             │ Coder + Judge        │  Push to PR branch. Update
                             │                      │  fix_queue status comment.
                             └──────────┬───────────┘
                                        │
                                        ▼
                             Reactions on the trigger comment evolve:
                             👀 acknowledged (immediate) →
                             🔄 queued behind in-flight fix (if applicable) →
                             ✅ pushed | ❌ declined | 💬 needs clarification

                             Granular progress lives in the status comment;
                             the reaction is the at-a-glance state.
```

**Mode change worth being explicit about:** `@vairdict fix` works on **any review comment**, regardless of who wrote the original PR or the original review comment. This makes VAIrdict useful as a collaborator on human-authored PRs, not only on PRs it opened. Authorization (write access) is the gate that prevents drive-by abuse.

**Scope of `@vairdict fix`** — limited specifically to PR review comments (threaded on a line of code via `pull_request_review_comment.created`):

- *Reply to a PR review comment with `@vairdict fix`* → executes; uses `comment.in_reply_to_id` to find the parent review comment as the fix target.
- *Top-level PR conversation comment (not on a line of code) with `@vairdict fix`* → ambiguous, no file/line target. Reply: "please use `@vairdict fix` as a reply to a specific review comment." Don't guess.
- *Issue comment with `@vairdict fix`* → not applicable; issues don't have a diff to target. Treated as unknown command (❓ + threaded reply pointing to `@vairdict help`).

The trigger event must carry a fixable target (file path + line range + diff hunk) for the command to run. Anywhere else, fix is rejected gracefully.

**Fix without a plan (human-authored PRs):** when no plan exists for the PR, the `within_plan_scope` gate evaluates to `skip`, not `pass` or `fail`. The fix runs against a synthesized lightweight scope derived from the review comment's file path and line range — no plan to track against, but Coder is still constrained to the immediate area of the comment. Out-of-scope expansion in this mode triggers the same failure as `within_plan_scope` would have on a plan-backed PR.

**Rebase failure on parallel-fix merge-back:** when fix B's rebase against the just-pushed fix A fails due to conflicts, treat it as a `fix_conflict` failure-mode. ❌ on fix B's trigger comment, status comment updates, user re-issues if they still want it. Coder does not attempt automatic conflict resolution — the situation has already proven Coder's earlier preflight conflict check missed something, and forcing through is worse than asking.

**Forks:** when VAIrdict can't push to a fork PR (no "allow edits by maintainers"), fall back to posting a GitHub suggested-changes block the author can apply. Detection: try push, catch 403, switch mode. (See milestone-aif-fork in §6.)

### 3.3 Pause / Stop / Resume

| Verb | Scope | Durability | Effect |
|---|---|---|---|
| `@vairdict pause` | Per-PR | Until resumed | Mute all initiated activity on this PR. Aborts in-flight runs (work stashed). New commands during pause get ⏸ reaction only, no execution. Includes Judge auto-runs. |
| `@vairdict resume` | Per-PR | One-shot | Un-mute. Past commands during pause are not replayed. |
| `@vairdict stop` | Per-PR | One-shot | Aborts the currently in-flight run. Reactive behavior unchanged after. |

**Stop vs. Pause distinction:** stop kills *this run*; pause durably mutes *this PR*. Both abort in-flight work on invocation; they differ in what happens to subsequent commands.

**During pause:** even commands like `@vairdict help` work (otherwise users can't discover `resume` while paused). Help, status, usage are always-on. Authorization-gated commands respect normal write-access checks regardless of pause state.

### 3.4 Failure modes

All failures use one template, three sections:

```
🤖 VAIrdict — <one-line outcome>

What I tried:
  • <2-4 concrete bullets, with commit SHAs / iteration counts where relevant>

Why I stopped:
  <one paragraph naming the gate, limit, or event. No "encountered difficulties.">

What you can do:
  • <1-3 actionable next steps with exact `@vairdict ...` commands pre-typed>
```

**Failure kinds (failure templates live in code as constants):**

- `iterations_exhausted` — Coder + Judge couldn't converge
- `gate_blocked_<name>` — specific gate blocked (duplicate, conflicts, plan_stale, insufficient_context, requires_human_approval)
- `plan_needs_revision` — Coder bailed mid-execution; plan turned out wrong
- `fix_conflict` — fix-vs-fix conflict, both declined
- `cost_cap_run` — per-run cost cap exceeded
- `cost_cap_monthly` — repo monthly cap reached
- `human_takeover` — race detected, VAIrdict stood down
- `human_pushed_during_run` — human commit landed mid-iteration, work stashed
- `plan_stale` — plan's affected files drifted on main before `@vairdict go`
- `api_unavailable` — LLM API call failed (insufficient credits, rate limit, transient error)

**Tone rules:** no apology, no "Unfortunately," no emoji beyond the leading 🤖. First person. Past tense for actions, present tense for state.

**Deduplication:** before posting any failure-mode comment, check the last N comments on the same PR/issue for an existing comment with the same `failure_kind` within a short window (10 min). If found, react ❌ on the new trigger and update the existing comment ("also affected: 2 more invocations") rather than posting fresh. Same `comment_kind` marker pattern as status comments, with `failure_kind` in the JSON.

**API/billing failures don't durably block.** A run aborts, but VAIrdict keeps responding to future commands normally. Each new command is a fresh attempt. If the issue persists, each invocation gets its own ❌ + comment (subject to dedup).

---

## 4. Commands

| Command | Where invoked | Authorization | Purpose |
|---|---|---|---|
| `@vairdict plan` | Issue or PR | Write access | Generate or refresh a plan |
| `@vairdict plan --force` | Issue | Write access | Override duplicate-detection gate |
| `@vairdict go` | Plan comment thread | Write access | Approve plan, start coding |
| `@vairdict fix` | Reply to any review comment | Write access | Address that review comment |
| `@vairdict fix --thorough` | Reply to review comment | Write access | Force full pipeline (vs. trivial-fix shortcut) |
| `@vairdict fix --quick` | Reply to review comment | Write access | Force trivial path (skip inner loop) |
| `@vairdict pause` | PR | Write access | Mute VAIrdict on this PR until resume |
| `@vairdict resume` | PR | Write access | Un-mute |
| `@vairdict stop` | PR or issue | Write access | Abort current run |
| `@vairdict help [verb]` | Anywhere | None (always-on) | Show commands and current state |
| `@vairdict status` | PR or issue | None (always-on) | Show what's in-flight, current state |
| `@vairdict usage` | PR or issue (repo-scoped) | Write access | Show cost breakdown for the month |
| `@vairdict org usage` | PR or issue (any repo in the org) | Write access | Show org-aggregated cost breakdown across all repos. Requires `<org>/.vairdict` exists. (milestone-aif-7) |
| `@vairdict review` | PR | Write access | Run outer Judge on demand (milestone-aif-review) |
| `@vairdict freeze` / `@vairdict unfreeze` | Anywhere (repo-wide) | Admin | Repo-wide pause for release freezes (milestone-aif-freeze) |

**Unknown command handling:** `@vairdict <unknown>` → ❓ reaction + threaded reply "unknown command, try `@vairdict help`."

**VAIrdict invoking itself:** comments from `vairdict[bot]` skip command processing entirely. Hard skip before auth check, prevents loops.

**Authorization detail:** check via `GET /repos/{owner}/{repo}/collaborators/{username}/permission`. Permission level `write`, `maintain`, or `admin` allows; `read` or `none` blocks with ❌ + threaded reply. Cache per (user, repo) for ~1 hour.

---

## 5. Configuration

### 5.1 Repo-tier `vairdict.yaml`

```yaml
# Lives at repo root, committed to git, reviewed via PR.

# Sensitive paths drive criticality (gates run strict, no auto-merge)
sensitive_paths:
  - internal/auth/**
  - internal/payment/**
  - migrations/**

# Per-run cost caps (USD). Defaults shown. Configurable per-size.
cost_cap_per_run:
  S: 0.50
  M: 2.00
  L: 5.00

# Optional monthly cap. No system default — opt in to enable enforcement.
# Soft warning at 80%, hard stop at 100%.
monthly_cap_usd: null  # or e.g. 50.00

# Maximum plan revisions per issue (plan-needs-revision loop cap).
# Counts revisions *after* the original plan: original + N revisions = N+1
# total plans before the cap fires. Default 2 → up to 3 plans per issue.
# The (N+1)th revision request triggers a failure-mode comment and
# requires human intervention before VAIrdict re-engages.
plan_revision_limit: 2

# Authorization. Default is "any user with write access to the repo".
# Override only if stricter allowlist is needed.
authorized_users: null  # or e.g. [alice, bob]

# Auto-merge per fix class. Locked to false during first-time mode regardless.
auto_merge:
  trivial: false
  standard: false
  critical: false  # always false; sensitive-path criticality blocks

# Gate enable/disable per stage. Defaults to all-on.
stages:
  triage:
    gates: [is_duplicate, has_sufficient_context, conflicts_with_in_flight]
  inner_loop:
    gates: [build_passes, within_plan_scope]
  fix_preflight:
    gates: [plan_is_fresh, no_conflicts_with_other_fixes]
  outer_judge:
    gates: [build_passes, within_plan_scope, requires_human_approval]
```

**No env/secrets config in vairdict.yaml.** GitHub Actions handles env via standard `secrets.X` and `vars.X` mechanisms. The workflow file VAIrdict ships passes through whatever the repo has configured. If a repo's tests need a credential, the workflow already passes it. VAIrdict adds zero new env surface.

### 5.2 Org-tier `<org>/.vairdict/vairdict.yaml`

Same schema as repo-tier. Acts as defaults. Repos in the org inherit unless they override.

```yaml
# Lives at <org>/.vairdict/vairdict.yaml
# Org-wide defaults. Overridden by repo-level vairdict.yaml.

sensitive_paths:
  - "**/auth/**"
  - "**/payment/**"

cost_cap_per_run:
  S: 0.50
  M: 1.00  # tighter org-wide
  L: 3.00

monthly_cap_usd: 100.00  # default per repo unless repo overrides
```

**Precedence:**
- Caps (cost_cap_per_run, monthly_cap_usd): **stricter wins** — repos can be more conservative than the org, never less. Constraints flow down strictly.
- Other scalars: repo wins.
- Lists: merge.

**Discovery:** on every run, VAIrdict fetches `<org>/.vairdict/vairdict.yaml` (if exists, public or with App access) and merges with repo-tier config. Cached for the duration of a run; refreshed across runs.

---

## 6. State management

### 6.1 Status comments (per-PR/issue, GitHub-as-DB)

Each in-flight context (a fix queue, a run in progress, a triage result, a plan) has one status comment. The comment renders human-readable progress on top, with machine-readable JSON in an HTML comment underneath.

**Marker pattern:** `<!-- vairdict-state` opens, `-->` closes. Find via regex on PR/issue comments, exactly one per kind per context.

**Comment kinds:**

- `fix_queue` — on PRs during fix runs
- `plan` — on issues, contains plan body and `approval_state` (`pending` / `approved` / `superseded`). The state is read-modify-written when `@vairdict go` fires; if the state is already `approved`, the second `go` is a no-op with a threaded reply ("plan already approved, run in progress"). Prevents double-execution from concurrent `@vairdict go` invocations on the same plan. Re-running `@vairdict plan` flips the previous plan's state to `superseded` and creates a fresh `pending` plan.
- `run_progress` — during Coder execution (PRs or issues)
- `triage` — at issue-open, gate results

**Schema (illustrative — see code module for canonical):**

```json
{
  "schema_version": 1,
  "comment_kind": "fix_queue",
  "pr": 142,
  "items": [
    {
      "id": "c1",
      "review_comment_id": 1234567890,
      "state": "completed",
      "started_at": "2026-04-29T14:02:11Z",
      "completed_at": "2026-04-29T14:03:23Z",
      "commit_sha": "abc1234",
      "outcome": "pushed",
      "author": "alice"
    }
  ]
}
```

**Concurrency on the comment:** read-modify-write with optimistic retry. GitHub's PATCH on issue comments is last-write-wins; collisions are rare and operations on different items are commutative. On detected collision (compare against pre-read state), re-read and retry. No ETags, no distributed locks.

**Size management.** GitHub's per-comment limit is 65,536 characters. Long-running PRs with many fixes can approach this. Strategy: when a status comment exceeds 50KB (buffer below the limit), archive completed items to `archived/fix-queues/<pr>/<timestamp>.json` on the state branch, retain the active items + last 5 completed in the live comment, and append a "view full history" link. Archival happens transparently as part of the next state write; readers of the comment never need to fetch archived data unless they follow the link. Designed in milestone-aif-3 (when fix queues ship) rather than waiting for the issue to be observed in the wild.

**All state writes go through one module** (`internal/state/comment.go` or equivalent). Single source of parsing/serialization. When migration to a queryable store is needed (e.g., for a UI), only this module changes; callers are untouched.

**Schema is private.** Users see the rendered surface, not the JSON. We don't promise schema stability externally — only to ourselves via `schema_version`.

### 6.2 Cross-PR state (state branch)

State that spans PRs/issues lives on a `vairdict-state` branch:

- Telemetry JSONL records (one per run)
- Trust-ladder data (rolling merge/revert ratios)
- Repo-wide freeze state (milestone-aif-freeze)
- Install metadata
- Embeddings cache for duplicate/conflict detection (refreshed per main commit)

**Structure (illustrative):**

```
vairdict-state branch:
  /events/
    2026-04/
      run_abc123.json
      run_def456.json
      run_xyz789.json
    2026-05/
      run_...
  /state/
    repo.json          # autonomy level, install date, etc. (low frequency)
    freeze.json        # if currently frozen (low frequency, human-toggled)
    deliveries/        # processed webhook delivery IDs, one file per ID
      <delivery-id>    # empty marker file; presence = processed
      ...              # daily cleanup prunes files older than 24 hours
  /cache/
    embeddings/
      <main-sha>.bin   # last 10 retained, older pruned on each new write
  /archived/
    fix-queues/
      pr-142/
        2026-04-29T14-02.json
```

**Why per-run files for events.** Concurrent runs writing to the same `2026-04.jsonl` would conflict on push to the state branch (last-write-wins on git). Per-run files (`events/<YYYY-MM>/<run_id>.json`) eliminate write contention entirely — no two runs ever target the same path. Sharded by month for filesystem sanity. Aggregation walks the directory at read time; rare enough that the trade-off is right.

**State branch write strategy by file frequency.**

- **Per-run files** (events, archived fix queues) — no contention by construction; filenames are unique per run.
- **High-frequency shared files** — sharded with name-as-data: `state/deliveries/<delivery-id>` is an empty marker file; presence means "delivery processed." No content to merge, so no merge conflicts. Daily cleanup prunes old files. Same pattern applied to any future high-frequency shared state.
- **Low-frequency shared files** (`state/repo.json`, `state/freeze.json`) — optimistic-retry-with-rebase pattern matching status-comment writes. Mutations are rare and human-initiated (autonomy level changes once over many runs; freeze toggles by admin). Collisions are exceptional; retry is sufficient.
- **Embeddings cache** — keyed by main commit SHA, so writers for different SHAs never collide. Pruning of older entries is a single cleanup write per main-commit-update, run from a watcher rather than per-run.

**Event record (illustrative fields):**

```json
{
  "run_id": "run_abc123",
  "parent_run_id": null,
  "repo": "almog27/vairdict",
  "stage": "fix",
  "size": "S",
  "critical": false,
  "pr_or_issue": "pr/142",
  "trigger_user": "alice",
  "gates_evaluated": [
    {"name": "no_conflicts_with_other_fixes", "result": "pass", "duration_ms": 320}
  ],
  "roles_invoked": [
    {"role": "coder", "model": "claude-sonnet-4-7", "input_tokens": 4200, "output_tokens": 800, "cost_usd": 0.018}
  ],
  "compute": {"actions_minutes": 0.5, "cost_usd": 0.004},
  "outcome": "pushed",
  "human_action_after": null,
  "started_at": "2026-04-29T14:02:11Z",
  "completed_at": "2026-04-29T14:03:23Z"
}
```

**`human_action_after` is backfilled** by a watcher process that scans recently-merged VAIrdict PRs and updates records based on subsequent activity (merge/close/revert/edit/force-push-over). Without backfill, telemetry is activity logging without outcome — and the trust ladder (milestone-aif-trust) needs outcomes.

### 6.3 Welcome and journal artifacts

**Welcome issue** — one-time. Opens on `installation.created` with setup instructions and first-commands. User-owned: closeable, ignorable. VAIrdict does not edit it after first-time-mode graduation. (See milestone-aif-ONBOARD.)

### 6.4 Audit

VAIrdict's audit story spans **three tiers**, each with a different audience and store:

| Tier | Audience | Scope | Store |
|---|---|---|---|
| Per-repo | Repo owner | One repo's activity | State branch in that repo + `git log` on `vairdict.yaml` |
| Org-owner | Org admin (and repo write-access users in the org) | All repos in one org | Mirrored events at `<org>/.vairdict/events/` (built in milestone-aif-7) |
| Operator | VAIrdict service operator team | All installs across all orgs | Central operator store (deferred: milestone-aif-operator-audit) |

Three concrete audit concerns, mapped to the tiers above:

- **Activity audit** (who triggered what, when, how it ran): JSONL telemetry. Per-repo on state branch, mirrored to org-tier when configured. Every run records `trigger_user`, timestamps, role invocations, gates evaluated, outcome.
- **Config-change audit** (who edited config, when): free via git for both repo (`vairdict.yaml`) and org (`<org>/.vairdict/vairdict.yaml`). `git log` is the audit log. No additional infrastructure.
- **Cost audit** (what was spent, by what, when): same telemetry stream. Surfaced via `@vairdict usage` (per-repo) and `@vairdict org usage` (org-aggregated, when org-tier exists).

VAIrdict does not need a separate audit log database for per-repo or org-owner audit. The combination of (a) git history for declared intent and (b) state-branch + org-mirror telemetry for executed actions is the audit trail.

**Operator-side audit is qualitatively different.** Once VAIrdict serves users beyond the operator, the operator needs cross-install visibility (abuse detection, quality regression, incident response, compliance trails) that per-repo and org-tier audit cannot provide. This is `milestone-aif-operator-audit` (deferred). It does not replace the other two tiers; it complements them with a different audience and content rules. The events module's pluggable sink interface (specified in milestone-aif-7) is designed from day one specifically so the operator-audit sink can slot in without refactoring.

---

## 7. Distribution and surfaces

VAIrdict's user-facing surface is **GitHub commands and `vairdict.yaml`**. Users do not run VAIrdict locally. The bot runs as a GitHub App with Actions workflows; there is no per-user daemon, container, or local install for end users.

The Go CLI (`vairdict`) exists for two purposes:
- **VAIrdict's own development** (build, test, dogfood loops on this repo)
- **Org-tier admin tasks** (`vairdict org init`, etc.) — run rarely, by an org admin

The CLI lives at the existing entry point `cmd/vairdict/` in the Go module; the published binary stays `vairdict` with subcommands (no rename). The GitHub App webhook handler is a **separate process** whose language and runtime is decided at implementation time — it could be a Go binary at `cmd/vairdict-server/`, a Cloudflare Workers script (consistent with existing SPM infra), or a Lambda/serverless handler. The plan does not lock this in. Whatever runtime is chosen, shared logic (gate evaluation, plan parsing, status-comment marshaling, event emission) lives in language-portable libraries — Go packages under `internal/` if both processes are Go, or as a clearly-specified contract if the server is in another language.

Docker, self-hosted runners, local agents, and similar packaging are explicitly out of scope as user-facing concepts. They may exist as internal implementation details (e.g., the persistent-workspace optimization in `milestone-aif-perf` could use container images), but users never see them.

This is documented here so the question "should we ship a Docker image?" doesn't recur — the answer is "no, the surface is GitHub."

---

## 8. Cost and telemetry

### 8.1 Cost ceiling and kill switch

Four layers, ordered strictest-binds-first:

1. **Per-run hard cap** (LLM cost, USD). Defaults: S=$0.50, M=$2.00, L=$5.00. Configurable in vairdict.yaml per size. Exceeded → run aborts with `cost_cap_run` failure-mode comment naming the actual spend.
2. **Per-repo monthly cap** (LLM cost, USD). No system default — opt-in via `monthly_cap_usd: X` in repo-tier `vairdict.yaml`. Soft warning at 80%, hard stop at 100% (commands react ❌ + reply "monthly budget reached").
3. **Per-org monthly cap** (LLM cost, USD, aggregate across all repos). Opt-in via `monthly_cap_usd: X` in `<org>/.vairdict/vairdict.yaml`. Enforced by reading aggregated org-mirrored telemetry before each run. Same warning/stop thresholds. Enables "the whole acme org spends at most $500/month on VAIrdict" without per-repo budget micromanagement.
4. **`@vairdict stop`** for one-shot in-flight aborts.

When multiple caps apply (per-run + per-repo monthly + per-org monthly), all must pass — strictest binds.

Cost is **knowable**: Anthropic API returns `usage.input_tokens` and `usage.output_tokens` per call. Multiply by per-model rates. Aggregate per run, per repo per month, per org per month. No estimation.

**Check-after-call enforcement.** VAIrdict does not interrupt LLM calls in flight. Cost is checked after each completed call against the cap; if exceeded, no further calls are made within that run. The triggering call's cost is included in the spend recorded — the cap is a ceiling on completed cost, not a budget for in-flight requests.

**Compute cost (GitHub Actions minutes)** is reported separately, never mixed into LLM cost in the UI:

```
LLM:      $3.18 / $50.00
Compute:  $0.24 (GitHub Actions)
```

Caps apply to LLM only (the part VAIrdict controls via model/iteration choices). Compute is reported but not capped.

### 8.2 Cost-reducing strategies

- **Splitting L → sub-issues.** When Planner judges `size: L`, it considers whether to emit a single plan or a meta-plan proposing to split into 2-3 sub-issues. Each sub-issue runs its own pipeline. Cheaper because: smaller context per Coder run, fewer iterations, scoped failures, scoped human review. Default to single-plan for coherent L's; split for L's that decompose. Human approves either way at the `@vairdict go` gate.
- **Cheaper models per role.** Triage gates and Judge structural pass are classification — Haiku is fine. Coder needs the strong model. Planner benefits from strong-model for trade-off reasoning.
- **One-pass + gate by default.** Inner loop defaults to one Coder pass + Judge gates. On gate failure, structured failure-mode comment, human re-triggers if they want another attempt. Critical bucket keeps autonomous iterations because stakes justify cost.
- **Embeddings cache per main commit.** Duplicate/conflict detection embeddings computed once per main commit (post-merge hook), reused until next merge.

### 8.3 Telemetry questions to answer

The schema in §6.2 is designed around the questions we'll actually ask:

- Per gate: how often it fires, how often it blocks, false-positive rate (blocked then human overrode).
- Per fix class (trivial/standard/critical): success rate, median time, iteration count, cost.
- Per role: where time and tokens go.
- Per repo: which kinds of issues VAIrdict handles well vs. bails on.
- Failure taxonomy: gate-blocked, iterations-exhausted, human-took-over, reverted-after-merge.

**Privacy posture:** v1 is single-tenant (your own usage). When VAIrdict serves other orgs (multi-tenant), telemetry boundary work is needed (milestone-aif-multitenant): per-org isolation, opt-out config, file-content redaction.

---

## 9. Roadmap and milestones

This plan is large. Milestones are sized to be independently shippable; later milestones depend on earlier ones for shared primitives (gates, state module, status comments). The first milestone (milestone-aif-1) is the wedge — proves the entry-point shift works without committing to the full pipeline.

> **Convention:** milestone IDs use `milestone-aif-N` (AI-First) or `milestone-aif-<NAME>` for named follow-ons. To be added to global `ROADMAP.md`.

### milestone-aif-1 — Issue triage + context-aware Judge (the wedge)

**Goal:** Demonstrate the polarity flip end-to-end with minimum risk. Make Judge context-aware, and add issue-open as a new entry point with the duplicate-detection gate only.

**Scope:**
- New webhook handler: `issues.opened` triggers Triager.
- Triager runs `is_duplicate` gate against open issues + recent merged PRs, posts a triage status comment (`comment_kind: triage`) listing top-3 related items.
- Outer Judge gains a "context pass" before posting verdicts: embeds the diff + PR title, queries open PRs and recent issues, surfaces top-3 related items inline in the verdict comment.
- Embeddings cache on state branch, computed per main commit. **Cold-start behavior:** on cache miss (first triage in a fresh install, or new main commit not yet indexed), compute embeddings inline. Slower first run, eventually consistent. milestone-aif-6 later adds a background bootstrap on `installation.created` as a UX upgrade — both modes work; the bootstrap just makes the first triage fast.
- Gate framework module (composable, used by Triager and Judge).
- Status comment module (one-module pattern from §6.1, supporting `triage` kind).

**Out of scope:** Planner trigger, Coder execution, fix flow, all other gates.

**Exit criteria:** Opening an issue in a VAIrdict-installed repo posts a triage comment within 30 seconds. Outer Judge on a PR includes a "Related" section with linked items when relevant. Both behaviors observed end-to-end on the dogfood repo.

**Depends on:** existing Judge infrastructure.

---

### milestone-aif-2 — Plan-Code-PR pipeline with approval gates

**Goal:** Issue triage extends to full plan→code→PR autonomy with explicit human gates.

**Scope:**
- `@vairdict plan` command on issues. Authorization check (write access).
- Planner produces structured plan output per schema in §3.1 (size, affected_files with change_type, architecture decisions, trade-offs, acceptance criteria, out_of_scope, created_against_sha).
- Plan posted as `comment_kind: plan` status comment with freshness header.
- `plan_is_fresh` gate (diff `created_against_sha` against main, intersect with `affected_files`).
- `@vairdict go` command — required to proceed to coding.
- `vairdict/issue-N` branch created on `go`.
- Coder ↔ Judge inner loop on the branch. Up to 5 iterations (3 in first-time mode).
- **Coder execution accepts a cancellation context from day one** (Go `context.Context` or equivalent propagated through LLM calls, build steps, gate evaluation; checked between major steps). Both *internal aborts* (cost cap exceeded, iterations exhausted, plan-needs-revision) and *external aborts* (eventually `@vairdict stop`, pause from milestone-aif-4) funnel through the same cancellation path so cleanup is uniform — work stashing, status comment finalization, telemetry write-out. External-trigger wiring is unexposed in this milestone (added in milestone-aif-4); the path itself exists from the start so cost-cap aborts in milestone-aif-5 have somewhere to plug in.
- Inner-loop gates: `build_passes`, `within_plan_scope`.
- `plan-needs-revision` exit from inner loop; suggests `@vairdict plan` to refresh. **Capped at 2 plan revisions per issue** (configurable). Third revision request triggers a failure-mode comment and stops.
- PR opens against main on convergence. PR body = plan + drift notes if applicable.
- Outer Judge runs on PR (continues using context pass from milestone-aif-1).
- Re-running `@vairdict plan` updates the plan in place (same comment, new content, new `created_against_sha`).

**Out of scope:** fix flow, auto-merge, sub-issue splitting, cost ceilings (yet).

**Exit criteria:** Opening an issue, running `@vairdict plan`, then `@vairdict go` results in a PR opened by VAIrdict with a passing build, observable on the dogfood repo. Plan staleness correctly detected when main moves.

**Depends on:** milestone-aif-1.

---

### milestone-aif-3 — Fix loop on PR review comments

**Goal:** `@vairdict fix` works on review comments (VAIrdict's PRs and human-authored PRs), with conflict detection and parallel execution.

**Scope:**
- `@vairdict fix` command on review comment replies (uses `comment.in_reply_to_id` from `pull_request_review_comment.created` event).
- Authorization check (write access).
- `no_conflicts_with_other_fixes` gate (file + line-range overlap, plus semantic-conflict LLM check).
- `fix_queue` status comment kind, with reaction-as-state on the targeted review comment (👀, 🔄, ✅, ❌, 💬).
- Fix-size classifier (trivial vs. substantive). Trivial path: 1 Coder pass + format/vet/build. Substantive path: inner loop with gates.
- Parallel worktrees for non-conflicting fixes (cap at 2-3 concurrent). Sequential merge-back via rebase.
- `--thorough` and `--quick` flags to override classifier.
- Idempotency: `@vairdict fix` on already-handled comment (existing ✅) replies with link to commit.

**Out of scope:** fix-batch optimization (single Coder run for multiple fixes — deferred), `@vairdict review` on demand.

**Exit criteria:** Three `@vairdict fix` invocations within 30 seconds on non-conflicting comments execute in parallel. Conflicting fixes both decline with named-author refs. Fix queue status comment updates correctly. Latency measured and recorded; if substantive-fix latency consistently exceeds the abandonment threshold, that triggers `milestone-aif-perf` (see future milestones).

**Depends on:** milestone-aif-2 (plan, gates, state module).

---

### milestone-aif-4 — Pause / Stop / Help / Status / Usage

**Goal:** Control surface and discoverability commands.

**Scope:**
- `@vairdict pause` / `@vairdict resume` per-PR. Pause aborts in-flight (work stashed), mutes initiated activity, ⏸ reaction-only on subsequent commands. Resume drops everything during pause; nothing replays.
- `@vairdict stop` one-shot abort of current run. ⏹ reaction on the trigger; status comment updates to "stopped by @user."
- `@vairdict help [verb]` always-on, dynamic content (current state, what's in-flight, paused/active).
- `@vairdict status` always-on, dynamic state without command list.
- `@vairdict usage` repo-scoped, by-phase + by-outcome breakdown, LLM and compute reported separately.
- Unknown command handling: ❓ + threaded reply.
- This milestone introduces no new failure-mode templates — pause/stop/help/status/usage produce reactions and status updates, not failure-mode comments. Templates for human_takeover and human_pushed_during_run ship with milestone-aif-7 alongside the soft-branch-ownership detection that emits them.

**Out of scope:** repo-wide freeze, on-demand review.

**Exit criteria:** Pause durably mutes the PR; resume restores. Stop aborts cleanly with stashed work recoverable. Help works during pause. Usage matches telemetry totals to the cent.

**Depends on:** milestone-aif-3.

---

### milestone-aif-5 — Cost ceilings and failure templates

**Goal:** Production-grade cost discipline and trustworthy failure UX.

**Scope:**
- Per-run hard caps enforced check-after-call (read accumulated token usage × rate after each completed LLM call; if cap exceeded, no further calls within this run).
- Per-repo monthly cap (opt-in). Soft warning at 80%, hard stop at 100%.
- LLM and compute cost separately tracked and reported.
- Failure-mode template module: shared infrastructure (template format, parameterization, dedup logic) plus all failure templates whose underlying concepts have shipped by this milestone. That set: `iterations_exhausted`, `gate_blocked_<name>`, `plan_needs_revision`, `fix_conflict`, `cost_cap_run`, `cost_cap_monthly`, `plan_stale`, `api_unavailable`. Templates for branch-ownership failures (`human_takeover`, `human_pushed_during_run`) ship in milestone-aif-7 alongside the detection behavior that emits them — no stubs.
- Failure-mode dedup: same failure_kind on same context within 10 min updates existing comment instead of posting new.
- API/billing failures use `api_unavailable` template. No durable mute — each new command is fresh attempt.
- Cost cap failure includes actual spend in the comment.

**Out of scope:** dynamic cap adjustment based on merge rate, per-user caps. Org-aggregate cap (deferred to milestone-aif-7 where org-mirrored telemetry exists).

**Exit criteria:** Inner-loop bug that would spin infinitely is caught by per-run cap and produces a clean failure-mode comment. All failure kinds owned by this milestone have tested templates rendered correctly. Dedup verified against repeated triggers.

**Depends on:** milestone-aif-3 (the fix flow is the cost-intensive use case to test caps against; control commands in milestone-aif-4 are independent of cost ceilings, which is why §10 recommends shipping milestone-aif-5 before milestone-aif-4).

---

### milestone-aif-6 — Onboarding and welcome flow

**Goal:** First-impression UX and first-time-mode safety net.

**Scope:**
- `installation.created` webhook handler.
- Welcome issue: single issue with 3-line summary, default `vairdict.yaml` stub (with org-tier reference if `<org>/.vairdict` exists), four key commands (`plan`, `go`, `fix`, `help`), invitation to try.
- **Embeddings bootstrap (background).** On install, asynchronously compute initial embeddings for the candidate set (recent issues, open PRs) and write to the embeddings cache on the state branch. Welcome issue posts immediately; bootstrap runs in the background and is typically done before the user files their first issue. First triage on a still-warming cache falls back to in-line embedding compute (slower but correct). Avoids the cold-start cliff where the first triage on a busy repo is unexpectedly slow.
- First-time mode behaviors:
  - Plan comment includes "first-time mode" banner.
  - Inner-loop iteration cap halved (5 → 3) for first 3 PRs.
  - Auto-merge disabled regardless of config until 3 VAIrdict PRs merged.
- Welcome issue is user-owned: VAIrdict does not edit it after first-time-mode graduation.
- Org-tier discovery: check for `<org>/.vairdict` on install, reference in welcome issue.
- `installation.deleted` handler: stop in-flight runs, leave history (PRs/comments/branches) untouched, no destructive cleanup.

**Out of scope:** repo scanning to propose `sensitive_paths`, suggested first issue, org-level summary repo, journal.

**Exit criteria:** Installing VAIrdict on a fresh repo produces exactly one welcome issue with correct content. First three VAIrdict PRs run in first-time mode with halved iterations and disabled auto-merge. Uninstalling leaves the repo cleanly.

**Depends on:** milestone-aif-1 (App identity established). Note: first-time-mode graduation counts merged VAIrdict PRs via the GitHub API (`GET /repos/.../pulls?state=closed` filtered by author=`vairdict[bot]`), not via telemetry. Telemetry from milestone-aif-7 would also answer this but is not required.

---

### milestone-aif-7 — Telemetry, rollback signal, soft branch ownership

**Goal:** Observability foundation and the missing safety primitives.

**Scope:**
- Single events module (`internal/events/` or equivalent) emitting structured run records via a **pluggable sink interface**. Day-one sinks: (a) state branch sink writing per-run JSON files at `events/<YYYY-MM>/<run_id>.json` (per-repo authoritative copy, no write contention by design), (b) org-mirror sink writing to `<org>/.vairdict/events/<repo>/<YYYY-MM>/<run_id>.json` when the org-tier repo exists. Interface designed so additional sinks (operator-audit endpoint, analytics service, etc.) can be added in later milestones without changes to call sites.
- Per-run event files per §6.2 schema. Mirrored to org-tier when applicable.
- Org-mirror is conditional: repos in orgs without `<org>/.vairdict` write only to the state branch. The moment `vairdict org init` runs, future events mirror to org-tier. No backfill of historical events (deferrable as a one-time migration).
- Cross-run org-config cache: 5-minute TTL keyed on `<org>/.vairdict@<sha>`. Fresh fetch when SHA changes. Avoids hitting GitHub API on every trigger in high-frequency repos.
- Embeddings cache retention: keep last 10 main commits' embeddings, prune older on each new write. Prevents unbounded state branch growth.
- `@vairdict org usage` command — org-aggregated cost breakdown by repo, user, phase, outcome. Reads from `<org>/.vairdict/events/`. Authorization: org admin or repo write access (the data is the org's own).
- **Aggregate org-level cost cap enforcement.** When `<org>/.vairdict/vairdict.yaml` declares `monthly_cap_usd`, every run reads org spend across all repos before deciding whether it's allowed to proceed. Per-repo monthly cap and per-run cap remain independent (multiple caps stack; strictest binds).
- `human_action_after` watcher: dual-mode. Webhook handler on `pull_request.closed` for immediate close/merge signals; daily scheduled job for revert detection (looks back 7 days for revert commits referencing VAIrdict-merged SHAs). Both modes write back to the run's event file in place.
- Rollback detection: revert commits within 7 days of a VAIrdict merge mark the original run `reverted` in telemetry.
- Webhook delivery idempotency: record `X-GitHub-Delivery` IDs as marker files in `state/deliveries/<delivery-id>` (no contention since each ID is unique). Daily cleanup prunes files older than 24 hours. Duplicate deliveries skip processing. Separate concern from the 10-minute content-level dedup window for failure-mode comments.
- GitHub API rate-limit handling: cache aggressively (permission checks, comment reads, embeddings), use conditional requests (ETags) where applicable, monitor secondary rate-limit response headers in telemetry, back off automatically on 403/429.
- Soft branch ownership: pre-push HEAD check on every Coder push; on conflict, abort, stash to `vairdict/issue-N-stashed-<timestamp>`, post status update with named author.
- Human-pushed-during-run detection and handling per §3.4 (`human_pushed_during_run` failure).
- Branch ownership exit: detect when human commits exceed VAIrdict commits significantly or VAIrdict's last activity is stale; gracefully exit with comment.
- Org-tier config fetch: read `<org>/.vairdict/vairdict.yaml` per run (with cross-run cache above), merge with repo-tier, cache for run duration.

**Out of scope:** trust ladder enforcement (milestone-aif-trust), org-wide pause.

**Exit criteria:** Reverting a VAIrdict-merged PR results in a telemetry record marked `reverted` within 7 days. Pushing to a `vairdict/issue-N` branch mid-iteration causes Coder to stand down with stashed work and a named-author comment. Org-tier config defaults are observable in run behavior. `@vairdict org usage` from any repo in an org with `<org>/.vairdict` returns aggregated cost by repo and user. An org-level monthly cap is enforced across repos.

**Depends on:** milestone-aif-6.

---

### Future milestones (named, design-complete, deferred)

These are designed enough to scope but not v1. Listed here so the global roadmap captures them.

#### milestone-aif-docs — Documentation and README

User-facing documentation across surfaces. Repo `README.md` (what VAIrdict is, install, first commands, link to docs site). Docs site at vairdict.dev (full user docs: commands, configuration, gates, cost, troubleshooting). Per-milestone, the docs for that milestone's surface ship with it as part of the milestone's exit criteria — this milestone is for the *holistic documentation pass* once the v1 surface stabilizes (after milestone-aif-7), and for ongoing docs governance. **Depends on:** milestone-aif-1 onward (each contributes its own surface docs). **Triggers:** v1 surface complete, ready for external users.

#### milestone-aif-perf — Latency optimization (persistent workspace, etc.)

If measured fix latency consistently exceeds the abandonment threshold (substantive fixes > 4 min p50, or trivial fixes > 90s p50), pull in latency optimizations:

- Persistent warm workspace per active PR (Fly machine or self-hosted runner with repo cloned and deps installed). Eliminates cold-start + setup, the dominant share of latency.
- Pre-warming runners on `pull_request.opened` for PRs likely to receive review comments.
- Caching of build artifacts across iterations.

**Trigger condition:** observed latency, not assumed. Don't pre-optimize. **Depends on:** milestone-aif-3 (fix flow) and milestone-aif-7 (telemetry to measure latency).

#### milestone-aif-trust — Trust ladder enforcement

Use telemetry's rolling 30-day merge-vs-revert ratio per repo per fix-class to drive autonomy levels (L1 surface only / L2 draft PR / L3 auto-merge on green Judge). Merges expand autonomy; rejections contract; reverts contract sharply. Per fix class (trivial/standard/critical), so dep bumps don't crowd out refactors. **Depends on:** milestone-aif-7 telemetry and rollback signal.

#### milestone-aif-freeze — Repo-wide pause for release freezes

`@vairdict freeze` / `@vairdict unfreeze` invocable on any issue or PR, applies repo-wide. Authorization: admin. State stored on state branch (`/state/freeze.json`), not on welcome issue (welcome can be closed). Same semantics as per-PR pause, wider scope. **Depends on:** milestone-aif-4 (per-PR pause), milestone-aif-7 (state branch).

#### milestone-aif-review — `@vairdict review` on demand

Opt-in passive Judge pass: runs gates, posts findings, doesn't auto-merge or push. Useful during pause and on human-authored PRs. Outer-loop Judge separated from auto-trigger. **Depends on:** milestone-aif-4.

#### milestone-aif-fork — Fork PR fallback

When VAIrdict can't push to a fork PR (no "allow edits by maintainers"), fall back to GitHub suggested-changes blocks. Detection: try push, catch 403, switch mode. Needed to be useful on open-source repos. **Depends on:** milestone-aif-3.

#### milestone-aif-scout — Proactive opportunity detection (deferred indefinitely)

A "Scout" role that surveys signals (stale deps, coverage gaps on hot files, doc drift, repeated reviewer comments → candidate lint rule) and emits opportunities to a queue Planner pulls from. Cap with an "initiative budget" tied to the trust ladder. **Explicitly deferred:** the entry-point shift to issues delivers most of the AI-first value. Scout adds risk and noise without proportional benefit until the rest is mature. Revisit after milestone-aif-trust is operational.

#### milestone-aif-multitenant — Per-tenant telemetry isolation

Concerns the *user-facing* side of multi-tenant telemetry: ensure that when VAIrdict serves multiple orgs, each org's telemetry on their state branch contains only their own data, with file-content redaction (paths and counts only, no diff content), per-tenant opt-out (`telemetry: false`), and no cross-tenant data bleed. This is about what each install owner can see — not about what the operator sees. **Depends on:** milestone-aif-7. **Trigger:** first non-operator install.

#### milestone-aif-operator-audit — Operator-side audit and observability

Concerns the *operator-facing* side: when VAIrdict serves users beyond the operator (i.e., installs in repos/orgs other than the operator's own), per-repo audit (telemetry on state branch, `@vairdict usage`) is necessary but not sufficient. The operator needs cross-install visibility for abuse detection, quality regression analysis, incident response, capacity planning, and eventual compliance audit trails.

This milestone is **distinct from per-repo and per-tenant telemetry**. Different audience (operator team, not repo owner), different store (central, not per-install), different access model (restricted to operator team, never exposed to repo owners), different content rules (run metadata only, never diff content), different retention.

**Scope:**
- Events emitted from each install to a central operator endpoint, async/best-effort, never blocking a run. If the endpoint is down or unreachable, runs continue normally and the per-install state branch retains the record.
- Run metadata only: timestamps, gates, role invocations, token counts, costs, outcomes, failure kinds, optionally file paths (config-gated). **Never:** diff content, file content, comment bodies, plan text, code.
- Pluggable sink interface in the events module (specified in milestone-aif-7) so the operator sink slots in without changes to call sites.
- Per-install privacy controls: `telemetry_to_operator: off | minimal | detailed` in `vairdict.yaml`. Default to `minimal` (counts and outcomes, no paths). Operators of self-installed VAIrdict (single-tenant) leave it `off`.
- Central store choice deferred to implementation time. Candidates: Cloudflare R2 + D1 (consistent with existing SPM infra), PostHog self-hosted, ClickHouse, Honeycomb. Selection based on volume and operational preferences when built.
- Operator dashboard: real-time activity across installs, p-tile latency, error rates, cost summaries, alerting hooks. Restricted access — never a public surface.
- Retention: operator-side independent of per-install state branch retention. Defaults to 90 days hot / 1 year cold (revisitable when built).
- Compliance posture: design accommodates SOC2 audit logging and GDPR right-to-delete for when VAIrdict has paying customers. Not implemented on day one; structure permits adding without re-architecting.

**Trigger condition:** first external (non-operator) install of VAIrdict. Single-tenant operation does not justify this work — `git log` over state branches in operator-owned repos is sufficient. **Depends on:** milestone-aif-7 (events module with pluggable sink interface), milestone-aif-multitenant (privacy boundary).

**Why named now despite being deferred:** earlier milestones (especially milestone-aif-7) need to be designed operator-friendly — stable event schemas, pluggable sink interface, no schema assumptions that break when a second sink is added. Naming this milestone enforces those design choices upfront, instead of refactoring later.

#### milestone-aif-journal — Long-term journal artifact

Running record of VAIrdict's behavior in a repo (revert detected, autonomy level changed, monthly summary). Lives on state branch as `/state/journal.md` or stable issue (TBD). Separate from welcome issue. **Depends on:** milestone-aif-7.

#### milestone-aif-dashboard — Web UI

When justified by usage scale, a web dashboard for gate config, telemetry browsing, multi-repo view. Triggers migration of status-comment state from inline JSON to a queryable store (the one-module pattern in §6.1 makes this clean). **Depends on:** all above.

---

## 10. Sequencing recommendation

For a single-developer pace, the right order is **milestone-aif-1 → milestone-aif-2 → milestone-aif-3 → milestone-aif-5 → milestone-aif-4 → milestone-aif-6 → milestone-aif-7**, with one exception from the numbered order: **cost ceilings (milestone-aif-5) before control commands (milestone-aif-4)**. Reason: an autonomous pipeline shipped without cost discipline is a runaway-cost risk; control commands (pause/stop) help debugging but aren't the primary safety net. Caps are.

Future milestones (milestone-aif-trust, milestone-aif-freeze, etc.) are pulled in opportunistically when milestone-aif-7's primitives are available.

---

## 11. Open implementation questions (intentionally deferred)

These don't block the plan but should be revisited at the implementation milestone where they apply:

- **Embeddings model and provider** — could be Voyage, OpenAI, or self-hosted. Decide at milestone-aif-1 with the cheapest viable option; revisit if quality is insufficient. (Note: model selection is generally tracked in the global VAIrdict roadmap, not here — this entry is only for embeddings, which are AI-first-specific.)
- **Embeddings cache invalidation strategy beyond per-main-commit** — for repos with very fast-moving main, may need finer granularity. Defer until observed.
- **Failure-mode template content tuning** — initial templates ship as designed; revisit after first month of dogfood usage with telemetry feedback.
- **Long-term journal location** — `/state/journal.md` on state branch vs. a stable issue. Decide when journal is implemented (currently in milestone-aif-journal, deferred).
- **Plan content schema versioning beyond status-comment `schema_version`** — when the plan content schema (architecture_decisions, trade-offs, acceptance_criteria fields) evolves, parser branches on version. Old plans remain readable; re-running `@vairdict plan` produces a new plan with new schema. Migration of in-flight plans isn't done — they stay on the version they were created with. Revisit if schema needs to evolve mid-milestone.
- **Squash-on-merge enforcement** — VAIrdict squashes when *VAIrdict* merges (auto-merge case, gated and rare). When humans merge, VAIrdict has no control; the recommendation lives in the welcome issue. The branch keeps the iteration trail regardless. No additional enforcement needed unless repos report drift.

---

## 12. Decisions log (for reference)

Decisions reached in design conversation, recorded so future planning doesn't re-litigate:

| Decision | Reasoning |
|---|---|
| Single GitHub App identity, not three | Three apps = three installs, three permission grants, branch-protection complexity. Role visible via comment headers and commit trailers. |
| Authorization = write access to repo | Right balance of openness and abuse prevention. Configurable allowlist if stricter needed. |
| Pause = full mute including auto-Judge | Half-pause is a worse abstraction. CI analogy: paused workflows don't run, period. |
| Pause does not queue commands | CI doesn't run on commits during a maintenance window. Reaction-only acknowledgment. |
| `@vairdict fix` works on any comment, not only VAIrdict's | Makes VAIrdict useful on human-authored PRs. Authorization gate prevents abuse. |
| Fix conflicts decline both, not pick one | Forces explicit human resolution. Avoids "why did it pick A's version" debugging. |
| Gates as composable first-class concept | Same gate runs at multiple stages (build_passes at inner-loop and outer-judge). Easy add/disable, future UI. |
| Size (S/M/L) and criticality are orthogonal | Size = how big, criticality = how careful. Different policies, composable. |
| No env/secrets config in vairdict.yaml | Actions handles env. We were solving an imagined problem. |
| Status comment co-locates JSON + human surface | Atomic updates, no infra. Migration to store is clean via single-module pattern. |
| API failures don't durably block | Each new command is a fresh attempt. Transient issues don't permanently mute the bot. |
| Reactions for state, comments for new info | Deduplication discipline. Prevents PR comment spam. |
| Welcome issue is user-owned, state lives elsewhere | Closing welcome shouldn't break VAIrdict. State on state branch. |
| Per-run cost cap has system defaults; monthly cap does not | Per-run cap is safety (always-on); monthly cap is budget (user opt-in). |
| Cost is exact, not estimated | Anthropic API returns token usage per call. Compute is GitHub-billable per-job. Both knowable. |
| Two-tier config: org `.vairdict` repo + repo `vairdict.yaml` | Follows `.github` meta-repo convention. Lists merge, scalars replace, caps stricter-wins. |
| Three audit tiers: per-repo, org-owner, operator | Different audiences, different stores, different access models. Conflating them produces wrong access controls. |
| Telemetry mirrors to `<org>/.vairdict/events/` from day one in milestone-aif-7 | Org-owner audit is a near-term concern (any org with multiple repos). Mirroring is free (it's a commit), and capturing data from day one means dashboards and aggregate caps are implementable when needed without backfill. |
| Aggregate org-level cost cap is a real enforcement layer | Multiple repos in an org without aggregate cap means no upper bound on total spend. Aggregate cap reads from org-mirrored telemetry, enforces "whole org spends at most X/month." |
| Pluggable sink interface for events from day one | Adding org-mirror + later operator-audit + dashboard sinks without refactoring requires the interface in place at the start. Cost of designing it correctly once: small. Cost of retrofitting: real. |
| Splitting L → sub-issues as a Planner option | Cheaper, scoped failures, per-sub-PR human review. Planner judges; human approves. |
| Failure-mode templates as code constants, not config | Product surface, not user config. One file, ~10 templates, parameterized. |
| Cheap models (Haiku) for triage and structural Judge passes | ~70% of LLM calls are classification — Haiku is sufficient. |
| Soft branch ownership, humans always win races | VAIrdict is contributor, not owner. No locks, no three-way merges. |
| Squash on merge, iteration trail on branch | Clean PR history, debuggable agent trail. |
| Schema_version: 1 from day one | Migration cost without it is "hope every existing comment parses." |

---

## 13. Glossary

- **Inner loop** — Coder ↔ Judge iteration before any PR exists. Agent-facing feedback.
- **Outer loop** — Judge running on an open PR. Human-facing verdict.
- **Trust ladder** — Per-repo autonomy level (L1/L2/L3) driven by merge/revert ratio. milestone-aif-trust.
- **First-time mode** — Reduced-autonomy behavior for first 3 VAIrdict PRs in a new repo install.
- **Soft branch ownership** — VAIrdict observes and yields rather than locking branches.
- **Wedge** — milestone-aif-1: minimum scope that proves the polarity flip without committing to full pipeline.
- **Sensitive paths** — `vairdict.yaml`-configured globs that mark files/components as critical, raising gate strictness.
- **Status comment** — Per-PR/issue comment with rendered surface + JSON state in HTML comment.
- **State branch** — Long-lived `vairdict-state` branch holding cross-PR state, telemetry, and embeddings cache.
- **Plan staleness** — Plan whose `created_against_sha` has drifted relative to current main, intersected with `affected_files`.
- **Size (S/M/L)** — Planner's judgment of the *scope of work for an issue/plan*. Set in plan output, drives pipeline depth (skip Planner for trivial S, fewer iterations for M, full pipeline for L). Lives on the work item (issue/plan/PR).
- **Fix class (trivial / substantive)** — applies *only to fixes* (review-comment-driven micro-changes). Set by the fix-size classifier in milestone-aif-3, derived from comment content + scope of the targeted diff. Lives on the individual fix. *Distinct from size:* a fix is always a small unit of work (no S/M/L applies); a plan never has fix class (the concept is review-comment-specific). Fixes happen on PRs that may or may not have an associated plan.
- **Criticality** — derived attribute (not configured per work item). True when any of the work's `affected_files` matches `sensitive_paths`. Drives gate strictness regardless of size or fix class.
