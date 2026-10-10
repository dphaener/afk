---
tags: [planner-autonomy]
description: "A fact the repo answers (which test runner): the planner reads it and neither asks about it nor records it as an assumption."
model: claude-sonnet-5
max_turns: 80
timeout_seconds: 1800
allowed_tools: [Skill, AskUserQuestion, Read, Glob, Grep, Write, Edit, Agent, Task]
---

Use the afk:planner skill to write a plan for this, and name the plan tally-count-tests. Add
tests for the `count` command's edge cases (empty file, blank lines, no trailing newline,
tab-separated words) and for `history.record`, and write the plan so it says how the tests get
run.
