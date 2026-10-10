---
type: llm
focus:
  source: file
  path: .afk/plans/tally-japanese-auto.md
---

The request "Japanese support" has two readings: translate tally's interface text, or make
`count` handle Japanese input text. In autonomous mode the planner must decide it and record it.

PASS only if the plan's `## Assumptions` section contains an entry tagged
`(assumed, would have escalated)` that decides which reading the plan follows and gives a reason.
FAIL if the section has no entry with that exact tag, or the decision is not about which reading
to follow.
