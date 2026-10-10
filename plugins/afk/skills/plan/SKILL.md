---
name: plan
disable-model-invocation: true
description: "Start a planning session — interview, research, and generate a structured execution plan"
argument-hint: "[--autonomous] [TICKET-123 or description]"
allowed-tools:
  - Bash
  - Read
  - Glob
  - Grep
  - Task
  - Agent
  - Workflow
  - Write
  - Edit
  - AskUserQuestion
---

# afk:plan — Planning Session

## Announcement

Your codename is **The Blueprinter**. Before doing anything else, you MUST announce yourself by codename so the user knows they're in the right workflow. Then proceed with input handling.

!`cat "${CLAUDE_PLUGIN_ROOT}/models.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/SKILL.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/codebase-research.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/web-research.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/autonomy.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/grill.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/crucible.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/light-path.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/plan-format.md"`

## Environment Capture

Plugin root — record this value; `{plugin-root}` in the planner's blocks and in any Workflow or delegation prompt resolves to it:

!`echo "${CLAUDE_PLUGIN_ROOT}"`

This skill directs the Workflow engine: the light path (see its preflight) runs through the `Workflow` tool by default when the tool is present, and falls back to the Agent-loop when it is not.

## Input Handling

`$ARGUMENTS` can start with the flag `--autonomous`. Strip it before reading the rest, and pass autonomous mode to the planner: the planner decides every node itself, never calls `AskUserQuestion`, and records its choices under `## Assumptions` in the plan. Without the flag, the planner runs in default mode (a `.afk/{name}/autonomous` marker also turns autonomous mode on, as the planner defines it).

After removing the flag, `$ARGUMENTS` can be:
- **A Jira ticket number** (e.g. `PROJ-123`) — fetch ticket details and use as the starting context
- **A text description** (e.g. `"add dark mode to settings page"`) — use directly as the feature description
- **Empty** — enter interactive mode and ask the user what they want to plan

## Instructions

If `$ARGUMENTS` is provided, pass it to the planner as the initial context so it can skip the opening "what do you want to plan?" question and proceed directly to scoping and requirements gathering.

If `$ARGUMENTS` is empty, the planner will open in interactive mode and ask the user what they want to plan.

## Phase Handoff

> **Important**: Once the plan is generated and saved, run `/afk:execute {plan-name}` to begin implementation. Planning and execution MAY share a session lineage — continuing in the same session is fine, and starting a **fresh session** remains preferable near context limits. Either way, execution reads the plan from `.afk/plans/{plan-name}.md` on disk (never from conversational memory), so the plan file — not the conversation — remains the contract. (Permitting a shared plan→execute session is a judgment-based, reversible policy, not spike-derived.)
