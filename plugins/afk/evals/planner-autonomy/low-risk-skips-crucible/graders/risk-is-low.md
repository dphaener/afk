---
type: llm
focus: trace
---

PASS only if the transcript shows the planner rated this plan's risk LOW (the draft's `## Risk`
section, the handoff summary's Risk line, or an equivalent statement), and it did not run the
Crucible.
FAIL if it rated the risk MEDIUM or HIGH, never rated it, or ran the Crucible.
