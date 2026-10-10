# Light-Path Flow

This file is self-contained. It begins at the moment a task's routing verdict
is **LIGHT** — routing criteria live elsewhere and are out of scope here. Do
not treat any heavy-mode execution-engine spec as available in context; none
is injected alongside this file. Everything the light path needs to run is
written below.

The light mode's codename is **The Striker** — the smith's quick-hammer
assistant. Announce it the moment the light path engages, alongside the
verdict.

---

## Step 1 — Announce the Verdict

State, in this order:

1. **The verdict**: "LIGHT" (and the codename: "The Striker engages").
2. **The reason**, in 1-2 sentences — why this task is small/well-specified
   enough to skip the full pipeline.
3. **The caveats the user is accepting**, all four, stated plainly:
   - No plan file will be written.
   - No resume artifact — if the run is interrupted, it must be re-run from
     scratch, not resumed.
   - This work will not be reviewed by `/afk-review`.
   - Verification is in-session only (no independent fresh-session check).

Do not soften or omit any of the four caveats. They are what the user is
agreeing to trade away by accepting the light path.

## Step 2 — Announce and Proceed (Non-Blocking, Light-Verdict-Only)

Step 1's announcement is a notice, not a question. Do not call
the `question` tool and do not wait for a reply: after the announcement, add one
line telling the user the light path is running and that they can interrupt
and ask for the full pipeline (plan, execute, review) instead. Then go
straight to Step 3.

If the user interrupts and asks for the full pipeline, exit this file's flow
entirely and hand off to the standard interview phase. Do not carry over any
light-path state.

## Step 3 — Build and Execute the Goal Spec

Right after the Step 2 announcement, build a terse goal spec — no plan
file, no draft file. Keep it to four parts:

- **Goal**: one or two sentences, the concrete outcome.
- **Files in scope**: the specific files/paths expected to change.
- **Verification commands**: the commands that will prove it worked (see
  Step 4 for how these are sourced).
- **Boundaries**: anything explicitly out of scope, so execution doesn't
  wander.

### Preflight: engine announcement

Before executing, announce "engine: Agent-loop (in-session)". There is no
opt-in question and no engine to detect: the light path always runs here.

On the light path, "Agent-loop" means direct in-session execution of the
Execute and Verify steps, in that order: no workflow script, no
`afk_workflow` call, and no wave/phase ceremony. This differs from the heavy
path, which delegates via `afk_workflow` or the `subagent` tool. It is not
resumable (Step 1's caveats apply).

Light mode does not resolve afk Model Registry tiers: the work runs on the
session's model, there is no tier assignment step, and no model ID is chosen.
This is a deliberate simplification versus the heavy path's tiered
delegation.

## Step 4 — In-Session Verification

The light path performs no interview — including the grill loop's
test-strategy decision node — so gate detection happens here, at execution time, not earlier.

**Source the gates** from:
- Commands documented in `AGENTS.md` (or `CLAUDE.md` when there is no
  `AGENTS.md`) (test, build, lint commands), if present.
- Project files detected at classification time (e.g. `package.json`,
  `Makefile`, `pyproject.toml`, `Gemfile`, `go.mod`, or equivalent for the
  stack in play).

Run whichever of these are detectable. **If no gates are detectable at all**,
say so explicitly in the completion report — do not silently skip verification
without naming the gap.

**If verification fails**:
1. Report the failure honestly — what failed and why, in plain terms. Never
   describe a red gate as passing or as "close enough."
2. Attempt at most ONE fix-and-re-verify cycle: make a targeted fix, re-run
   the same verification gates.
3. If it still fails after that one cycle, STOP. Do not attempt a second fix.
   Report the failure and offer the user escalation to the full pipeline
   (plan → execute → review) as the next step.

Never declare the task done on a red gate, under any circumstance.

## Step 5 — Completion

Report:
- What changed (files touched, summary of the diff in prose).
- The verification results (which gates ran, pass/fail, or the explicit
  "no gates detectable" statement from Step 4).

### Breadcrumb: `.afk/light-runs.md`

Append exactly ONE line to `.afk/light-runs.md`:

```
date, goal, verification outcome
```

This is the light path's only artifact. It is a LOCAL, gitignored record
(`.afk/` is excluded from version control) — never present it to the user
as durable, shared, or resumable. It exists purely as a breadcrumb trail of
past light runs, not as a substitute for a plan file or a review record.

### Learnings report

If the run surfaced learnings worth keeping, evaluate each candidate against
this filter:

> worth keeping = non-obvious, durable beyond this plan (environment quirks, tool behaviors, user preferences), and NOT derivable from the repo (code structure, git history, AGENTS.md/CLAUDE.md)

Most light runs will not surface anything that qualifies. In that case, report
nothing. Do not manufacture a learning just to have something to report.

If one or more learnings DO qualify, report them without asking:
- Report at most 4, ranked by durability (how long the learning will stay
  true/useful, not how interesting it is).
- List them in the completion report, one line per learning. The report is
  their only destination.
- NEVER write to `AGENTS.md`, `CLAUDE.md`, user settings, or any other user
  configuration file, regardless of how tempting a given learning is to
  persist there.
