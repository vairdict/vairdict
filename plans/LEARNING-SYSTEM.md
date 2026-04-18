# VAIrdict Learning System — Design

Status: Draft
Audience: VAIrdict contributors
Owner: vairdict maintainers

## 1. Motivation

VAIrdict's plan/do/judge loop generates a uniquely high-signal view of what goes wrong during AI-assisted development. When the Judge rejects a PR, or when planning takes multiple iterations to converge, the causal chain is fully observable: what was asked, what was proposed, what was done, why it was rejected, and what eventually worked.

Most "self-improving agent" tooling treats failure as a low-signal event (a bash command returned non-zero, a tool failed). VAIrdict sees strategic failures — "this plan was wrong because it assumed X" — which are far richer learning signals.

This document specifies the system that captures those signals, grades them, stores them, and surfaces them in future loops so the same mistakes don't repeat.

### Two classes of knowledge the system addresses

**Reactive learnings** — produced when a loop fails or iterates: "Planner assumed synchronous I/O but this codebase is async throughout."

**Proactive learnings** — contributed by humans via `@vairdict add-learning` when they want to record institutional knowledge before it causes a failure: "Always validate webhook signatures before parsing body."

Both flow through the same pipeline and land in the same storage.

### What this system is not

It is not a general-purpose agent memory layer (Engram, Mem0, ContextPool already fill that niche). It is not a knowledge base for non-engineering content. It is not a replacement for `AGENTS.md`, `CLAUDE.md`, `.cursorrules`, or other human-authored agent instruction files — those capture intentional, always-loaded conventions; this system captures emergent, retrieval-loaded lessons.

### Scope of this document

This document specifies **architecture and behavior**: data model, file layout, write path, read path, governance. It deliberately does not cover:

- **Testing strategy.** Implementation tests follow the conventions in `CLAUDE.md` (mandatory tests, golden-file tests for file format stability, mocked LearnableSignals for write-path coverage). Details belong in the test files themselves, not in this design.
- **Observability.** Operational visibility (counts, retrieval hit rates, grade drift) is implemented as CLI commands (`vairdict learning stats`) and slog output at appropriate verbosity. Specified in each milestone's implementation, not architecturally.
- **Exact prompt text** for Planner/Coder/Judge learning-related invocations. Prompts are tuned during implementation; this document specifies *what* each role does, not *how* it's prompted.

## 2. Scope and non-goals

### In scope (milestone-learning-1 through milestone-learning-4)

- Machine-tier and repo-tier learning storage, file-based, markdown format.
- Judge-driven write path (plan/do/judge applied to learnings themselves).
- Retrieval bounded by token budget, filtered by scope and topic.
- All three loop roles (Planner, Coder, Judge) consume learnings with role-specific framings.
- Grading with reinforcement on use, categorized decay, protected tier.
- `@vairdict` PR commands for reviewer control.
- Monorepo-aware file layout with service-level scope.
- Org-tier support via a separate `<org>/.vairdict` repo.
- Org repo bootstrap (`vairdict org init`) and graceful degradation when missing or inaccessible.
- Auto-PR against the org repo when repo-level overrides are created.
- SHA-based freshness via committed per-tier `manifest.json`.

### Deferred to later milestones (milestone-learning-5+)

Each of the following has a dedicated milestone with scope defined in the roadmap.

- MCP server for external-agent consumption.
- Multi-model `learning_judge` for independent grading.
- Consumption skill distributed via SPM for non-VAIrdict agents.
- Automated cross-repo pattern detection and org PR proposals beyond override-triggered.
- `version` and `ticket` resolvers for `resolves_when`.
- Automatic eviction with maintenance PRs.

### Explicit non-goals

- **No hosted service or database backend.** Files in git are the storage.
- **No global/shared-across-orgs tier.** Security baselines and similar broad rules belong in existing tooling (linters, CI checks, SPM packages), not in this system.
- **No cross-tier atomic updates.** Repo and org are independent stores with explicit override semantics.

## 3. Architecture overview

### Tiers

- **Machine tier** — `~/.vairdict/learnings/`. Personal, local-only, gitignored. Used for developer-specific notes the team shouldn't see.
- **Repo tier** — `.vairdict/learnings/` in the project repo. Team-shared, git-tracked, authored by Judge via PR diffs.
- **Org tier** — a dedicated repo, by default `<org-name>/.vairdict`, containing cross-repo organizational learnings. Same file format as repo tier; fetched alongside. Naming follows the `.github` meta-repo convention (see §10.1 for details and exceptions). Lifecycle managed via `vairdict org init` and related commands (§10.1).

No hosted service. Git is the transport, storage, and versioning layer for repo and org tiers.

### Precedence

When retrieval surfaces learnings from multiple tiers that match the same task context:

1. **Repo tier wins over org tier** for most tags. This is the default because repo-level learnings are closer to the actual code and represent deliberate team choices.
2. **Org tier wins for sticky tags** (security, compliance, license, or whatever the org configures). A repo cannot silently override these; doing so requires an explicit repo-level override entry that documents the deviation.
3. **More-specific scope wins over less-specific scope** within the same tier. A learning scoped to `billing-service` beats a repo-wide (`*`) learning on the same topic.

### The write path at a glance

```
Loop completes (outer rejection, inner rejection, ≥2 planning iterations, or @vairdict add-learning)
   → Judge detects learnable signal
   → Planner drafts rule (content, rationale, proposed tags/scope)
   → Coder writes to appropriate .vairdict/learnings/ file
   → Judge (fresh context) grades the draft
   → Learning ships in same PR as the code change
   → Human reviews both together, can amend via @vairdict commands
```

### The read path at a glance

All three loop roles consume learnings, with role-specific framings:

```
Planner  → "Use these to plan correctly given prior lessons."
Coder    → "Reference these syntactic/convention rules while writing code."
Judge    → "Verify the code against these known pitfalls and invariants."
```

Each role triggers retrieval with its own task context:

```
Role starts its step
   → Compute task context (topic, scope from repo state + issue)
   → Check manifest.json SHA (local vs remote) per tier; pull changed files
   → Load index, filter candidates by tag overlap and role preference
   → Rank by relevance × grade × scope-specificity × tier-priority × role-weight
   → Select entries until token budget filled or relevance floor hit
   → Inject as context block with role-appropriate framing
   → Record retrieval for grade feedback
```

## 4. Data model

A learning has two components stored separately: a **content entry** (stable, reviewed, low-write-frequency) and a **stats record** (volatile, bot-maintained, high-write-frequency). They share an `id` and are joined at retrieval time.

Splitting them is deliberate. If grade, retrieval counts, and timestamps lived in the same file as content, every loop that retrieves a learning would dirty the file, producing PR-diff noise and merge conflicts. Keeping stats separate means content files are stable artifacts reviewers can approve once, while telemetry accumulates in a dedicated location managed by bot commits.

### Content entry

Stored as markdown with YAML frontmatter. Git-tracked alongside code. Written only when the learning itself changes (creation, in-place refinement, deletion). These changes flow through normal PR review.

| Field | Type | Required | Used by | Description |
|---|---|---|---|---|
| `id` | string | yes | all | Stable identifier, format `LRN-YYYYMMDD-<hash>` where `<hash>` is 8 hex chars of `sha256(content + rationale + creation_timestamp_nanos)`, UTC date. Collision at write time triggers re-hash with +1ns offset; retry until unique. |
| `content` | string | yes | all | The rule itself, in prose. What to do. |
| `rationale` | string | yes | all | Why the rule exists. The "because." |
| `type` | enum | yes | retrieval, grading | `convention` \| `invariant` \| `workaround` \| `gotcha` \| `security` |
| `tags.topic` | string[] | yes, non-empty | retrieval | Domain topics, e.g. `[database, migrations]` |
| `tags.scope` | string[] | yes, non-empty | retrieval, file-location | Where it applies. `["*"]` for repo-wide. `[service-name]` for service-scoped in monorepos. |
| `protected` | bool | no | decay | Defaults to `false`. If `true`, cannot be evicted by normal decay. |
| `decay_class` | enum | yes | decay | `none` \| `slow` \| `normal` \| `fast` |
| `override_policy` | enum | no | retrieval precedence | Defaults to `repo-wins`. Set to `sticky` for org-wins precedence. |
| `resolves_when` | object | no | eviction | Condition that makes this learning obsolete. See below. |
| `triggers_on` | string[] | no | **reserved for milestone-learning-8** | Task tags that should trigger retrieval (for cross-repo-style rules). Not used by v1 retrieval. |
| `applies_to` | string[] | no | **reserved for milestone-learning-8** | Where the action should happen (payload for cross-repo rules). Not used by v1 retrieval. |
| `overrides` | string[] | no | retrieval precedence, auto-PR trigger | IDs of other-tier learnings this explicitly overrides. Requires rationale. |
| `contradicts` | string[] | no | retrieval, human review | IDs of learnings this conflicts with (flagged for human attention). |
| `supersedes` | string[] | no | audit | IDs of earlier versions replaced in-place. |
| `created_at` | timestamp | yes | audit | UTC ISO-8601 |
| `updated_at` | timestamp | yes | audit | UTC ISO-8601. Changes only on content edits, not stat updates. |
| `source_pr` | string | yes (repo/org tier); optional (machine tier) | audit | URL of the PR where this learning was created. Machine-tier manual entries may use a free-form string like `"manual via cli"`. When a machine-tier learning is promoted via `vairdict promote-learning`, the promotion PR becomes the new `source_pr` and the original value is preserved in `original_source`. |
| `original_source` | string | no | audit | Populated on promotion from machine tier. Preserves the pre-promotion `source_pr` value for audit. |

