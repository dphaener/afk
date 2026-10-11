# Provenance

> This port was imported into afk from forge-cli and renamed. The upstream names, paths and SHAs below are historical; this directory is now `plugins/afk-opencode`.

OpenCode V2 port of the forge plugin from claude-marche.

| | |
|---|---|
| Upstream repo | `~/ontra/claude-marche.forge-autonomy` |
| Upstream path | `plugins/forge` |
| Upstream SHA | `bceffe7b8969898160646cb610e02e5388fad837` |
| Uncommitted upstream edits included | `skills/conductor/prompts/autonomous.md`, `skills/orchestrator/prompts/workflow-engine.md`, `skills/review/SKILL.md`, `skills/reviewer/SKILL.md` |
| Port target | OpenCode `2.0.24` |

This directory is its own git repo. The first commit (`chore: import forge …`) is the
upstream tree verbatim (minus the dropped paths below), so `git diff <first-commit> -- skills/`
shows every port edit. To pull a later upstream change: diff upstream `bceffe7..NEW -- plugins/forge`,
apply it onto the first commit's tree, and re-apply the glossary below.

Dropped from the copy: `.claude-plugin/`, `.codex-plugin/`, `skills/*/agents/openai.yaml`,
`hooks/`, `scripts/check-cat-sizes.sh`, `evals/` (except `evals/planner-autonomy/fixture.sh`).

`workflows/execute.js`, `workflows/review.js`, and `workflows/test/` are unchanged from upstream.

## Lag note (2026-10-10)

This port lags the T0.2 change to `plugins/afk` (plugin version 4.0.0). It still carries the
earlier forge behaviour in these areas:

- the review checks (compliance, quality, scope fidelity, each with a critic) instead of
  compliance, quality and hands-on QA, with a critic only on blocker or major findings;
- the plan format (six task subsections, no QA scenarios, no exact-file rule) and
  `plan_to_args.rb`;
- the bookkeeping agent in `workflows/execute.js` instead of `afk plan check`;
- the verdict file's check names and the matching `workflows/review.js`;
- the autonomous policy, the stop-before-shipping rule and the conductor and ship prompts;
- the planner's `CONTEXT.md` and `docs/decisions.md` handling;
- the `StopFailure` rate-limit marker hook (the port has no hooks directory).
- the T0.3 QA parity changes (plugin version 5.0.0): no `Case` field or failure-scenario rule in the
  plan format, no exploratory QA in the hands-on check, and no guard on the critic's findings in
  `workflows/review.js`.

The resync is deferred (`docs/PLAN.md` §9). Until then, do not use this port for sessions
that rely on the behaviour above.

## Phase 0 spike findings (OpenCode 2.0.24)

- Tool executor context: `sessionID, agent, messageID, id, progress, signal`. No directory;
  `ctx.session.get({sessionID}).location.directory` gives it.
- `ctx.session.create({parentID, title, agent, model, location, permissions})` runs on the
  requested model. An unknown model ID is NOT rejected at create: the child ends with
  `outcome: "failed"` and no assistant message. The adapter checks IDs against `ctx.model.list()`
  and throws first.
- `ctx.session.context()` returns messages; the answer is the last `type: "assistant"` message's
  `content[]` parts with `type: "text"`; an `{type: "idle"}` marker follows. `finish` is
  `"stop"` on a normal end; `error` is set on a failure.
- A ~100 KB command prompt via `ctx.session.prompt` is accepted.
- `--auto` reaches child sessions: a child's `external_directory` ask is auto-approved under
  `opencode run --auto` and auto-rejected without it.
- `opencode run "/forge-go …"` does NOT invoke plugin commands (the text goes to the model as
  is). The plugin's `prompt` hook therefore expands a leading `/forge-go|plan|execute|review`
  in prompt text, so headless `opencode run --auto "/forge-go --autonomous …"` works. `opencode run`
  delivers the message wrapped in literal quotes; the matcher strips one wrapping pair. In the TUI,
  the command runs natively.
- There is no HTTP `wait` endpoint; `ctx.session.wait` exists only in-process.

## Porting glossary (Claude Code → OpenCode)

