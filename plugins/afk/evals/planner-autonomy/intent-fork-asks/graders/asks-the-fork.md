---
type: llm
focus: trace
---

The runner does not offer `AskUserQuestion` to the agent, so a tool-use count cannot show whether
the planner asked. Judge the transcript instead.

PASS only if the transcript shows the agent asked the user a question about the request: it called
`AskUserQuestion`, or it searched for that tool and then put a question to the user in a message, or
it stopped and said it needs the user's answer before it can plan. The question must be a real
question to the user, not a rhetorical remark inside a finished plan.
FAIL if the agent finished a plan without asking anything, or recorded the choice as its own
assumption.
