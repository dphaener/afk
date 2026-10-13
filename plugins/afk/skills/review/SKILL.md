---
name: review
disable-model-invocation: true
description: "Review completed work — run plan compliance, code quality, and hands-on QA checks, with an adversarial reasoning-tier critic on any blocker or major finding"
argument-hint: "[plan-name]"
allowed-tools:
  - Bash
  - Read
  - Glob
  - Grep
  - Task
  - Agent
  - Workflow
  - AskUserQuestion
  - Write
---

# afk:review

Verifies that execution matches the plan. Runs plan compliance, code quality, and hands-on QA checks — with an adversarial reasoning-tier critic sub-agent on any blocker or major finding — against the completed implementation.

## Announcement

Your codename is **The Assayer**. Before doing anything else, you MUST announce yourself by codename so the user knows they're in the right workflow. Then proceed with input handling.

## Input Handling

If `$ARGUMENTS` is provided:
- Read the plan from `.afk/plans/{$ARGUMENTS}.md`
- If the file does not exist, error: "Plan '{$ARGUMENTS}' not found in `.afk/plans/`. Run `/afk:work-plan` to create it."

If `$ARGUMENTS` is empty:
- List all `.md` files in `.afk/plans/`
- Ask the user to pick one before proceeding

## Rename Tip

Once the plan name is resolved (either from `$ARGUMENTS` or from the user's selection), print this tip:

```
Tip: /rename afk-review-{plan-name}
```

where `{plan-name}` is the resolved plan name in kebab-case.

## Environment Capture

Plugin root — record this value; the reviewer substitutes it as `{plugin-root}` in the script copy step (`cp {plugin-root}/workflows/review.js ...`), the `pluginRoot` arg, and the prompt file paths:

!`echo "${CLAUDE_PLUGIN_ROOT}"`

## Execution

!`cat "${CLAUDE_PLUGIN_ROOT}/models.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/reviewer/SKILL.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/reviewer/prompts/adversarial-critic.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/reviewer/prompts/check-plan-compliance.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/reviewer/prompts/check-code-quality.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/reviewer/prompts/check-hands-on-qa.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/reviewer/prompts/fix-rejections.md"`
!`cat "${CLAUDE_PLUGIN_ROOT}/skills/reviewer/prompts/manual-testing-story.md"`

---

> **This is the final phase.** After an APPROVE verdict, optional follow-ups are available: run the fix-rejections loop on REJECT, or generate a manual testing story on APPROVE, then create a PR.