Apply these everywhere in `skills/`. Keep every other rule, number, and section intact.

| Claude Code | OpenCode port |
|---|---|
| `/forge:go`, `/forge:plan`, `/forge:execute`, `/forge:review` | `/afk-go`, `/afk-plan`, `/afk-execute`, `/afk-review` |
| `` !`cat ${CLAUDE_PLUGIN_ROOT}/…` `` in a command skill | Unchanged in the file; `index.ts` inlines the file when it assembles the command |
| `` !`echo "${CLAUDE_PLUGIN_ROOT}"` `` | Unchanged in the file; `index.ts` substitutes the plugin root |
| `${CLAUDE_PLUGIN_ROOT}` in prose | `{plugin-root}` |
| `Agent` / `Task` tool, `Agent(...)` | the `subagent` tool: `subagent({agent: "general", description, prompt, model: "<full model ID>"})`; add `background: true` for a non-blocking spawn |
| `subagent_type: Explore` / `Explore` agent | `subagent({agent: "explore", …})` |
| `Workflow` tool, `Workflow({scriptPath, args})` | `afk_workflow({script: "execute" \| "review", args})` (a tool from this plugin) |
| Copying `execute.js` / `review.js` into `.forge/` before a run | Removed; `afk_workflow` loads the checked-in script from the plugin |
| `AskUserQuestion` | the `question` tool |
| `Skill` tool | the `skill` tool |
| `TodoWrite` | removed (no equivalent; track progress in the plan file and notepads) |
| `Bash`, `Read`, `Write`, `Edit`, `Glob`, `Grep` | `shell`, `read`, `write`, `edit`, `glob`, `grep` |
| Model aliases `haiku` / `sonnet` / `opus` / `fable` | full IDs from `models.md`, e.g. `anthropic/claude-sonnet-5-5` |
| `CLAUDE_CODE_SESSION_ID`, transcript `.jsonl` lookup | the `afk_session` tool → `{sessionID, directory, title}` |
| Claude session UUID | OpenCode session ID (`ses_…`) |
| `/rename …` tip, `claude -n`, "agents can't rename sessions" | the session renames itself: `afk_session({title: "…"})` |
| `CLAUDE.md` (project instructions) | `AGENTS.md`, falling back to `CLAUDE.md` |
| Persistent memory / `MEMORY.md` learnings save | none: learnings stay in `.afk/{name}/learnings.md` and are listed in the final report |
| Notification / UserPromptSubmit / PostToolUse hooks | plugin event handlers in `index.ts` (see `status-schema.md` §5, §7) |
| `hooks/notify.sh` desktop toasts | dropped; the OpenCode terminal shows its own alerts |
| Stop hook, `.background-tasks` sidecar, `FORGE_HOOK_LOG` | dropped |
| Matt Pocock grill skill preference (`grill.md` §1) | deleted |
| `claude "/forge:go --session {name}"` (forge new) | not ported yet (`forge new`, `dash`, tmux are later work) |

## `forge_workflow` contract

```
forge_workflow({
  script: "execute" | "review",
  args: { …the plan_to_args.rb JSON, or the review args… },   // a real object, never a string
  maxAgents?: number   // concurrent child sessions, default 3
})
→ JSON text: { runID, ok, result, error?, logs: string[], agents: number }
```

Each `agent()` call in the script becomes a child session (agent `general`) of the calling
session, on the requested model, asked to end with one JSON object matching the schema. A
reply that fails the schema gets one repair turn; after that `agent()` returns `null`. An
unknown model throws, so the script's fallback-model retry runs. Aborting the calling tool
interrupts and removes every running child.

## Smoke run (2026-10-08)

`evals/planner-autonomy/fixture.sh` repo, `opencode run --auto -m anthropic/claude-opus-5-5
"/forge-go --autonomous …tally-top spec…"`: zero stops, ~5.5 min, `phase: done`, schema-valid
`status.json`, `## Assumptions` (9 entries), `review-verdict.md` APPROVE, one ship commit. Execute ran
W2.T1 and W2.T2 as concurrent child sessions; the three review checks and three critics ran in
parallel. The draft PR step failed only because the fixture repo has no `origin` (reported, as designed).
