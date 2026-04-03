# VAIrdict Progress

## Current Milestone: M0 — Infrastructure

### Ready to start
- #9 infrastructure setup

### In progress
- none

### Blocked
- #2 config (waiting on #9)
- #3 state (waiting on #2)
- #1 bootstrap (waiting on #2)
- #4 agents/claude (waiting on #2)
- #5 judges/plan (waiting on #4)
- #6 phases/plan (waiting on #3, #4, #5)
- #7 cmd (waiting on #1, #2, #3, #6)
- #8 dogfood (waiting on #7)

### Done
- none

## Dependency Graph
#9 → #2 → #3 → #6 → #7 → #8
          #4 → #5 ↗
     #1 ↗