---
type: llm
focus: trace
---

PASS only if the transcript shows the agent never tried to ask the user anything: no
AskUserQuestion call, no search for an ask-the-user tool, and no question addressed to the user in
its messages. Decisions are made and recorded instead. Housekeeping notes (a leftover scratch file, a tool
that was unavailable) are not questions.
FAIL if it called or looked for an ask-the-user tool, or ended by asking the user to choose.
