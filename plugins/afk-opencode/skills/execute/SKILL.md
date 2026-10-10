---
name: execute
description: "Execute a plan — orchestrate task delegation, verification, and completion tracking"
argument-hint: "[--autonomous] [plan-name]"
---

# afk Execute

Reads a plan file and orchestrates its full execution: task delegation, verification, and completion tracking.

## Announcement

Your codename is **The Anvil**. Before doing anything else, you MUST announce yourself by codename so the user knows they're in the right workflow. Then proceed with input handling.

## Environment Capture

Plugin root — record this value; `{plugin-root}` in the engine spec and in every delegation prompt resolves to it:

!`echo "${CLAUDE_PLUGIN_ROOT}"`

This skill directs the Workflow engine: executing through the `afk_workflow` tool is the default, and the engine spec below defines the Agent-loop fallback for sessions without the tool.

## Input Handling

`$ARGUMENTS` can start with the flag `--autonomous`. Strip it before reading the plan name. In autonomous mode, pass `--autonomous` to the plan parser (`plan_to_args.rb`) and never relay a `NEEDS_INPUT` return to the user; the engine spec says how the run handles it.

**If a plan name remains:**
- Read the plan at `.afk/plans/{name}.md`
- If the file does not exist, error: "Plan not found at `.afk/plans/{name}.md`. Available plans: [list files in `.afk/plans/`]"

**If no plan name remains:**
- List all `.md` files in `.afk/plans/`
- If exactly one plan exists, use it
- Otherwise, in default mode ask the user to pick one before proceeding; in autonomous mode stop with the error: "execute: no plan name given and .afk/plans/ does not hold exactly one plan; pass a plan name"

## Session Title

Once the plan name is resolved (either from `$ARGUMENTS` or from the user's selection), rename this session with `afk_session({title: "afk-execute-{plan-name}"})`, where `{plan-name}` is the resolved plan name in kebab-case.

## Execution

!`cat "${CLAUDE_PLUGIN_ROOT}/models.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/SKILL.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/six-section-prompt-template.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/codename-announcement.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/fix-delegation.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/failure-retry.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/workflow-engine.md"`

## Phase Handoff

Execution reads the plan from `.afk/plans/{plan-name}.md` on disk and executes from that file, never from conversational memory, so the plan file remains the contract. Execution MAY continue in the same session as planning or start a fresh one.

When execution completes, run `/afk-review {plan-name}` to review the work. The same session is fine.
