---
type: llm
focus:
  source: file
  path: .afk/plans/tally-top.md
---

PASS only if the plan has an `## Assumptions` section with at least one entry, and every entry
names the decision, the chosen answer, and a reason for the choice.
FAIL if the section is missing or empty, or any entry has no reason.
