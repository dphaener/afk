# Project Documents

Besides the plan, the planner keeps two project documents and may read a master plan. The only files it writes outside `.afk/plans/` and `.afk/drafts/` are `CONTEXT.md` and `docs/decisions.md`, and only as described here.

## Glossary

If `CONTEXT.md` exists at the project root, read it during research and use its words in the plan, the task titles and the decision tree. At plan generation, append every domain term the plan introduces as one line: the term, then its meaning in plain language. Create the file when the first term appears. Never rewrite or reorder lines that are already there, and add a term only once.

## Decisions

When the session description names a master-plan task (for example "Implement T1.1 from docs/PLAN.md"), write or extend that task's section in `docs/decisions.md` with every decision the master plan did not make. These are the plan's `## Assumptions`, written in plain language: what was decided, and why. Extend an existing section for the task instead of adding a second one. The conductor amends the same entry before the final commit.

## Master plan

When the description names a task and a plan file, read these before building the decision tree:

1. The sections the plan file's own "how to use" text tells every task to read.
2. The named task's entry.
3. The `docs/decisions.md` entries of the tasks it depends on.

The task's Scope, Must not and Acceptance become requirements of the work plan: each one is covered by a task's `**What to do**`, `**Must NOT do**` or `**Acceptance Criteria**`, and a requirement no task covers is a gap in the plan.

## Research

In autonomous mode with a master-plan task as input, web research runs only when the task names an external API or library the repo does not already use (PLAN §5.7 rule 9). Everything else the master plan and the codebase already answer.
