---
tags: [planner-autonomy]
description: "A fully specified, multi-part request: the planner decides every node itself, asks nothing, and records its assumptions with reasons in the plan."
model: claude-sonnet-5
max_turns: 80
timeout_seconds: 1800
allowed_tools: [Skill, AskUserQuestion, Read, Glob, Grep, Write, Edit, Agent, Task]
---

Use the afk:planner skill to write a plan for this, and name the plan tally-top. Add a `top` command to tally that prints
the N most frequent words in a file: `node src/cli.js top <file> [--limit N]`, default limit 5,
words lowercased, ties broken alphabetically, one `word count` pair per line. Add a `--json`
flag to both `count` and `top`; with it the command prints a single line of JSON
(`{"lines": N, "words": M}` for `count`, `[{"word": "the", "count": 4}, ...]` for `top`) instead
of the human-readable output, and the run is still recorded in history. History entries get a
`command` field naming the command that ran, and `history` prints it. Cover all of it with tests,
document the new command in the README, and add a changelog entry.
