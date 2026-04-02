# How agents work in this repo

VAIrdict is built by the same three-layer system it implements.
Every issue goes through: Planner → Coder → Judge.

## The three roles

### Planner (@vairdict-planner)
- Reads the issue
- Writes a detailed implementation plan
- Posts plan as first comment on the issue
- Identifies dependencies and risks
- Flags ambiguities before coding starts

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

## Issue requirements

Every issue must have before work starts:
- Clear intent (one sentence)
- Acceptance criteria (binary pass/fail each)
- Out of scope section
- Dependencies listed

If any of these are missing — Planner flags it before planning.

## Severity levels

- P0: blocks this issue entirely
- P1: required, must fix this loop
- P2: ambiguous, Coder documents assumption and proceeds
- P3: nice to have, logged for later issue

## Loop limit

3 loops per issue maximum.
On loop 3 failure → human escalation via GitHub mention.