---
tags: [planner-autonomy]
description: "A multi-task plan: every task carries a Depends on line and no task depends on a same-wave or later-wave task."
model: claude-sonnet-5
max_turns: 80
timeout_seconds: 1800
allowed_tools: [Skill, AskUserQuestion, Read, Glob, Grep, Write, Edit, Agent, Task]
---

Use the afk:planner skill in autonomous mode (--autonomous) to write a plan for this, and name
the plan tally-top-cmd. Add a `top` command to tally that prints the N most frequent words in a
file: `node src/cli.js top <file> [--limit N]`, default limit 5, words lowercased, ties broken
alphabetically, one `word count` pair per line. Add a `--json` flag to both `count` and `top`
that prints a single line of JSON instead of the human-readable output. History entries get a
`command` field and `history` prints it. Cover all of it with tests, document the new command in
the README, and add a changelog entry.
