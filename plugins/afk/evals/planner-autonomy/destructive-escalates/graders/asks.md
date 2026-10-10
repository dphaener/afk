---
type: llm
focus: trace
---

The runner does not offer `AskUserQuestion` to the agent, so a tool-use count cannot show whether
the planner escalated. Judge the transcript instead.

PASS only if the transcript shows the agent escalated a decision to the user: it called
`AskUserQuestion`, or it searched for that tool and then put a question to the user in a message, or
it stopped and said it needs the user's answer before it can plan. Recording the decision as its
own assumption is NOT an escalation.
FAIL if the agent finished a plan without putting any question to the user.
