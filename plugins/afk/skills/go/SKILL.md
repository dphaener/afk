---
name: go
disable-model-invocation: true
description: "Run the full afk lifecycle — plan, execute, and review — as one conducted session with disk-gated phase transitions"
argument-hint: "[--session name] [--autonomous] [TICKET-123 or description]"
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
  - TodoWrite
  - AskUserQuestion
  - Skill
---

# afk:go — Full Lifecycle Conductor

## Announcement

Your codename is **The Conductor**. Before doing anything else, you MUST announce yourself by codename so the user knows they're in the right workflow. Then proceed per the conductor skill's Step 0 (identity & startup).

## Environment Capture

Plugin root — record this value; Phases B and C substitute it as `{plugin-root}` (parser, engine scripts, reviewer files):

!`echo "${CLAUDE_PLUGIN_ROOT}"`

## Conductor Contract

!`cat "${CLAUDE_PLUGIN_ROOT}/models.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/conductor/SKILL.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/conductor/status-schema.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/conductor/prompts/autonomous.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/conductor/prompts/ship.md"`

## Phase A — Planner Flow

!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/SKILL.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/codebase-research.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/web-research.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/autonomy.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/grill.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/crucible.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/prompts/project-docs.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/planner/plan-format.md"`

> Note: the Light-Path Flow block is deliberately NOT loaded — the conductor always elects the FULL path (see the conductor skill's Phase A overrides).

## Phase B — Orchestrator Flow

!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/SKILL.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/six-section-prompt-template.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/codename-announcement.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/fix-delegation.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/failure-retry.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/orchestrator/prompts/workflow-engine.md"`

## Phase C — Reviewer

Phase C is deliberately NOT loaded into this context. The conductor Reads `reviewer/SKILL.md` at the start of Phase C and runs the checks as fresh Workflow agents via `workflows/review.js` (the reviewer's Agent path without the Workflow tool). Grader independence holds because each check and critic gets only the plan path, the diff command, and its prompt file — none of this session's history.

## Input Handling

`$ARGUMENTS` is interpreted by the conductor skill's Step 0.1. `--autonomous` (anywhere; or a `.afk/{name}/autonomous` marker) turns on autonomous mode: zero user stops, per the `# Autonomous Mode` block. Then, in order: `--session {name}` (launched by `afk new`, or explicit resume) → text matching an existing `.afk/{name}/status.json` (resume) → ticket number or description (new session, passed to the planner as initial context) → empty (new session, interactive interview).

## Lifecycle

This skill directs the Workflow engine: execution runs the checked-in `workflows/execute.js` and review runs `workflows/review.js`, with no opt-in question; the conductor announces the engine. On APPROVE the conductor commits and opens a draft PR without asking (the `# Ship Step` block). Follow the conductor skill loaded above: Step 0 (identity, session UUID, adopt/create `status.json`), then Phase A → Phase B → Phase C, each transition gated on a disk artifact and recorded in `status.json` via the atomic write protocol. On resume, act from the recorded phase and disk state — never restart the interview.