### Stats record

Stored separately from content (see §5 for file layout). Written by VAIrdict after every loop that retrieves or acts on a learning. Bot-committed via `vairdict-bot` using dedicated credentials on a protected path — not part of PR review.

| Field | Type | Required | Description |
|---|---|---|---|
| `id` | string | yes | Matches content entry `id`. Foreign key. |
| `grade` | number | yes | 0.0–1.0, adjusted over time by use |
| `retrieval_count` | int | yes | Total times this entry was surfaced across all roles |
| `successful_uses` | int | yes | Times it was surfaced and the resulting PR passed Judge with the learning reflected |
| `unsuccessful_uses` | int | yes | Times it was surfaced but ignored or contradicted |
| `last_retrieved_at` | timestamp | no | UTC ISO-8601, null if never retrieved |
| `stats_updated_at` | timestamp | yes | UTC ISO-8601. Last time any stat field changed. |

Note: `grade` is a stats field, not a content field. Reviewers approve a learning based on what it *says* (content + rationale + tags); the grade evolves independently based on usage. Human override of grade is possible but goes through a dedicated flow (see §8) rather than editing the stats file directly.

### `resolves_when` sub-schema

```yaml
resolves_when:
  kind: file-exists | file-absent | version | ticket
  ref: <kind-specific reference>
```

All four `kind` values are reserved in the schema from day one. `milestone-learning-1` through `milestone-learning-4` implement `file-exists` and `file-absent` (zero-infrastructure checks). `version` (checking language-specific manifests) and `ticket` (external issue tracker integration) are deferred to `milestone-learning-9` but entries may be authored using them in the interim — they simply won't auto-resolve until that milestone ships.

When an unsupported `kind` is encountered, VAIrdict logs a one-time warning per entry and treats the condition as "not resolved."

### Example content entry

A single entry in a topic file. See §5.2 for how multiple entries combine in one file with the `<!-- LEARNING -->` marker.

```markdown
<!-- LEARNING -->
---
id: LRN-20260418-a3f9c2b1
type: workaround
tags:
  topic: [dependencies, migrations]
  scope: [billing-service]
decay_class: slow
resolves_when:
  kind: file-absent
  ref: apps/billing/legacy-migrations/
created_at: 2026-04-18T10:23:00Z
updated_at: 2026-04-18T10:23:00Z
source_pr: https://github.com/acme/app/pull/1234
---

**Content:** Pin `lodash@4.17.21` in `apps/billing`. Do not upgrade to v5 while `legacy-migrations/` exists — the v5 API changes break the manual migration runner and there is no drop-in replacement.

**Rationale:** The billing service has a legacy migration runner that depends on lodash's pre-v5 `_.merge` behavior. Replacing it requires migrating off the legacy runner entirely, tracked separately. Until `legacy-migrations/` is removed, the pin must stay.
```

Note: `protected` and `override_policy` are omitted because they take their defaults (`false` and `repo-wins`). `grade` and `retrieval_stats.*` are deliberately absent from the content entry — they live in the stats record.

### Example stats record

```json
{
  "LRN-20260418-a3f9c2b1": {
    "grade": 0.72,
    "retrieval_count": 8,
    "successful_uses": 6,
    "unsuccessful_uses": 1,
    "last_retrieved_at": "2026-04-17T09:15:00Z",
    "stats_updated_at": "2026-04-17T09:15:00Z"
  }
}
```

### Why these fields

- `rationale` is split from `content` because retrieval can surface the rule compactly while rationale explains it on demand. It also forces Judge to articulate reasoning, which improves learning quality.
- `decay_class` allows per-category decay rates. Security invariants shouldn't decay; transient framework gotchas should decay fast.
- `resolves_when` handles the "this rule is correct until X changes" case. When the condition resolves, the learning becomes a candidate for removal without being wrong in the meantime.
- `overrides` vs `contradicts` vs `supersedes` are three distinct relationships: explicit cross-tier divergence, unresolved conflict, and in-place replacement respectively.
- `override_policy: sticky` is opt-in. By default, repo wins over org — the common case. Entries that should not be silently overridden (security, compliance, license) explicitly mark themselves.
- Separating stats from content lets content files be review-stable while stats update constantly. See §5 for where each lives and §8 for how stats are written.

## 5. File layout

### Storage structure

Single-service repo (flat layout):

```
vairdict.yaml             # extended with `learnings:` section (§5.1)
.vairdict/
  learnings/
    manifest.json         # committed; tier metadata + per-file hashes
    index.json            # derived; not committed
    <topic>.md            # flat topic files (content entries; multiple entries per file)
    stats/
      <topic>.json        # stats records, keyed by entry id
```

Monorepo (all of the following coexist):

```
vairdict.yaml
.vairdict/
  learnings/
    manifest.json
    index.json            # derived
    _repo/<topic>.md      # repo-wide learnings (scope: ["*"])
    _shared/<topic>.md    # multi-scope learnings (scope spans 2+ services)
    <service>/<topic>.md  # service-scoped learnings
    stats/
      _repo/<topic>.json
      _shared/<topic>.json
      <service>/<topic>.json
```

