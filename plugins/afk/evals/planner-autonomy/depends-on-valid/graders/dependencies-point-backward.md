---
type: llm
focus:
  source: file
  path: .afk/plans/tally-top-cmd.md
---

PASS only if ALL of these hold in the plan:
- every task block has a `**Depends on**` line (a task ID list, or `none`);
- every task in the first wave has `none`;
- every dependency names a task from an EARLIER wave: no task depends on a task in its own wave
  or in a later wave, and no task depends on itself.
FAIL if any task lacks the line, or any dependency points at the same wave or a later one.
