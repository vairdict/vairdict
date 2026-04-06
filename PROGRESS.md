# VAIrdict Progress

Agents read this file before picking up any issue.
Update this file when opening, completing, or blocking an issue.

---

## Current Milestone: M3 — Quality Phase

---

## Ready to Start
- #32 judges/quality: e2e + intent check vs original task
- #34 escalation: loop limit + human notification

## In Progress
- none

## Blocked
- #33 phases/quality: quality phase orchestration (blocked on #32)
- #35 cmd: wire quality phase + escalation into vairdict run (blocked on #33, #34)
- #36 dogfood: first full three-phase task on vairdict (blocked on #35)
- github/verdict: post structured judge verdict as PR comment (blocked on #22 github PR — M2 done)
- cmd/auto-vairdict: auto-merge on passing verdict (blocked on M3 complete)

## Done
- #9 chore: repo infrastructure setup
- #2 config: vairdict.yaml parsing
- #1 bootstrap: init flow
- #4 agents/claude: Anthropic API client
- #3 state: task state machine
- #5 judges/plan: plan judge
- #6 phases/plan: plan phase
- #7 cmd: cobra CLI
- #8 dogfood: vairdict init on repo
- #19 agents/claudecode: Claude Code CLI runner
- #20 judges/code: code judge via spm ship
- #22 github: PR creation + comments
- #21 phases/code: code phase orchestration
- #23 dogfood: plan + code phases e2e
- #29 auth: ~/.config/vairdict/ API key configuration

---

## Dependency Graph

```
#9 (infrastructure)
 └── #2 (config)
      ├── #3 (state)
      │    └── #6 (phases/plan) ←──────────────┐
      ├── #4 (agents/claude)                    │
      │    └── #5 (judges/plan) ───────────────→┤
      └── #1 (bootstrap) ──────────────────────→#7 (cmd)
                                                 └── #8 (dogfood)
```

---

## Dogfooding Status

VAIrdict is built using itself as it becomes capable.
The process evolves across milestones:

| Milestone | Dogfood Level | How                                              |
|-----------|---------------|--------------------------------------------------|
| M0        | none          | VAIrdict doesn't exist — human judges all PRs    |
| M1        | none          | same — human judges all PRs                      |
| M2        | partial       | plan + code phases working — use where possible  |
| M3        | full          | complete loop — all M3 issues go through VAIrdict|
| M4+       | full          | every issue planned, coded, judged by VAIrdict   |

**From M3 onwards:**
- `vairdict run "<issue intent>"` is the only way to open a PR
- No PR is merged without a passing VAIrdict verdict
- Human only sees escalations and the final PR

---

## When to Open Next Milestone Issues

Issues for future milestones are opened by the agent planner,
reviewed by the agent judge, only then created in GitHub.

| Open     | When                          |
|----------|-------------------------------|
| M2 issues | 6/8 M1 issues closed         |
| M3 issues | 4/5 M2 issues closed         |
| M4 issues | 4/5 M3 issues closed         |
| M5 issues | 4/5 M4 issues closed         |
| M6+       | same pattern, one ahead      |

**Process for opening new milestone issues:**
1. Planner agent reads ROADMAP.md for the next milestone
2. Planner writes detailed issues (intent, criteria, notes, deps)
3. Judge agent reviews each issue — is it actionable? unambiguous?
4. Judge approved → open in GitHub and add to this file
5. Judge rejected → planner rewrites, loop again

---

## How Agents Use This File

**Before starting work:**
1. Read CLAUDE.md — understand the repo and current dogfood level
2. Read this file — find the first issue in "Ready to Start"
3. Read that issue in full on GitHub
4. Move it from "Ready to Start" to "In Progress" here
5. Implement the issue
6. Open a PR linked to the issue
7. Move to "Done" when PR is merged
8. Move any newly unblocked issues to "Ready to Start"

**When completing an issue:**
- Check the dependency graph — what does this unblock?
- Move unblocked issues from "Blocked" to "Ready to Start"
- Update milestone completion table below
- If milestone is complete — update "Current Milestone"

**Never:**
- Start an issue still in "Blocked"
- Skip updating this file after completing work
- Open a PR without a passing verdict (M3+)
- Open M2+ issues without planner + judge process

---

## Milestone Completion

| Milestone | Status      | Issues Done |
|-----------|-------------|-------------|
| M0        | done        | 1/1         |
| M1        | done        | 9/9         |
| M2        | done        | 6/6         |
| M3        | in progress | 0/6         |
| M4        | not started | 0/6         |
| M5+       | not started | —           |
