# Workflow Engine Spec

This block is the execution-engine spec for `/afk:execute` and `/afk:go`. It is injected by the `execute` and `go` skills, never by `/afk:work-plan` (the light path carries its own entry contract in the planner's light-path block). It extends the afk Orchestrator (orchestrator/SKILL.md) with a second execution engine: the **Workflow tool** running one checked-in script, `workflows/execute.js`. Everything in orchestrator/SKILL.md still applies unless this block says otherwise for the Workflow engine. This block specifies how to run the plan through the static script, what to do when that is not possible, and the completion step that applies to both engines.

---

## 1. Preflight: Engine Detection and Announcement

Before delegating any tasks (after SKILL.md Step 1 plan analysis), detect the engine and announce it. There is no opt-in question: the invoking afk skill's instructions direct the Workflow engine, and that direction is the user's opt-in under the Workflow tool's rules.

Check whether the `Workflow` tool is present in the current session's available tool list. It is either a defined tool or absent; no probe invocation is needed. Also check that `ruby` is on PATH (the plan parser needs it).

Announce exactly one of these strings before the first delegation:

- Tool present: **"engine: Workflow (afk default)"**
- Tool absent: **"engine: Agent-loop fallback (Workflow tool unavailable in this session)"**

If the tool is present but `ruby` is not on PATH, announce that the parser cannot run and use the Agent-loop (Section 3), stating the reason in the announcement.

Also run `command -v afk`; it must succeed before the Workflow engine is chosen, because every checkbox mark is an `afk plan check` call. When it fails, say plainly: "afk is not on PATH. Install it (`make install` in the afk repo) so `afk plan check` can mark finished tasks, then run this again." A standalone `/afk:execute` has no conductor to check this, so you check it here. In autonomous mode the caller's failure path reports it.

**Never silently pick an engine.** Engine selection is a routing decision and must be announced before the first delegation.

---

## 2. Workflow Engine: Static Script

The orchestrator **never writes or edits Workflow JS**. One script, `{plugin-root}/workflows/execute.js`, is checked in and tested; the orchestrator only feeds it arguments.

### 2.1 Run

1. Parse the plan: `ruby {plugin-root}/scripts/plan_to_args.rb .afk/plans/{name}.md`, adding `--autonomous` when the caller states autonomous mode. It prints one JSON object to stdout.
2. If it exits 1, the plan is invalid: surface the stderr reason (`plan_to_args: {reason}`) verbatim and stop. The Agent-loop is not a way around an invalid plan, and the orchestrator never edits the plan to make it parse; the plan is the contract, and fixing it is the planner's or the user's job. In default mode that is the stop; in autonomous mode the caller's failure path reports it (the conductor applies Failure Handling), never silently.
3. Otherwise copy the checked-in script into the session's working tree, then run the copy: `cp {plugin-root}/workflows/execute.js .afk/{name}/execute.js`, then `Workflow({scriptPath: "{working-dir}/.afk/{name}/execute.js", args: <the parsed JSON>})`, with `{working-dir}` the absolute project root. The Workflow tool rejects a `scriptPath` outside the working directory and added directories, and the plugin root is outside both. Copy on every run, so the copy always matches the checked-in script, and never edit the copy. Pass `args` as a real object, never as a JSON string; the script throws before starting any agent when `args.tasks` is not an array.
4. Record the run ID the Workflow tool returns in `.afk/{name}/decisions.md` (append, under a `## Workflow run` heading).

The script schedules tasks by the dependency DAG that `plan_to_args.rb` builds (`mode: "dag"`) or by synthesized wave dependencies (`mode: "waves"` for legacy plans). A task starts as soon as everything it depends on has passed. Tasks whose `**Files**` overlap never run concurrently and run in plan order; this is the same-file serialization rule from SKILL.md Step 5, enforced by the script. Per-task retries, tier escalation (3 attempts, `light` to `balanced` to `heavy` to `reasoning`), the fallback-alias retry, budget stops, and the marking of finished tasks (below) all live inside the script. No bookkeeping agent exists.

### 2.2 Tiered verification and short task prompts (Workflow engine only)

Two rules of the Agent-loop are deliberately replaced for the Workflow engine. They remain in force for the Agent-loop.

- **Tiered verification replaces the per-task verifier.** Each task agent self-reports `{status, checks: [{command, exitCode}], summary, question?}` after running its own acceptance commands. A separate verifier agent (model `args.verifierModel`) runs only when the task is `heavy` or `reasoning` tier, touches a risky path (`.github/`, a `hooks/` directory, or a test path), reported no checks, reported a non-zero exit code, or reported a status other than `done`. Everything else passes on its self-report. On a verifier FAIL a fix agent runs, and the fix is re-verified. A `blocked` self-report never passes on a verifier PASS alone: the task agent runs again with the agent's own reason. A verifier that returns nothing is re-run on the next attempt against the same self-report; the task agent is not.
- **Short disk-pointer prompts replace the "prompts under 30 lines are too short" rule.** Task prompts point at the plan path and the task's line range, the codename announcement file under `{plugin-root}/skills/orchestrator/prompts/`, and the notepads. They do not carry the task text; the sub-agent reads the plan block itself. Sub-agents still read the notepads at start and append findings before finishing.

### 2.2.1 Who marks the plan

Nothing in the run edits the plan by hand; `afk plan check` is its only writer.

- **`done`:** the agent whose result settles the task runs `afk plan check --project {working-dir} {name} {id} done` as its last command. That is the verifier on PASS when one runs, else the task agent when it ran at least one check, every check exited 0 and its status is `done`. Finished work is marked when it finishes, so a crashed run never redoes it.
- **`failed` and blocked:** the script cannot run a command and spawns no agent for it, so the caller does. After each run, run `afk plan check --project {working-dir} --reason "{reason}" {name} {id} failed` for each entry of the result's `mark` list (Section 2.3). It also appends the `## (ID) FAILED` entry to issues.md.
- **While a run is in flight, the caller makes no model call and does not poll.** Wait for the Workflow tool to return; checking progress in a loop spends tokens for nothing.
- Verification stays tiered (Section 2.2): the task agents' own focused tests and self-reports carry most tasks, and the independent verifier runs on risk.

### 2.3 Results

The run returns `{passed, failed, skipped, needsInput, mark}`:

- `passed`: task ID strings.
- `failed`: `{id, reason}` entries. A task that used all 3 attempts is listed in `mark`; the plan is not changed until the caller marks it.
- `skipped`: `{id, reason, by?}` entries. Reason `blocked` means a dependency failed; the task is listed in `mark`. Reasons `needs_input` and `budget` mean the task never ran and stays `[ ]`.
- `needsInput`: `{id, question}` entries (default mode only; in autonomous mode a `needs_input` self-report counts as a failed attempt).
- `mark`: `{id, state: "failed", reason}` entries for every task the script could not have had an agent mark (failed tasks and tasks blocked by one), leaving out tasks already `[!]` or `[x]` when the run started. It is authoritative over the file.

Reconcile before anything else, using the command and not a delegated sub-agent: for each `mark` entry run `afk plan check --project {working-dir} --reason "{reason}" {name} {id} failed`, even where an agent had already ticked the task (the `mark` list wins over the file), skipping a task already `[!]`. Then for any ID in `passed` whose line is not `[x]`, run `afk plan check --project {working-dir} {name} {id} done` and record it in issues.md, so a re-run never re-executes finished work. A command that exits non-zero is reported with its message; do not hand-edit the plan.

Handle them in this order:

1. **`needsInput` (default mode only).** Relay each question per the caller's relay protocol. Append each answer to `.afk/{name}/decisions.md` under `## Relayed answers`. Then re-run the parser and invoke a fresh run; `done` tasks skip by status. A `needs_input` reaches the user only after the whole run returns, so independent work finishes first. State this when relaying.
2. **Tasks still `[ ]` after a run** (needs_input or budget class) mean a re-run is needed, not a passed gate. Re-run (parser, then fresh run) until no `[ ]` remains or a run makes no progress (no new task reaches `[x]` or `[!]`). Report a no-progress stop explicitly, naming the stuck task IDs and reasons.
3. **`failed` and `blocked` tasks** are reported in the final report with their reasons; they are not retried by re-running.

### 2.4 Final audit

After the last run returns, re-read the plan file. Confirm every task ID in `passed` is marked `[x]`; a mismatch means a mark did not land, so run `afk plan check ... done` for it (Section 2.3) and record it in issues.md. Spot-check changed files per SKILL.md Step 3.B (the cap rules apply). The orchestrator remains the final QA gate.

### 2.5 Wisdom

SKILL.md Step 6 carries over: notepads live under `.afk/{name}/`. The script's task prompts tell each sub-agent to read them at start and append at end. A Workflow-engine run MUST populate `.afk/{name}/learnings.md` the same as an Agent-loop run. If a run completes and learnings.md was never appended to, record a protocol violation in issues.md.

---

## 3. Fallback Engine: Agent Loop (unchanged)

Use the Agent-loop only when the Workflow tool is absent, `ruby` is missing, or a fresh re-run after a mid-run failure also fails (Section 5). Drop to the EXISTING Agent-loop orchestration specified in orchestrator/SKILL.md, unchanged: Steps 0-7 exactly as written, including direct Agent-tool delegation, notepad reads before every delegation, orchestrator-run Step 3 verification, and the Step 5 parallelism rules (the same same-file serialization rule). This block adds nothing to and removes nothing from that path except the engine announcement (Section 1) and the completion step (Section 6), which apply to both engines.

---

## 4. Invariant Floor (NON-NEGOTIABLE)

Regardless of engine, these invariants hold:

- **The plan file is always the contract (never executed from conversational memory).** The parser reads a fresh copy of `.afk/plans/{name}.md`; checkbox state on disk, not recollection, decides what runs.
- **Every routing or skip decision is announced.** Engine selection, task skips, re-runs, fallback transitions, and no-progress stops are stated explicitly, never silent.
- **The review phase stays independent of execution.** Execution does not review its own work, and the Workflow engine does not absorb or weaken review. The caller decides when and how review runs.
- **Same-file serialization stands.** Tasks that modify overlapping files run strictly one after another in plan order.

---

## 5. Mid-Run Failure

If a Workflow run dies mid-run (error, crash, or session interruption), recover in this exact order:

1. **Do NOT resume the dead run by its run ID.** DAG start order is not deterministic, so a replayed prefix of agent calls is not trustworthy. Re-run the parser against the plan (checkbox state on disk records what finished) and invoke a fresh run once. Announce the re-run.
2. **If the fresh run also fails, fall back to the Agent-loop for the remainder.** Re-read the plan file and treat its checkboxes as the source of truth:
   - `[x]` tasks are complete; **never re-run completed tasks**.
   - `[!]` tasks are failed; handle per SKILL.md Step 4's post-failure rules.
   - `[ ]` tasks are the remainder; execute them via the Agent-loop (Section 3), respecting dependencies and the same-file serialization rule. Announce the fallback transition before the first Agent-loop delegation.

---

## 6. Completion: Learnings Auto-Save (both engines)

After all tasks finish, regardless of which engine executed them, and after SKILL.md Step 7's final report:

1. Read `.afk/{name}/learnings.md` and evaluate every entry against the canonical filter, applied verbatim:

   > "memory-worthy = non-obvious, durable beyond this plan (environment quirks, tool behaviors, user preferences), and NOT derivable from the repo (code structure, git history, CLAUDE.md)"

2. Save every qualifying learning via the session's persistent-memory facility, with no question: one memory file per learning plus its MEMORY.md index line.
3. List the saved entries in the final report. If none qualify, save nothing and say nothing.
4. **NEVER write to CLAUDE.md, settings, or any user config.** The persistent-memory facility is the only sanctioned destination for saved learnings.