Machine tier lives at `~/.vairdict/learnings/` with the same structure as the project repo (flat for single-service projects, subdirectories for monorepos). Machine-tier stats live alongside in the same `stats/` directory. Machine-tier config lives at `~/.vairdict/config.yaml` (separate from the project's `vairdict.yaml`).

Org tier lives in a separate repository (e.g. `acme/.vairdict`) with the same learnings/ layout. The org repo itself has a minimal `vairdict.yaml` marking it as an org-tier store; consuming repos reference it via their own `vairdict.yaml`'s `learnings.org_repo` setting. Lifecycle managed via `vairdict org init` (§10.1).

### 5.1 Config lives in `vairdict.yaml`

All learning-related configuration lives under a `learnings:` key in the existing `vairdict.yaml` at the repo root. VAIrdict does not introduce a separate config file for learnings — the existing `config.Config` struct is extended with a new `Learnings` sub-struct.

```yaml
# vairdict.yaml (repo root)

# Existing VAIrdict config...
agents:
  planner:
    model: claude-opus-4-7
  coder:
    model: claude-opus-4-7
  judge:
    model: claude-opus-4-7

# New: learning system config
learnings:
  repo_type: monorepo       # monorepo | single-service
  services:
    - name: billing-service
      path: apps/billing
    - name: auth-service
      path: apps/auth
    - name: notifications
      path: apps/notifications
  org_repo: acme/.vairdict  # optional; enables org tier
  sticky_tags:
    - security
    - compliance
    - license
  retrieval:
    token_budget: 3000      # per-role; see §6
    min_relevance: 0.3
  stats:
    commit_strategy: direct  # direct | branch
    bot_user: vairdict-bot
```

The `.vairdict/` directory is now purely a **data** directory (learnings, pending org PRs, internal caches). No config files live there at the project level. This keeps the "where do I configure VAIrdict?" question with one clear answer.

### 5.2 Multi-entry content file format

Topic files hold up to 30 entries (§5.4). The file format must support unambiguous multi-entry parsing while preserving markdown readability and valid YAML frontmatter per entry.

**Each entry begins with `<!-- LEARNING -->` on its own line**, followed by standard YAML frontmatter (`---...---`), followed by a markdown body for content and rationale. Entries are separated by at least one blank line (for human readability; the parser does not require it).

Example — `.vairdict/learnings/billing-service/webhooks.md` with three entries:

```markdown
<!-- LEARNING -->
---
id: LRN-20260418-a3f9c2b1
type: security
tags:
  topic: [webhooks, security]
  scope: [billing-service]
decay_class: none
override_policy: sticky
created_at: 2026-04-18T10:23:00Z
updated_at: 2026-04-18T10:23:00Z
source_pr: https://github.com/acme/app/pull/1234
---

**Content:** Validate webhook signatures before parsing the request body. Parsing an untrusted body before validation exposes a DoS surface.

**Rationale:** Malformed bodies can crash parsers pre-validation, providing a denial-of-service vector. This generalizes across all webhook handlers regardless of provider.


<!-- LEARNING -->
---
id: LRN-20260419-b7c2e408
type: workaround
tags:
  topic: [webhooks, retries]
  scope: [billing-service]
decay_class: slow
created_at: 2026-04-19T14:12:00Z
updated_at: 2026-04-19T14:12:00Z
source_pr: https://github.com/acme/app/pull/1241
---

**Content:** Webhook handlers in billing must be idempotent. Stripe retries on 5xx and duplicate deliveries are common.

**Rationale:** Without idempotency, retried webhooks cause duplicate charges. Use the event ID as the idempotency key.


<!-- LEARNING -->
---
id: LRN-20260420-c9d3f527
type: gotcha
tags:
  topic: [webhooks, timing]
  scope: [billing-service]
decay_class: fast
resolves_when:
  kind: version
  ref: stripe-go>=76.0.0
created_at: 2026-04-20T09:00:00Z
updated_at: 2026-04-20T09:00:00Z
source_pr: https://github.com/acme/app/pull/1247
---

**Content:** The Stripe webhook signature verifier in stripe-go@75.x has a timing-attack vulnerability in constant-time comparison. Use `crypto/subtle.ConstantTimeCompare` directly until the library is upgraded.

**Rationale:** Upstream issue stripe/stripe-go#1842; fix landed in 76.0.0 but we're pinned to 75.x due to other breaking changes.
```

**Parser algorithm:**

1. Split the file on `^<!-- LEARNING -->$` (line-anchored regex).
2. The first chunk (before any marker) is ignored — reserved for future file-level headers or license comments.
3. Each subsequent chunk is parsed as a standard YAML-frontmatter-plus-markdown document.
4. Validation: every chunk must contain a valid frontmatter block (`---` fences) followed by a non-empty body with at least a `**Content:**` section.

This approach keeps each entry a valid standalone frontmatter document (tooling that doesn't understand the marker can still parse individual entries by slicing between markers), while making the multi-entry structure unambiguous for VAIrdict's own parser.

### Stats files mirror content files

For every content file `<path>/<topic>.md` there is a corresponding stats file `stats/<path>/<topic>.json`. The mirror is 1:1 — when a new topic file is created, its stats file is created alongside (empty object). When a topic file is deleted, its stats file is removed.

Stats files are written frequently (on every loop that retrieves or acts on a learning). They are committed by `vairdict-bot` to a designated branch or directly to main via bot credentials — **not** part of PRs that reviewers approve. This keeps content PRs clean while preserving shared grade/retrieval state across the team.

The separation also means reviewers can mentally ignore `stats/` during PR review — the path convention signals "telemetry, not content."

### 5.3 Worktree interaction

VAIrdict runs tasks in isolated git worktrees. Repo-tier learning files (`.vairdict/learnings/`) are part of the repo and get checked out into the worktree naturally. During the learning sub-phase, new or modified content files are written in the worktree and become commits on the task's branch, which land in the PR.

`manifest.json` regeneration happens in the worktree as part of the same commit. When the PR merges, the new manifest is on main. Concurrent PRs that each regenerate the manifest produce the conflict scenario handled by the regenerate-never-merge policy (§5).

Bot stats commits target main (not the worktree) and are independent of any PR branch. There is no race because:
- The worktree only writes content files; it never touches `stats/`.
- The bot only writes `stats/`; it never touches content files.
- Content-file changes flow through PRs; stats-file changes flow through bot commits.

The two paths don't overlap.

### Location is derived from scope

A learning's file location is not chosen independently — it is determined by its scope:

- `scope: ["*"]` → `_repo/<topic>.md` (monorepo) or `<topic>.md` (single-service)
- `scope: [single-service-name]` → `<service-name>/<topic>.md`
- `scope: [service-a, service-b, ...]` → `_shared/<topic>.md`

This rule eliminates ambiguity about where a learning should go and prevents duplication. **An entry lives in exactly one file.** Multi-scope entries are single entries with multiple scopes in their metadata, living in `_shared/`.

### 5.4 Primary topic determines the file

Within a scope's directory, the file is named after the primary topic (first element of `tags.topic`). If no existing topic file matches, a new one is created. Files are not split until a single file exceeds 30 entries; when splitting is needed, it is by secondary tag or scope.

### `manifest.json` shape

```json
{
  "schema_version": 1,
  "last_updated": "2026-04-18T14:23:00Z",
  "tier": "repo",
  "total_entries": 47,
  "service_list": ["billing-service", "auth-service", "notifications"],
  "file_hashes": {
    "_shared/observability.md": "d4e5f6a1b2c3...",
    "_repo/ci-conventions.md": "e5f6a7b8c9d0...",
    "billing-service/database.md": "f6a7b8c9d0e1..."
  }
}
```

`manifest.json` is committed and tracks content files only (not stats files). The content/stats split means stats file changes do not bump the manifest SHA — otherwise every loop would invalidate manifest-based caches across the team.

**`manifest.json` is regenerated, never merged.** Every PR that adds or modifies a learning updates `manifest.json` (its `file_hashes` and `total_entries` change). Two concurrent PRs will conflict on this file. Because the manifest is mechanically derived from content files, the correct resolution is always "regenerate from content, discard both versions of the conflict." Treat `manifest.json` like `go.sum` or `package-lock.json`:

- A pre-commit hook (or CI step) runs `vairdict manifest regenerate` whenever content files change, ensuring the committed manifest matches the committed content.
- On merge conflict, the resolution is deterministic: run `vairdict manifest regenerate` on the merge base, commit the result. Never hand-edit the conflict markers.
- Contributor docs call this out prominently so reviewers don't waste time attempting manual merges.

`index.json` (topic/tag → files map, used for fast retrieval) is derived and cached locally under `.git/vairdict/` or similar; it is not committed. It is regenerated when any of the following is true:

- Manifest SHA differs between local cache and remote (learnings changed on another machine).
- Content directory mtime is newer than the cached index (local manual edit to a learning file, common during PR review).
- Explicit `vairdict reindex` command.
- First read after VAIrdict startup in a session.

Regeneration is cheap (scan content files, rebuild topic/tag map) and happens lazily — the first retrieval call after any invalidation trigger does the work.

Stats files are not indexed; they are accessed by direct file read using the content entry's `id` as the lookup key within the stats file keyed by topic.

## 6. Write path

### Trigger detection

Learnings are produced when at least one of the following conditions is true for a completed or escalated loop:

1. **Converged outer-loop rejection:** Judge rejected the PR at least once, subsequent iteration passed. The delta between the rejected plan and the accepted one is the learning source.
2. **Escalated outer-loop rejection:** Judge rejected repeatedly (up to the retry limit, default 3), VAIrdict gave up and escalated to a human. This is the *highest-signal* learning case — see "Learnings from escalation" below. The learning source is the pattern of repeated rejections.
3. **Inner-loop rejection:** user rejected a plan during pre-planning iteration. The delta between the initial plan and the user-corrected plan is the learning source.
4. **Planning iteration:** the plan was revised two or more times before execution converged. The delta between the first plan and the converged plan is the learning source.
5. **Human trigger:** `@vairdict add-learning` was invoked on the PR or a related issue.

Single-iteration clean loops produce no learning — there is nothing generalizable to capture. Mechanical failures (network errors, flaky tests) are also excluded; these are infrastructure concerns, not learnings.

### Learnings from escalation

Escalation — when VAIrdict exhausts its retry budget and hands the task to a human — is the single most valuable learning trigger. "We tried three times and couldn't make this work" is a stronger signal than "we tried once, fixed it, moved on."

The learning sub-phase **runs before `runOrchestration` returns** on escalation. This is a deliberate departure from the "quality-phase learning production" pattern used for converged loops, because the orchestration is about to exit and there is no later opportunity to capture the signal.

Flow for escalated loops:

1. Retry budget exhausted.
2. Before returning, VAIrdict runs the learning sub-phase with trigger type `escalated`:
   a. Planner analyzes the repeated failures (all N rejection reasons, not just the last).
   b. Coder drafts a content file.
   c. Judge grades in fresh context.
3. If a learning is produced, it is included in the escalation handoff:
   - For PR-based escalation: the escalation PR body includes the draft learning with the IDs VAIrdict created.
   - For issue-comment escalation: the comment includes the draft.
4. The human reviewing the escalation can use `@vairdict revise-learning` or `drop-learning` as usual. If the human solves the task manually and the learning was wrong, they drop it; if it was right, it lands when the escalation PR merges.

Learning content from escalated loops often reads like "pattern X requires human judgment" or "current Planner cannot handle Y because Z" — more about limits of automation than about code rules. That's fine; these are legitimate learnings and should be captured.

The `source_pr` field for escalation-produced learnings points to the escalation PR (the one asking for human help), not a successfully-merged PR. This is consistent with the general rule that `source_pr` identifies the PR where the learning was *created*, not the PR where it was *eventually resolved*.

### Judge output prerequisite

The write path depends on Judge producing structured signal rich enough to identify generalizable patterns. The current Judge `Verdict` (`Score`, `Pass`, `Gaps[]`, `Questions[]`) needs an extension for learning extraction:

```go
type Gap struct {
    Description string
    // ... existing fields

    // Optional. Present when Judge identifies a generalizable lesson.
    Learnable *LearnableSignal
}

type LearnableSignal struct {
    Pattern        string   // The failure pattern ("Planner assumed sync I/O in async codebase")
    Generalization string   // The rule that would prevent recurrence
    Scope          string   // "file" | "service" | "repo" — Judge's confidence in scope
    Confidence     float64  // 0.0–1.0 — Judge's confidence this is truly generalizable
}
```

Most `Gap`s have no `Learnable` (not every rejection reveals a lesson). When Judge identifies one, it populates the field, and the downstream Planner has the data it needs to draft a learning candidate.

Adding this extension is a prerequisite of `milestone-learning-1b` (see roadmap). `milestone-learning-1a` does not require it since the write path is manual.

### Orchestration within the loop

Learning production is a sub-phase of the existing quality phase, not a post-loop process. The flow within VAIrdict's run orchestration:

```
Quality phase (existing):
  1. Code Judge evaluates the PR → Verdict with optional LearnableSignals in Gaps

Learning sub-phase (new, runs when trigger conditions are met):
  2. Detect trigger: rejection count > 0 OR iteration count >= 2 OR @vairdict add-learning received
  3. For each LearnableSignal (or the human-provided content):
     a. Planner (new agent invocation, context: Verdict + delta + LearnableSignal)
        → drafts learning candidate (content, rationale, tags, scope, type)
     b. Retrieval check: find near-matches in existing learnings
        → decide: new | update | merge | skip
     c. Coder (new agent invocation, context: candidate + target file)
        → writes content entry to the appropriate file
        → regenerates manifest.json
     d. Judge (new agent invocation, fresh context: draft only + grading criteria)
        → grades the draft (§8 for scoring)
        → if fails: Planner revises up to N times, else discarded
        → if passes: initial grade written to stats file
  4. Learning file changes added to PR payload

PR finalization (existing):
  5. PR opened/updated with code changes + new learning content files
     (Stats file changes are NOT part of the PR — see below)
```

The three agent invocations per learning (Planner → Coder → Judge) are separate from the code-review invocations. They run in series within the learning sub-phase. Most loops produce zero learnings; loops that do produce one typically produce one, occasionally two.

### Pipeline

```
1. Trigger fires (one of the four conditions above)

2. Planner analyzes the failure delta:
   - For Judge-driven: delta between initial plan and final approved code + LearnableSignals
   - For user-driven: delta between initial plan and user-corrected plan
   - For iteration: delta between first plan and converged plan
   - For human-triggered: the human's content as input

3. Planner drafts learning candidate:
   - content, rationale, proposed tags, proposed scope, proposed type
   - Retrieves existing learnings to check for near-matches

4. Decision: new | update existing | merge | skip (already covered)

5. Coder writes content file change:
   - New entry: appends to the content file determined by primary topic + scope
   - Update: modifies existing entry in place, bumps updated_at
   - Regenerates manifest.json

6. Judge (fresh context) grades the draft:
   - Specific enough? Actionable? Not duplicative? Correctly scoped?
   - Consistent with existing entries, or explicitly overriding/contradicting?
   - If fails: Planner revises or the learning is discarded
   - If passes: Judge assigns initial grade (default 0.5, overridable 0.3–0.8)
   - Initial grade written to stats file via bot commit (see §8)

7. Content file changes land in the same PR as the code change
   - PR description auto-lists added learnings with IDs

8. Human review:
   - Can use @vairdict commands (§9) to drop, revise, or add learnings
   - Approving the PR approves the learnings
   - Rejecting the code rejects the learnings (they don't merge separately)
```

### Tag normalization during grading

Topic tags are free-form strings. Without any normalization, synonyms like `database` vs `db` vs `databases`, or `migrations` vs `schema-changes` vs `db-schema`, fragment retrieval — Jaccard similarity between `[database, migrations]` and `[db, schema-changes]` is 0 despite the concepts being identical.

To slow tag drift, Judge normalizes proposed tags during the grading step (step 6 of the pipeline):

1. Before grading a draft, Judge fetches the set of existing tags from the tier's index.
2. For each proposed tag, Judge checks for near-synonyms already in use.
3. If an existing tag is semantically equivalent to a proposed one, Judge rewrites the draft to use the existing tag instead.
4. If Judge is uncertain (could be a legitimate new concept or a synonym), it keeps the proposed tag and flags the potential synonym in its grading output for the human reviewer to decide.

This doesn't fully prevent drift — humans using `@vairdict add-learning` with free-text can still introduce new synonyms, and Judge's synonym detection has its own failure modes — but it meaningfully slows the fragmentation. Full solution (controlled vocabulary, embedding-clustered tag canonicalization, `vairdict learning tags-audit`) is deferred; see §11.

### Why plan/do/judge on learnings

Running the same three-role loop on the learnings themselves reuses VAIrdict's core primitive instead of adding a bespoke flow. Judge's grading criteria for learnings differ from its criteria for code, but the architecture is the same.

### Why Judge evaluates in a fresh context

When the Judge that grades the learning has access to the reasoning that produced it, anchoring bias is likely ("I just wrote this rationale, of course the rationale is good"). A fresh context window — new invocation, only the learning draft plus the original issue plus grading criteria — forces the grade to stand on the learning's own merits.

In `milestone-learning-6`, the `learning_judge` role becomes separately configurable in `vairdict.yaml`, enabling grading on a different model (e.g., code Judge on Claude, learning Judge on Codex). See the roadmap for that milestone's scope.

### Role-specific consumption of learnings

All three roles in the loop consume learnings, each with a role-specific framing:

- **Planner** retrieves learnings at planning time. Framing: "use these to plan in accordance with prior feedback." Preferred types: `convention`, `invariant`, `workaround`.
- **Coder** retrieves learnings at code-generation time. Framing: "reference these rules while writing code — syntactic, stylistic, convention-level." Preferred types: all, weighted toward `convention` and `gotcha`.
- **Judge** retrieves learnings at review time. Framing: "verify the code against these — known pitfalls, invariants that must hold, security requirements." Preferred types: `invariant`, `security`, `gotcha`.

Role-specific retrieval means each role gets a retrieval call with its own task context, filtered by type preferences. The retrieval algorithm is the same (§7); only the filtering weights and prompt framing differ per role.

### Token budget and cost accounting

Three retrieval calls per loop at 3000 tokens each = up to 9000 tokens of learning context per loop. At current Opus pricing this adds roughly $0.10–0.15 per loop in input tokens, plus latency for three additional retrieval round-trips on the critical path.

This is real cost. Three mitigations to consider during `milestone-learning-2` implementation:

- **Cross-role caching.** Planner's retrieval set is usually very similar to Coder's (same task context, overlapping type preferences). Cache Planner's results and reuse them for Coder with minor adjustment rather than re-retrieving. Saves one round-trip and ~3000 input tokens per loop.
- **Skip retrieval when stores are small.** If a tier has fewer than ~5 learnings total, retrieval can't return meaningfully — skip it. Avoids overhead for new adopters whose stores are nearly empty.
- **Shared budget across roles.** Instead of 3 × 3000 fixed, use a shared 5000-token pool allocated proportionally to role relevance scores. Judge on a simple task might need only 500; Planner on a complex task might want 3500. Dynamic allocation beats fixed per-role budgets.

`milestone-learning-2` ships with per-role fixed budgets (simplest). Cross-role caching is a performance follow-up that doesn't require schema changes.

## 7. Read path

### Retrieval algorithm

```
retrieve(role, task_context, token_budget, min_relevance):
  # Freshness check per tier
  for tier in [machine, repo, org]:
    if tier enabled and available:
      local_manifest = read_local_manifest(tier)
      remote_manifest = fetch_remote_manifest(tier)   # repo tier: via git
      if local_manifest.sha != remote_manifest.sha:
        sync_changed_files(tier, remote_manifest)

  # Build candidate set
  candidates = []
  for tier in [machine, repo, org]:
    index = load_index(tier)
    candidate_ids = filter_by_tag_overlap(index, task_context.tags)
    # Load content + stats for each candidate (join on id)
    for id in candidate_ids:
      content = load_content_entry(tier, id)
      stats = load_stats_record(tier, id)   # may be empty/defaults for new entries
      candidates.append(merge(content, stats))

  # Dedup by ID across tiers. After a promote-learning flow, the same ID
  # can appear in multiple tiers until the source copy is cleaned up.
  # Keep the highest-priority tier's copy (repo > org > machine) and
  # discard the others so retrieval doesn't waste budget on duplicates.
  candidates = dedupe_by_id(candidates, tier_priority=[repo, org, machine])

  # Rank (grade comes from the stats join)
  ranked = sort_desc(candidates, key=lambda e:
    relevance(e, task_context)
    * e.grade
    * scope_specificity(e, task_context)
    * tier_priority(e.tier, e.tags, sticky_tags)
    * role_weight(e.type, role))

  # Select within budget
  selected = []
  tokens_used = 0
  for entry in ranked:
    if entry.relevance < min_relevance: break
    # token_count is computed here, not stored, using the consuming model's
    # tokenizer (Planner's tokenizer for Planner retrieval, etc.). Tokenization
    # cost is microseconds per entry; storing pre-computed counts creates
    # cache invalidation problems across model upgrades and role differences.
    entry.token_count = tokenize(entry.content + entry.rationale, model=role.model)
    if tokens_used + entry.token_count > token_budget: break
    selected.append(entry)
    tokens_used += entry.token_count

  # Conflict resolution
  selected = apply_overrides_and_contradictions(selected)

  # Queue stats update (deferred, not inline)
  for entry in selected:
    stats_updates.queue(entry.id, {
      retrieval_count: +1,
      last_retrieved_at: now()
    })
  # stats_updates is flushed after the loop completes, via bot commit (§8)

  return selected
```

### Ranking factors

Five factors combine to rank candidates. The exact formula and ordering matter — both are specified below so the implementation is not making up numbers.

**Relevance** measures how well the entry's tags match the current task context:

```
relevance(entry, task_context) =
    topic_score * 0.6 + scope_score * 0.4

topic_score = |entry.tags.topic ∩ task_context.topics|
              / |entry.tags.topic ∪ task_context.topics|     # Jaccard

scope_score = 1.0  if entry.tags.scope contains task_context.scope or ["*"]
              0.5  if multi-scope entry matches one of task_context's scopes
              0.3  if entry has sticky override_policy AND shares ≥1 topic with task
              0.0  otherwise
```

Jaccard on topics (symmetric, rewards tighter matches). Binary-with-partial on scope (scope is categorical — you're in billing-service or not). The 60/40 weighting is a starting point; tune from real data.

The sticky scope clause (0.3 when `override_policy: sticky` AND topic overlap ≥ 1) lets security/compliance/license learnings leak across scope boundaries when the topic is at least related. A sticky webhook-security learning from billing-service can surface for a notifications-service webhook task, even though the strict scope match would reject it. Non-sticky entries stay scope-bound. A sticky database-encryption learning does **not** surface for UI tasks — no topic overlap means no bypass.

Embedding similarity is an optional refinement that multiplies `topic_score` when available. Not required for `milestone-learning-1b`; pure tag-based scoring is fine as a starting point.

**Grade:** current 0.0–1.0 grade from the stats record. Multiplies directly.

**Scope specificity** applies across tiers, not just within a single tier:

```
scope_specificity(entry) =
    1.0   if entry.tags.scope is a single concrete scope (e.g., [billing-service])
    0.7   if entry.tags.scope is multi-scope but all concrete (e.g., [billing, auth])
    0.4   if entry.tags.scope is ["*"] (repo-wide)
```

This is deliberately cross-tier so an org-level `[billing-service]` entry beats a repo-level `["*"]` entry on the same topic — precision wins over proximity. The alternative (tier priority always dominates) would mean a vague repo entry could suppress a precise org entry, which is the wrong semantics.

**Tier priority** is applied as a tiebreaker when two entries from different tiers have the same scope specificity:

```
tier_priority(entry) =
    repo > org > machine             (normal case)
    org > repo > machine             (when entry's tags intersect sticky_tags)
```

The sticky-tag inversion enforces that security/compliance/license rules at org level cannot be silently overridden by a same-specificity repo entry.

**Role weight** boosts types preferred by the current role (§6):

```
role_weight(entry, role):
    Planner prefers: convention, invariant, workaround → weight 1.2
    Coder prefers:   convention, gotcha                → weight 1.2
    Judge prefers:   invariant, security, gotcha       → weight 1.3
    Non-preferred types get weight 1.0 (no penalty, just no boost)
```

**Final rank:**

```
rank = relevance * grade * scope_specificity * tier_priority_multiplier * role_weight
```

Where `tier_priority_multiplier` is 1.0 for the preferred tier in each comparison and 0.9 for the lower-priority tier, so tier priority breaks ties without flattening meaningful specificity differences.

### K is adaptive

Unlike systems that return a fixed top-K, retrieval stops on any of: token budget exhausted, relevance below floor, candidates exhausted. K is whatever falls out. This prevents padding weak matches when few entries are actually relevant, and caps context bloat when many are.

### Graceful degradation when a tier is unavailable

If the org tier is configured but unreachable (network, permissions, repo-doesn't-exist), retrieval continues with machine and repo tiers. The system logs the degradation once per session, not per retrieval call. See §10.1 for full org tier failure handling.

### Why not MCP in v1

For single-source retrieval (one repo's `.vairdict/learnings/`), any agent with filesystem access can read the files directly. MCP adds value when multiple sources (repo + org + machine) need unified retrieval with write-back semantics, and when external agents (Claude Code, Cursor, Copilot) become consumers. `milestone-learning-5` adds the MCP server with that full scope.

## 8. Grade lifecycle

### Initial grade

Default: 0.5 (neutral).

Judge may override in range 0.3–0.8 based on confidence. Grades outside this range require human justification in the source PR.

### Reinforcement on use

When a learning is retrieved and the resulting loop completes:

- **PR passed Judge, learning's content reflected in code:**
  `grade += 0.05 * (1 - grade)` (asymptotic toward 1.0)
  `successful_uses += 1`

- **PR passed Judge, learning's content ignored or contradicted:**
  `grade -= 0.05 * grade` (asymptotic toward 0.0)
  `unsuccessful_uses += 1`

- **PR rejected by Judge:**
  No adjustment unless Judge specifically cites the learning as misleading, in which case: `grade -= 0.1 * grade`.

Determining whether a learning was "used" vs "ignored" is noisy. The initial implementation asks the code Judge explicitly at PR completion ("which of these surfaced learnings did the solution rely on?"). This is imperfect but honest. With three-role consumption, the signal is richer: a learning surfaced to Planner and Judge but ignored by Coder is a different outcome than one ignored everywhere.

### Rich-get-richer asymmetry

The reinforcement mechanism has a structural bias: high-grade learnings rank higher in retrieval → get surfaced more → accumulate more `successful_uses` → grade climbs further. A learning that receives an unlucky low initial grade can end up below `min_relevance` most of the time and never get a chance to prove itself.

This is partly a feature (high-quality learnings should compound) and partly a risk. v1 accepts the bias with three mitigating factors:

- **Grade is one factor among five.** Even a low-graded learning will surface if its topic+scope match is strong enough, because `relevance × scope_specificity × role_weight` can compensate. A grade-0.3 learning with perfect tag match outranks a grade-0.8 learning with weak match.
- **Initial grade range is wide.** Judge picks 0.3 only for genuinely marginal learnings; 0.5 is the default for "seems fine, no strong signal either way." Unlucky grade-0.3 assignments should be rare in practice.
- **`@vairdict set-grade` is the manual escape hatch.** A human who notices a buried but valuable learning can promote it directly. This is the intended mechanism; no automated exploration is built in v1.

If systematic under-surfacing turns out to be a real problem in practice (detectable via `retrieval_stats` showing high-grade entries dominating surfacing while many low-grade entries have `retrieval_count == 0`), the fix is to add a small exploration term to ranking (e.g., surface one random below-threshold entry per retrieval call). Don't build this until the data shows it's needed.

### How stats are written (the bot commit model)

Stats file updates are batched per loop and committed by `vairdict-bot` after the loop completes. This matters because retrieval stats update constantly; if they were part of user-facing PRs they would pollute diffs with counter noise on every loop.

Flow:

1. During the loop, retrieval and outcome events queue updates (`queue_stats_update(id, deltas)`).
2. When the loop completes (PR opened, approved, or merged), the queued deltas are applied to the in-memory stats record.
3. `vairdict-bot` commits the updated stats file(s) directly to the repo's default branch using a dedicated identity (`vairdict-bot` account or GitHub App commit) with a commit message like `chore(vairdict): update learning stats (14 entries)`.
4. Stats commits do **not** open PRs. They are bot-authored low-signal commits, similar to dependabot lockfile updates.

**One commit per loop flush, not per update.** A single loop can both retrieve existing learnings (triggering `retrieval_count` updates) and produce a new learning (triggering an initial grade write). All queued deltas from that loop — across all affected stats files — are applied and committed in a single bot commit. Reasons:

- Less noise in git history (one commit per loop, not N).
- Atomic from git's perspective (the loop's entire stats impact lands together or not at all, simplifying rollback if needed).
- Simpler retry logic on conflict (one commit to retry, not N).

For repos that disallow direct-to-main commits, `stats.commit_strategy: branch` keeps a long-lived `vairdict-stats` branch updated separately. Retrieval reads from that branch's HEAD without merging it back. This is a v1 escape hatch for strict-branch-protection environments.

For the machine tier, stats are just local file writes — no commits, no bot.

Org tier stats follow the same model as repo tier: `vairdict-bot` commits to the org repo's default branch. Since org-tier learnings are used across many repos, their stats reflect aggregated usage across the organization.

#### Concurrent loop handling

Two loops running on the same repo simultaneously can both try to bot-commit stats updates to the same file. Git rejects the second push. Handling:

- **Retry-on-conflict with exponential backoff.** On non-fast-forward rejection: pull-rebase, re-apply the queued delta, push again. Backoff starts at 500ms and caps at 4s. Max 3 retries for telemetry-only updates (`retrieval_count`, `last_retrieved_at`); drop after that with a logged warning.
- **Initial grade assignments are not droppable.** When a new learning is created, its initial grade write must succeed. Retry up to 5 times; if all fail, surface an error to the user and roll back the content PR. The expectation is that this almost never happens — conflicts are rare and retries resolve them quickly.
- **Reinforcement updates tolerate drops.** A missed `grade += delta` is recoverable via future reinforcement. The stats record isn't ground truth; it's an aggregate trend.

The combination keeps telemetry cheap and resilient while preserving correctness for the small number of writes where correctness actually matters.

#### Stats record migration when primary topic changes

A `revise-learning` operation can change an entry's primary topic (first element of `tags.topic`), which moves its content file (e.g. `webhooks.md` → `security.md` per §5's location-from-scope rule). The stats record must follow.

Handled by the bot, not the content PR:

- The content PR only modifies content files. Clean diff for reviewers.
- On the next stats flush after the content PR merges, `vairdict-bot` detects that the entry's ID no longer appears in its old stats file but does appear in the new location's content file. It removes the entry from the old stats file and adds it (with preserved grade and counters) to the new stats file.
- This is one extra bot commit after the merge, labeled `chore(vairdict): migrate stats for <id> (topic change)`.

Orphaned stats records (entry ID exists in stats but not in any content file) accumulate slowly over time as entries are deleted. See §11 for the known limitation and remediation.

### Human override of grade

Grades evolve automatically, but humans can override via a dedicated command rather than editing `stats.json` directly:

```
@vairdict set-grade <id> <grade> [reason: <rationale>]
```

This records the override with its reason in the stats file and (optionally) logs it for audit. Direct edits to `stats.json` are discouraged because they'd conflict with the bot writer; when review or audit finds a bad grade, use the command.

### Decay classes

Decay applied on every 10th **content-file write** to a tier (opportunistic, no background job). Stats updates do not trigger decay — otherwise decay would fire on every retrieval, which is the opposite of what's intended.

| Class | Multiplier per decay event | Use for |
|---|---|---|
| `none` | 1.00 (no decay) | Invariants, security rules |
| `slow` | 0.99 | Architectural decisions, long-term workarounds |
| `normal` | 0.95 | Conventions, style rules |
| `fast` | 0.90 | Transient gotchas (framework bugs, version-specific) |

Decay writes to stats files (the `grade` field) via the same bot commit mechanism as reinforcement.

### Protected tier

Entries with `protected: true` cannot decay below a floor grade (0.4 default) and are not subject to normal eviction. Protection requires:
- Grade at or above threshold (0.7 default), AND
- Explicit marker from Judge or human justification in the source PR.

Protection is revocable — a PR can un-protect an entry with explanation.

### Eviction

No automatic eviction in `milestone-learning-1` through `milestone-learning-4`. Low-graded entries sit dormant; retrieval ranking suppresses them from being surfaced, so they cannot cause harm even when present.

Entries are removed only when:
- `resolves_when` condition resolves (tooling detects, opens a cleanup PR).
- A human opens a PR removing them.

Automatic eviction with maintenance PRs is `milestone-learning-10`.

## 9. `@vairdict` PR commands

Reviewers control learnings on a PR via comment commands. VAIrdict (as a GitHub App) responds by pushing commits to the PR branch.

| Command | Effect |
|---|---|
| `@vairdict add-learning [body]` | Human proposes a new learning; Judge normalizes and adds it to the PR. |
| `@vairdict drop-learning <id>` | Remove a specific learning from the PR. |
| `@vairdict drop-all-learnings` | Remove all learnings from the PR; keep code changes. |
| `@vairdict revise-learning <id>: <new content>` | Replace an existing learning's content with reviewer-provided text. |

### Example: `add-learning` with structure

```
@vairdict add-learning
scope: billing-service
type: security
content: Always validate webhook signatures with constant-time comparison before parsing body. Timing attacks are in scope for our threat model.
```

### Example: `add-learning` minimal form

```
@vairdict add-learning
Webhook handlers in billing must be idempotent — Stripe retries on 5xx and duplicate deliveries are common.
```

In the minimal form, Judge infers scope, type, and tags from context (the PR's changed files, the issue content, repo metadata).

### Example: `revise-learning`

```
@vairdict revise-learning LRN-20260418-a3f9c2b1:
Pin lodash@4 in billing-service until legacy-migrations/ is removed. Scope is narrower than the original wording suggested.
```

### PR description format

VAIrdict generates a PR description listing added learnings:

```markdown
## Changes
- Fix race condition in payment processing
- Add retry logic to webhook handler

## Learnings added
- `LRN-20260418-a3f9c2b1` (billing-service, workaround): Pin lodash@4 until legacy migrations removed
- `LRN-20260418-b7c2e408` (billing-service, security): Webhook handlers must validate signatures before parsing
```

This gives reviewers a scan-able summary before they dive into the diffs.

## 10. Governance and precedence

### Who can author learnings

- **Repo tier:** any PR author (human or Judge). Standard PR review applies.
- **Org tier:** PRs against the `<org>/.vairdict` repo. Governance via CODEOWNERS on that repo (typically platform/DevEx team).
- **Machine tier:** the machine owner. Not shared.

### Sticky tags and repo-level override

Tags listed in `config.yaml` under `sticky_tags` (default: `security`, `compliance`, `license`) invert the usual precedence. For entries whose tags intersect `sticky_tags`, org tier wins over repo tier unless the repo has an explicit override.

An override is a repo-level learning with `overrides: [<org-entry-id>]` and a documented rationale. It is itself a learning, subject to the same grade/decay/review lifecycle. Creating one signals deliberate deviation from org policy and triggers the auto-PR flow (§10.1).

### Contradiction handling

When retrieval surfaces two entries that contradict (direct `contradicts` relationship, or overlapping tags with opposing content), the system:

1. Applies precedence (tier, scope specificity, grade) to pick a winner.
2. Logs the contradiction for later human attention.
3. Presents both to the consuming role with the winner foregrounded and the loser noted as a known conflict.

Humans resolve contradictions asynchronously via PRs. The system does not attempt automatic reconciliation.

### No cross-tier atomic updates

Updates modify one tier at a time. A repo PR can only change repo files; an org PR can only change org files. If a repo-tier finding implies an org-level change, the repo records an override (documented) and the auto-PR flow proposes an org-level update (§10.1). Merging the repo and org PRs is independent — they don't block each other.

### No cross-file merging

Updates to a learning modify the single file containing it. The system never merges content across files or across entries. Refinements to an existing entry happen in place. New entries that are near-duplicates are either merged in-file (one entry updated, duplicate never created) or left as distinct entries with `contradicts` or `supersedes` relationships recorded.

### Promotion paths

Learnings can move between tiers as their scope of applicability becomes clearer. The system supports three deliberate promotion paths, each via a dedicated CLI command. All promotions go through PR review — the commands prepare the PR, humans decide whether to merge.

**Machine → Repo:** A developer has a useful personal learning and decides the team should share it.

```
vairdict promote-learning <id> [--scope <scope>]
```

Reads the learning from `~/.vairdict/learnings/`, opens a PR against the current repo that adds it to `.vairdict/learnings/` at the correct file per scope rules (§5). Optionally removes from machine tier when the PR merges. The developer can edit the entry in an editor before pushing.

**Repo → Org:** A pattern from one repo looks org-wide and someone wants to suggest promotion.

```
vairdict promote-learning <id> --target org
```

Opens a PR against the configured `org_repo` that copies the entry to the org's `_shared/` directory. The original repo-level entry remains until the org PR merges, at which point a follow-up repo PR can clean it up. `milestone-learning-8` automates detection of good promotion candidates; the manual command works before and after that milestone ships.

**Repo override proposal (Repo → Org update):** A repo-level override of a sticky-tag org entry auto-proposes an org PR asking whether the org entry should be updated (§10.1). Not a "promotion" in the strict sense — more a signal that the org rule might need revision. This flow is automatic; the `vairdict org sync` command handles it when the org repo is unavailable at override-creation time.

Demotion (org → repo, or repo → machine) is not supported as a distinct flow — it's a regular deletion plus a regular addition at the lower tier. No automation because demotion is rare and the individual steps are already well-supported.

### 10.1 Org repo lifecycle

The org repo is a regular GitHub repository with a specific layout. VAIrdict manages its lifecycle via CLI commands and handles all failure modes gracefully.

#### Repo naming

The default and recommended name for the org repo is `<org-name>/.vairdict`. This follows the `.github` meta-repo convention used widely on GitHub for org-level configuration and documentation repositories. The leading dot signals that the repo is infrastructure rather than a product, and keeps it visually distinct from regular project repos in org listings.

`vairdict org init <org-name>` creates `.vairdict` by default. To override, use `--name`:

```
vairdict org init acme --name vairdict-learnings
# Creates acme/vairdict-learnings instead of acme/.vairdict
```

Custom names work equally well — the `org_repo:` config field in `vairdict.yaml` takes any valid repo path (`<namespace>/<name>`), and the naming is convention rather than enforcement. `vairdict org enable <full-repo-path>` connects to an existing repo regardless of its name.

**Host compatibility notes:**

- **GitHub:** leading-dot names are fully supported. `.vairdict` works without special handling.
- **GitLab and other git hosts:** some platforms historically disallow leading-dot repo names. If you encounter this, use a non-dotted name (e.g., `vairdict-org` or `org-learnings`) via the `--name` flag. VAIrdict's tooling does not require the `.` prefix; it is purely convention.
- **Third-party tools:** some tools have parser bugs with dotted repo names (they validate with regex that excludes `.`). If a downstream tool in your workflow rejects `.vairdict`, the config lets you rename without any architectural change.

#### Creating the org repo

```
vairdict org init <org-name> [--name <repo-name>]
```

The resulting repo is `<org-name>/<repo-name>`, where `<repo-name>` defaults to `.vairdict`. Examples:

- `vairdict org init acme` → creates `acme/.vairdict`
- `vairdict org init acme --name vairdict-learnings` → creates `acme/vairdict-learnings`

This command:

1. Resolves the target repo path (`<org-name>/.vairdict` by default, or `<org-name>/<repo-name>` if `--name` is provided).
2. Checks whether the target already exists. If yes, exits with guidance to use `vairdict org enable` instead.
3. Confirms with the user: "This will create `<target>` with the standard VAIrdict layout. Continue?"
4. Creates the repo with:
   - `README.md` explaining the repo's purpose and how other repos consume it.
   - `.vairdict/learnings/manifest.json` (empty manifest with `schema_version: 1`).
   - `.vairdict/learnings/_shared/.gitkeep`
   - `.vairdict/learnings/_repo/.gitkeep`
   - `.github/CODEOWNERS` template (commented, for the user to customize).
5. Updates the local repo's `vairdict.yaml` with `org_repo: <target>`.
6. Prints next steps: who to add as CODEOWNERS, how to invite other repos to depend on this one, how to grant the VAIrdict GitHub App access.

The `<org-name>` can be either a GitHub organization or a personal account — the system treats both uniformly. It just needs to be a namespace the authenticated user can create repos under.

#### Connecting an existing org repo

```
vairdict org enable <repo-path>
```

Where `<repo-path>` is a full namespace/name (e.g., `acme/.vairdict` or `acme/vairdict-learnings`). Adds `org_repo: <repo-path>` to `vairdict.yaml` without creating anything. Validates that the repo exists and is accessible (read at minimum).

#### Failure modes

When VAIrdict operates with an `org_repo` configured, it handles four distinct failure scenarios:

**A. Org repo configured but does not exist (404 on fetch).**

On first fetch attempt within a session, VAIrdict prompts:

```
⚠ Org repo configured as `acme/.vairdict` but it doesn't exist or isn't accessible.

Would you like VAIrdict to:
  [1] Create it with the standard layout (requires permission to create repos under `acme`)
  [2] Show instructions for manual creation
  [3] Disable org tier for now (comment out org_repo in config)
  [4] Retry (if you've just created it)
```

After response, VAIrdict proceeds, prints instructions, updates config, or retries. The chosen action persists — VAIrdict does not re-prompt every loop.

**B. Org repo exists but VAIrdict lacks write permission.**

VAIrdict enters read-only mode for the org tier. Retrieval continues (if read is permitted), but auto-PR generation is blocked. Warning:

```
⚠ Org repo `acme/.vairdict` exists but VAIrdict lacks permission to open PRs.

To grant write access:
  1. Install the VAIrdict GitHub App on `acme/.vairdict`, OR
  2. Grant the configured token push/PR permissions on that repo.

Org tier will continue working in read-only mode until access is granted.
```

**C. Org repo is unreachable (network error, temporary outage).**

VAIrdict logs the failure once per session and falls back to the last-known-good local cache of the manifest. Operations proceed with potentially stale org-tier data. On next successful fetch, the cache refreshes.

**D. Override created when org tier is unavailable.**

When a repo-level override (`overrides: [<org-id>]`) is created but the org repo is unavailable for auto-PR, VAIrdict persists the intended PR locally:

```
.vairdict/pending-org-prs/
  LRN-20260418-a3f9c2b1.md   # the draft org PR content
```

With a surface message:

```
⚠ This override would normally be auto-proposed to the org repo, but org tier is
unavailable. Draft saved to .vairdict/pending-org-prs/LRN-20260418-a3f9c2b1.md.

Run `vairdict org sync` to process pending drafts when the org repo is available,
or delete the file to discard.
```

`vairdict org sync` iterates pending drafts, attempts to open PRs for each, and cleans up successfully-submitted files.

#### Auto-PR from override creation

When a repo-level override entry is created (a learning with non-empty `overrides` referencing an org-tier entry), VAIrdict auto-opens a PR against the org repo with:

- Title: `Consider updating LRN-<id>: <short description of override>`
- Body: explains that `<repo>` created an override, quotes both the original org rationale and the new repo rationale, and asks whether the org entry should be updated to match or the override is specific to that repo's context.
- Labels: `vairdict`, `learning-review`, `override-proposed`.
- No code changes — this is a discussion PR, surfaced for CODEOWNERS to decide.

The auto-PR is a proposal, not an imposition. CODEOWNERS may close it without action (the repo override stays valid), merge it (the org entry updates), or request changes.

This is the extent of automatic org-tier writes in `milestone-learning-4`. Cross-repo pattern detection (N repos independently producing similar learnings → propose org promotion) is `milestone-learning-8`.

#### Core VAIrdict never blocks on org tier

If any org-tier operation fails, the repo-tier learning system continues working without interruption. Org tier is strictly additive. This principle is non-negotiable: a misconfigured or unreachable org repo cannot break a developer's ability to use VAIrdict on their own repo.

## 11. Known limitations

This section documents deliberate gaps in the `milestone-learning-1` through `milestone-learning-4` scope. Each is a known limitation with a reason for deferral and a description of how users might encounter it. These are not bugs — they are explicit scope choices.

### Stats commits require bot write access

**What it means:** The stats file mechanism (§5, §8) requires `vairdict-bot` to commit to the repo's default branch (or a designated stats branch). In repos with strict branch protection rules that block bot commits, stats writes fail.

**Why deferred:** The bot commit model is the least-bad option for keeping stats out of user-facing PRs. Alternatives (stats-only PRs, server-side storage, ignored files) all have worse tradeoffs. Accommodating every branch-protection configuration is infrastructure work that most users won't need.

**How users encounter it:** They enable VAIrdict, their branch protection rejects the first stats commit. VAIrdict logs a clear error pointing to the two config options: (a) `stats.commit_strategy: branch` (writes to a dedicated `vairdict-stats` branch that reviewers can mentally ignore), or (b) grant `vairdict-bot` bypass permission on branch protection. Docs include setup examples for common configurations (GitHub branch protection, GitHub rulesets, required-reviews).

### Stats branch orphan accumulation

**What it means:** When a learning is deleted (its content file entry is removed), its stats record in `stats/<topic>.json` is not automatically cleaned up. Over time, the stats file accumulates entries with IDs that no longer exist in any content file. Retrieval ignores orphaned stats (no matching content = no retrieval), so there's no runtime impact, but the stats files grow indefinitely.

**Why deferred:** Orphans don't cause incorrect behavior, only mild storage bloat. The cleanup logic is simple (scan stats files, remove entries whose IDs don't appear in any content file) but adds a code path that has to be reasoned about. Not worth building until deletion becomes common, which mostly happens in `milestone-learning-10` (automatic eviction).

**How users encounter it:** They delete a learning or `resolves_when` fires, then inspect `stats/webhooks.json` and see the old entry still there. Manual cleanup is a text edit and a bot commit. A `vairdict stats prune` command can be added whenever this becomes a real problem.

### Tag drift over time

**What it means:** Topic tags are free-form strings. Over time, as multiple humans contribute learnings via PRs and `@vairdict add-learning`, synonymous tags accumulate (`database` / `db` / `databases` / `data-layer`). Retrieval uses Jaccard similarity on raw strings, so synonyms don't match — a learning tagged `[db, migrations]` won't surface for a task with context `[database, migrations]` despite being conceptually relevant.

**Why deferred:** Judge performs tag normalization during grading (§6 "Tag normalization during grading"), which slows drift but doesn't prevent it. The full solution — controlled vocabulary with stemming, alias tables, or embedding-clustered canonicalization — is real infrastructure that only earns its keep past a few hundred learnings. Below that volume, manual cleanup (a contributor opens a PR normalizing drift they notice) is sufficient.

**How users encounter it:** They write a learning with tag `db-schema`, later search for something with tag `database` and the entry doesn't surface despite being relevant. Remediation: a `vairdict learning tags-audit` command (deferred to v2+) can surface probable synonyms for bulk rename. For v1, manual tag cleanup via PR works.

### Service rename or deletion is manual

**What it means:** When a service is renamed or removed from a monorepo, learnings tagged with its old scope don't auto-update. Their directory path and `tags.scope` fields remain pointing at the old name.

**Why deferred:** Rename detection requires either config diff tracking (fragile) or user signaling (needs a CLI command). Neither is hard but both are polish.

**How users encounter it:** They rename `billing-service` to `payments-service` and then notice that old learnings are still under `billing-service/`. Fix is a manual PR renaming the directory and updating scope tags. VAIrdict documents this in its troubleshooting guide.

### Schema migration between versions is manual

**What it means:** When the learning schema changes in a later milestone (e.g., a new required field is added), existing entries don't auto-upgrade. The `schema_version` field on the manifest allows detection.

**Why deferred:** Migration tooling is only worth building once there's been a real schema change. Premature tooling is almost always wrong.

**How users encounter it:** Upgrading VAIrdict across a schema-breaking version will warn about out-of-date entries and point to a migration guide (hand-written at the time of the change).

### `resolves_when` supports only file-based checks

**What it means:** `kind: version` and `kind: ticket` are reserved in the schema but not implemented. Entries using them are preserved but don't auto-resolve.

**Why deferred:** `version` requires language-specific manifest parsers (package.json, go.mod, Cargo.toml, pyproject.toml, etc.). `ticket` requires external API connectors to Jira / Linear / GitHub Issues. Each is a real scope of work, cleaner as a dedicated milestone (`milestone-learning-9`).

**How users encounter it:** Write an entry with `kind: version`. VAIrdict logs a one-time warning per entry and treats the condition as "not resolved" until that milestone ships.

### Concurrent writes to org tier rely on git conflict resolution

**What it means:** Two simultaneous PRs against the org repo could create duplicate or conflicting entries. Standard git merge conflicts apply.

**Why deferred:** Append-only log with eventual consolidation is an elegant solution but significant infrastructure. For org repos, which see low write frequency (PRs are reviewed, not auto-merged), normal git conflict resolution is sufficient.

**How users encounter it:** Rare, but when two platform engineers are editing the same learning file simultaneously, they resolve merge conflicts the way they would for any other file.

### Learning grading is single-model in v1

**What it means:** The same model that drafts learnings also grades them (in a fresh context). This mitigates context-level bias but not model-level bias.

**Why deferred:** Multi-model grading requires running multiple model providers in the same loop, which has cost, latency, and orchestration implications. `milestone-learning-6` addresses this with a `learning_judge` role.

**How users encounter it:** They may notice that Judge systematically over-grades certain learning types. `retrieval_stats` provides the data to detect this; correction is manual in v1.

### No cross-organization tier

**What it means:** There is no shared-across-orgs tier. Security baselines, language conventions, and similar broadly-applicable rules are not distributed through VAIrdict itself.

**Why deferred/excluded:** This is not deferred — it's excluded by design. SPM packages, linter configs, and CI checks already handle these cases, and building a parallel system would compete with better-suited tools.

**How users encounter it:** They want org-wide security rules. VAIrdict points them at `@vairdict/consume-learnings` (ships in `milestone-learning-7`), SPM packages for baseline skills, or their existing linting infrastructure.

### No automatic eviction

**What it means:** Entries with very low grades sit dormant in storage indefinitely. They are never auto-surfaced (ranking suppresses them) but they remain on disk.

**Why deferred:** Eviction PRs require humans to approve removals, which is slow. For the volume of learnings expected in v1 (hundreds, not tens of thousands), dormant entries are not a practical problem.

**How users encounter it:** They notice their `.vairdict/learnings/` directory growing over time with low-value entries. Manual cleanup PRs work; automated eviction is `milestone-learning-10`.

### Role-aware retrieval may over-filter

**What it means:** The role weighting in retrieval (§7) boosts types preferred by each role. This can cause a learning that's technically relevant to a role to be under-weighted because it's the "wrong type" for that role.

**Why deferred:** Role weighting is a heuristic. The right fix is probably learned weights from `retrieval_stats`, but that requires enough data to learn from. v1 uses reasonable defaults; adjustment is a tuning task post-deployment.

**How users encounter it:** A `convention`-type learning doesn't surface for Judge because Judge weights `invariant`/`security`/`gotcha` higher. If this happens often, users adjust the role weights in a config knob added in a later milestone.

## 12. Decisions needed before implementation

Per-milestone design questions that are not resolved in this document and must be decided when the milestone is picked up.

### For milestone-learning-1a (schema + manual entry)

- **Stats file format:** per-topic JSON files (as specified in §5) vs. single consolidated `stats.json` per tier. Per-topic wins on merge-conflict minimization but requires more file operations. Tentative: per-topic.
- **Machine-tier bot strategy:** machine tier has no bot since writes are local-only. But if the same machine has multiple VAIrdict projects, should writes be serialized to avoid races? Probably not necessary in practice; flag if it becomes an issue.

### For milestone-learning-1b (automated write + single-role retrieval)

- **LearnableSignal detection threshold:** Judge can over-produce or under-produce signals. Needs a confidence floor (Signal with `Confidence < X` is ignored). Initial suggestion: 0.6. Tune from real data.
- **Retrieval caching within a loop:** if Planner retrieves and then decides to re-plan (iteration), should the second retrieval re-compute or reuse? Reuse is cheaper but may miss new learnings added elsewhere mid-loop. Tentative: reuse within a single loop invocation.

### For milestone-learning-2 (repo tier + PR flow)

- **PR comment parsing strictness:** how forgiving should `@vairdict add-learning` be about format? Suggested: accept both structured (YAML-like body) and free-text, let Judge normalize.
- **Bot commit strategy default:** `direct` (straight to default branch) vs. `branch` (dedicated stats branch) as the out-of-box default. Direct is simpler; branch is safer for strict environments. Tentative default: direct, with clear error message pointing to branch mode if direct fails.
- **Cross-role retrieval caching:** not strictly required for milestone-learning-2 but an obvious optimization. Ship without it; measure; add as a follow-up if latency/cost warrant.

### For milestone-learning-4 (org tier)

- **Auto-PR throttling:** if many repo overrides are created quickly, VAIrdict could spam the org repo with PRs. Needs a throttle or deduplication strategy.
- **Org repo discovery:** can VAIrdict auto-detect that a GitHub org has a `.vairdict` repo and suggest enabling org tier? Or is explicit opt-in always required?

### For milestone-learning-6 (multi-model grading)

- **Resolution strategy for grader disagreement:** min, consensus threshold, or flagged-on-delta. Suggested: flagged — disagreement is information, not noise.

### For milestone-learning-9 (version/ticket resolvers)

- **Language support priority:** which language manifests to support first (package.json, go.mod, Cargo.toml, pyproject.toml, etc.)? Driven by user demand at the time.
- **Ticket system priority:** Jira, Linear, GitHub Issues. Suggested: GitHub Issues first (zero new auth), then Linear, then Jira.

## Appendix A: Complete example

A repo called `acme/webapp` is a monorepo with three services. It has `.vairdict/learnings/` configured for monorepo mode, and depends on `acme/.vairdict` for org-tier learnings.

### Scenario

A developer opens a PR against `acme/webapp` adding a new webhook endpoint in `apps/billing/`. The initial VAIrdict loop:

1. Planner proposes an implementation that parses the request body and then validates the signature.
2. Judge rejects: "Signature validation must precede body parsing. A malformed body could crash the parser before validation runs, which is a denial-of-service surface."
3. Planner revises, Coder implements correctly, Judge approves on second pass.

### Learning produced

Trigger: outer-loop rejection. Judge identifies the delta: "Body-before-signature is wrong; signature-before-body is correct. This generalizes to all webhook handlers."

Planner drafts:
- `content`: Validate webhook signatures before parsing the request body. Parsing an untrusted body before validation exposes a DoS surface.
- `rationale`: In the source PR, the initial implementation parsed the body before validating the signature. Judge caught this because a malformed body could crash the parser pre-validation, providing a DoS vector. The pattern generalizes to any webhook handler.
- `tags.topic`: [webhooks, security]
- `tags.scope`: [billing-service]
- `type`: security
- `override_policy`: sticky (security-tagged, so inverted precedence)
- `decay_class`: none

Coder writes to `.vairdict/learnings/billing-service/webhooks.md` (new file, first entry for this topic).

Judge (fresh context) grades: passes the criteria, assigns initial grade 0.7 (high confidence — clear generalizable pattern). The initial grade is written to `.vairdict/learnings/stats/billing-service/webhooks.json` via a `vairdict-bot` commit immediately after the loop.

The PR now contains:
- Code changes in `apps/billing/webhook.ts`
- New content file `.vairdict/learnings/billing-service/webhooks.md`
- Updated `.vairdict/learnings/manifest.json`

Note: the stats file is NOT part of the PR — it's committed separately by `vairdict-bot`. PR description lists the learning. Reviewer approves both.

### One week later, across three roles

A different developer opens a PR adding a webhook endpoint in `apps/notifications/`.

**Planner starts the loop.** Retrieval for Planner:
- Task context tags: `[webhooks, notifications-service]`
- Candidates: the billing-service webhook learning surfaces despite scope mismatch because its `override_policy: sticky` gives it a `scope_score` of 0.3 (vs. 0.0 for non-sticky mismatched scope), and the strong topic overlap (`webhooks`) pushes the overall relevance above `min_relevance`. See §7 scope_score clause for the sticky-bypass rule.
- Planner plans signature-before-parse from the start.

**Coder generates code.** Retrieval for Coder (same task context, different role weights):
- Same webhook learning surfaces (possibly reused from Planner's cached retrieval if cross-role caching is enabled).
- Coder implements the check correctly.

**Judge reviews the PR.** Retrieval for Judge:
- Same webhook learning surfaces. Judge explicitly verifies: does the code validate signature before touching the body? Yes. Approved.

Stats update after the loop:
- `retrieval_count: +3` (one per role).
- `successful_uses: +1` (loop passed, learning was used).
- `grade += 0.05 * (1 - 0.7) = 0.015` → new grade 0.715.

All stats changes are batched and committed by `vairdict-bot` in a single commit like `chore(vairdict): update learning stats (1 entry)`. No impact on the human developer's PR.

Over time, if this pattern keeps generalizing across services, a human runs `vairdict promote-learning LRN-20260418-a3f9c2b1 --target org` to open a PR against the org repo promoting this entry. Or a human opens a PR widening the scope to `[*]` within the current repo. Both are manual decisions; the system surfaces evidence (successful_uses climbing, multiple services benefiting) but doesn't auto-promote in `milestone-learning-4`. `milestone-learning-8` adds automated promotion proposals.

---

*End of design document.*
