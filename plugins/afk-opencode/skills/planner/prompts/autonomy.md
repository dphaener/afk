# Planner Autonomy

This block defines the planner's two modes. The invoking skill (`/afk-work-plan`, `/afk-go`, or a caller naming the skill) states which one applies; with no statement, default mode applies.

## Default mode (decide and escalate)

You decide every decision node yourself with your recommended answer, record it with the `(assumed)` tag, and escalate only a node that meets the escalation rule, stated once in the `# Grill Loop` block, section 3. Facts are looked up, never asked. Most runs ask nothing.

- **`question` tool absent from your tool list**: an escalated decision still reaches the user. Put that one decision, with its options and your recommendation first, in your message and stop there; never decide it as `(assumed, would have escalated)`, which exists only in autonomous mode.
- **Skip phrases** ("just write the plan", and the planner skill's list): the user has declined to be asked, so escalation ends. Decide every remaining open node as `(assumed)`, including nodes that meet the escalation rule, self-rate the risk, and generate the plan. No risk question, no resumed loop.

## Autonomous mode

On when the invoking skill passes `--autonomous`, or `.afk/{name}/autonomous` exists. **Autonomous mode never calls the `question` tool**, anywhere in this skill or the blocks it loads:

- Nodes that meet the escalation rule are decided too, with the reversible, least-exposure option, and tagged `(assumed, would have escalated)` (grill loop, section 3). The tag reaches the plan's `## Assumptions` unchanged.
- Inputs that cannot be decided stop with an error message instead of a question:
  - no argument: `planner: no request given; autonomous mode needs a description or ticket argument`
  - Jira unreachable: `planner: cannot fetch {ticket} from Jira (twg and acli both failed); rerun with the ticket text as the argument`
- The Crucible rule is the same in both modes: it runs only on a HIGH planner rating, and it never asks anything.
