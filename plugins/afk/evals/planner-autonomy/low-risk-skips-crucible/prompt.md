---
tags: [planner-autonomy]
description: "A small additive plan rated LOW: the Crucible never runs, and no question stands in its place."
model: claude-sonnet-5
max_turns: 80
timeout_seconds: 1800
allowed_tools: [Skill, AskUserQuestion, Read, Glob, Grep, Write, Edit, Agent, Task]
---

Use the afk:planner skill in autonomous mode (--autonomous) to write a plan for this, and name
the plan tally-version-flag. Add `node src/cli.js --version`, which prints the version from
package.json. Add a test for it, a line in the README usage section, and a changelog entry.
