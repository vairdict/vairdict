# How agents work in this repo

VAIrdict is built by the same three-layer system it implements.
Every issue goes through: Planner → Coder → Judge.

---

## The three roles

### Planner (@vairdict-planner)
- Reads the issue
- Writes a detailed implementation plan
- Posts plan as first comment on the issue
- Identifies dependencies and risks
- Flags ambiguities before coding starts
- From M6+: also writes issues for the next milestone

### Coder (@vairdict-coder)
- Reads the plan from Planner's comment
- Implements exactly what the plan specifies
- Opens a PR linked to the issue
- Does not exceed scope of the plan
- Documents assumptions made

### Judge (@vairdict-judge)
- Reads: original issue + plan + PR diff
- Scores against acceptance criteria
- Posts structured verdict as PR review
- Pass → approves PR
- Fail → requests changes with specific gaps
- After 3 loops → escalates to @almog (human)
- From M6+: also judges new issues written by Planner

---

## Issue requirements

Every issue must have before work starts:
- Clear intent (one sentence)
- Acceptance criteria (binary pass/fail each)
- Out of scope section
- Dependencies listed

If any of these are missing — Planner flags it before planning.

---

## Severity levels

- P0: blocks this issue entirely — cannot proceed
- P1: required, must fix this loop
- P2: ambiguous, Coder documents assumption and proceeds
- P3: nice to have, logged for later issue

---

## Loop limit

3 loops per issue maximum.
On loop 3 failure → human escalation via GitHub mention.

---

## Dogfooding — how the process changes by milestone

### M0 — M1: fully manual
VAIrdict does not exist yet.
Agents work issues directly using Claude Code.
Human (@almog) acts as judge on all PRs.
Judge agent posts a structured review comment but human makes final call.

### M2: partial dogfood
Plan + code phases of VAIrdict exist.
For each issue:
```
vairdict run "<issue intent>"
```
VAIrdict handles plan and code phases.
Human judges quality phase manually.
Judge agent still posts review comment.

### M3: first full loop
Complete three-phase loop exists.
All M3 issues go through VAIrdict:
```
vairdict run "<issue intent>"
```
VAIrdict opens the PR automatically.
Human only sees escalations and the final PR.
Judge agent verdict is the VAIrdict verdict.

### M4 and beyond: full dogfood
Every issue is planned, coded, and judged by VAIrdict itself.
```
vairdict run "<issue intent>"
```
No PR is merged without a passing VAIrdict verdict.
Human only intervenes on escalations.
The three bot accounts (@vairdict-planner, @vairdict-coder,
@vairdict-judge) are driven by VAIrdict's own orchestration layer.

---

## Writing new milestone issues (M6+)

When it's time to open the next milestone (see PROGRESS.md):

**Planner writes each issue:**
- Reads ROADMAP.md for the milestone definition
- Expands each bullet into a full issue
- Includes: intent, context, acceptance criteria, technical notes,
  dependencies, out of scope
- Posts as draft issues for judge review

**Judge reviews each issue:**
Scores each issue on:
- Is the intent clear and unambiguous?
- Are acceptance criteria binary pass/fail?
- Is scope contained — not too broad, not too narrow?
- Are dependencies correctly identified?
- Could a coder agent implement this with zero clarification?

Pass (all yes) → issue opened in GitHub, added to PROGRESS.md
Fail → back to Planner with specific gaps

---

## PR format

Every PR must include:
```
## Issue
Closes #N

## What was built
One paragraph summary.

## Assumptions made
List any P2 items assumed during implementation.

## VAIrdict verdict (M3+)
Score: X%
Loops: N
```