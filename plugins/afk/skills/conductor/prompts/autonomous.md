# Autonomous Mode

This block extends the afk Conductor (`conductor/SKILL.md`). In autonomous mode a `/afk:go` run goes from invocation to reviewed commits on the session branch with nobody in the loop: the conductor never calls `AskUserQuestion` and never waits for a reply; it parks (§2 rule 3) only on something no machine can do, and it stops at the commit — no push, no PR (`docs/PLAN.md` T0.2). Every other conductor rule (disk gates, the status write protocol, the 2-cycle fix cap, grader independence) is unchanged. Default mode is unchanged by this block except for §7, which applies in both modes.

---

## 1. Detection and the marker file

- **ON** when `$ARGUMENTS` contains `--autonomous` OR `.afk/{name}/autonomous` exists. Strip the flag before Step 0.1's form matching.
- **Marker**: with the flag, create the empty marker (`touch .afk/{name}/autonomous`) as soon as Step 0.3 has created or adopted `.afk/{name}/`. The marker is how a resumed session (`/afk:go --session {name}`, a new Claude session, a crash restart) keeps the mode. Never delete it; a user who wants default mode back removes it by hand.
- **State lives in the marker, not in `status.json`.** Autonomy adds no status field and no phase value.
- **Announce** once, after detection: `mode: autonomous` (or `mode: default`).
- If `{name}` is not known yet (empty arguments), the planner stops with its autonomous-mode error first; there is nothing to mark.

---

## 2. The policy (`docs/PLAN.md` §5.5)

Seven rules, in order of precedence: an earlier rule wins over a later one, except that rule 7's project lines win over rules 2–6.

1. **Never wait on a rubber stamp.** No plan-approval gate, no ship sign-off, no "shall I proceed". The planner's TLDR, the engine choice, the review and the commit all happen without a question.
2. **Take the recommended option** on any question the planner would have asked or a sub-agent asks (§3). Record each assumed answer twice: as a bullet under `## Assumptions` in the work plan (`.afk/plans/{name}.md` — the planner writes its own during planning; you append later ones between runs, never while a run is in flight) and as a line in `.afk/{name}/decisions.md` (§3 gives the form). For each decision tagged `(assumed, would have escalated)` in the plan, once the plan is saved, and for each relayed answer (§3), also run `afk inbox add --kind question --no-block --title "{the decision in one line}" --assumed "{the answer taken}" --body "{the reason}"`, adding `--task {id}` when the session description names a master-plan task. Plain `(assumed)` decisions get no inbox item.
3. **Park instead of guessing** when the next step is irreversible or outside the machine: a missing credential or environment variable, a third-party dashboard, spending real money beyond the allowance in `afk.toml` `[policy] notes`, deleting data, anything that needs a physical device. Parking, in this order:
   - a. `afk inbox add --kind {env_var for a missing credential or variable | external_action for a dashboard, a device, money or deleting data | question for a choice only the owner can make} --block --title "{what the owner must do}" --body "{what is missing, where it goes, and that /afk:go --session {name} resumes}"`
   - b. Status-write `awaiting_input: true` (S4), per the Status Write Protocol; `phase` stays as it is.
   - c. Only if the command in a. failed (non-zero exit or `afk` missing), fire the toast as the fallback: `"{plugin-root}/hooks/notify.sh" "{name}" "$(basename "$PWD")" "afk is parked: {what the owner must do}" >/dev/null 2>&1 || true`. Otherwise no toast: the inbox item is the notification.
   - d. State plainly what is missing, where it goes, and that `/afk:go --session {name}` resumes the session (the startup write clears the flag, C3, and Resume Semantics pick the phase up from disk). Give the item's id.
   - e. End the turn. No `AskUserQuestion`, no polling, no guess.
   Parking is the only `awaiting_input: true` write in autonomous mode; the S3 cap stop is replaced by §4.
4. **Fix every confirmed review finding** of blocker or major severity (the review's critic has confirmed each one before it reaches you, contract C3), then re-review only the checks that failed. At the fix-loop cap, end `failed` (§4).
5. **Verification a machine cannot do** (a check on a real phone, a look at a screen you do not have) never blocks: append one line to `.afk/{name}/decisions.md` under `## Verification for the owner` — what to check and how — and run `afk inbox add --kind verification --no-block --title "{what to check}" --body "{how}"`. Carry on.
6. **Unsolicited scope ideas** go to `.afk/{name}/decisions.md` under `## Scope ideas`, one line each, and run `afk inbox add --kind notice --no-block --title "{the idea}"`; never into the diff and never into the plan.
7. **Project lines** from `afk.toml` `[policy] notes` at the project root, when that file exists: read them once after detection, append them after rule 6, and let them win over rules 2–6 (for example, a logged allowance for live API calls). They never override rule 1, the status schema, the phase gates or the 2-cycle cap.

The inbox is where the owner sees these records (`afk inbox`). Each item is written by `afk inbox add`, never by hand. A failed `afk inbox add` never blocks the work, except in a park, where the toast fallback applies. Never run `afk inbox answer`: answers are the owner's.

---

## 3. NEEDS_INPUT in autonomous mode

Delegations state autonomous mode, so sub-agents decide for themselves and record the decision in decisions.md. Under the Workflow engine, `plan_to_args.rb --autonomous` sets `args.autonomous`, and `execute.js` counts a `needs_input` self-report as a failed attempt; `needsInput` stays empty. A `NEEDS_INPUT` can still reach the conductor from the Agent-loop fallback (or from a sub-agent that ignored the mode). Answer each one yourself — unless its question is one of rule 3's cases, in which case park (§2 rule 3) instead:

1. **Pick the recommended option.** Use the option the sub-agent marked recommended; otherwise the reversible, least-exposure option consistent with the plan's What to do, Must NOT do, and `## Assumptions`. For an open-ended question, answer from the plan, the codebase, and the notepads with the same least-exposure rule.
2. **Record it** in `.afk/{name}/decisions.md` (append) under `## Relayed answers`:
   `- [{task-id}] {question verbatim} → {answer} — {reason} (assumed, would have asked)`
   and as a bullet under the plan's `## Assumptions`, plus the inbox call (rule 2).
3. **Re-delegate the SAME full prompt** with the `### Relayed Answer` subsection filled in: the question verbatim and the answer, stated as decided by the conductor in autonomous mode. Under the Workflow engine, re-run the parser and start a fresh run instead (`[x]` tasks skip by status).
4. No toast, no AskUserQuestion, no `awaiting_input: true` (a park is the one exception, above). The round-trip does not consume the task's 3-attempt retry budget.

---

## 4. Fix-loop cap (REJECT at `loop_count == 2`)

The cap itself is unchanged: never a third fix cycle. Only the stop is replaced:

1. No S3 write, no toast, no stop. `loop_count` stays `2`.
2. Hand off to the `# Ship Step` block (`conductor/prompts/ship.md`) in its **not-approved mode**: it folds the WIP commits and commits the work on the session branch — no push, no PR, no master-plan amendment. If no `# Ship Step` block is loaded in this session, skip the commit and say so in the report.
3. Status-write `phase: "failed"`, `awaiting_input: false` (C4), whatever the commit outcome. `failed` is terminal, so epic children never start from an unapproved parent.
4. Report: the verdict file's Checks and blocker/major Findings verbatim, the commit (or the commit failure), the branch the owner can inspect, and the reason `review still failing after 2 fix cycles`.

---

## 5. Every user stop and its autonomous replacement

| Point | Default mode | Autonomous mode |
|---|---|---|
| Step 0.2, no session UUID | AskUserQuestion: abort or continue degraded | Continue degraded without asking; announce `session UUID unavailable — continuing degraded (no hook notifications)` |
| Step 0.3, branch guard | Non-interactive in both modes | Same |
| Phase A, planning | Planner asks only escalation-rule decisions | Conductor passes autonomous mode; the planner never asks and marks would-escalate decisions `(assumed, would have escalated)`; Crucible runs only on HIGH risk, with no question |
| Phase A, planner error (no request, Jira unreachable) | Planner asks | Planner stops with its error; if `status.json` exists, apply Failure Handling (`phase: "failed"`) |
| Phase B, engine choice | No question (both modes) | Same; parser gets `--autonomous` |
| Phase B, `NEEDS_INPUT` | Relay: S4 write, toast, AskUserQuestion, C2 | Conductor answers itself (§3); a rule 3 case parks (§2) with a blocking inbox item |
| Any phase, a rule 3 case | The user is in the loop | Park: blocking inbox item, S4 write, plain statement, end the turn (toast only if the inbox call failed) |
| Phase C, fix-loop cap | S3 stop | Not-approved ship (commit only), then `phase: "failed"` (§4) |
| Ship | Commit, draft PR, `done` | Commit, no push, no PR, `done` (ship.md §2a); ship instructions in the session description are the one exception |

---

## 6. Never in autonomous mode

- Call `AskUserQuestion`, or end a turn waiting for a reply — a rule 3 park is the only deliberate stop.
- Write `awaiting_input: true` for anything but a rule 3 park; never S3.
- Push, open a PR, or ship in any way the session description did not ask for.
- Exceed the 2-cycle fix cap, or skip the `phase: "failed"` write at the cap.
- Hide an assumed answer, a parked step, an owner check or a scope idea: each is in decisions.md and, where §2 says so, in the inbox.
- Write a file under `.afk/inbox/` by hand, or run `afk inbox answer`.

---

## 7. Conductor reference blocks (both modes)

Detail moved out of `conductor/SKILL.md` to keep it under the `!cat` size cap. They apply in every run.

### 7.1 Session branch (Step 0.3, before the creation write)

Determine the trunk exactly as for `base_branch`. If `git branch --show-current` equals that trunk:

- **`{name}` does not exist** (`git show-ref --verify --quiet refs/heads/{name}` fails): create it, `gs --no-prompt branch create {name} --no-commit` when `git rev-parse --verify --quiet refs/spice/data` succeeds, else `git switch -c {name}`. Uncommitted changes carry over.
- **`{name}` exists**: `git switch {name}`. If git-spice is on and the branch is untracked (`git cat-file -e refs/spice/data:branches/{name}` fails), run `gs --no-prompt branch track {name} --base {base_branch}`.
- **`{name}` is checked out in another worktree** (`git switch` refuses): stop with git's error verbatim. No `status.json` is written.

On any other branch, stay on it.

### 7.2 Branch guard (every adoption, including form 2)

If an adopted file's `branch` equals its `base_branch`, do NOT perform the startup write: status-write `phase: "failed"`, `awaiting_input: false`, report `session branch equals base branch; start afk on a feature branch`, and stop. Never rewrite `branch`.

### 7.3 Verdict file mapping (Phase C.4)

Map review.js's `{checks: {compliance|quality|qa: {verdict, findings: [{severity, taskId, text}]}}}` (or the Agent path's per-check results) to `.afk/{name}/review-verdict.md` with the reviewer skill's Persist Verdict template, exactly (contract C3):

- **Checks lines**: `- Compliance: PASS|FAIL`, `- Quality: PASS|FAIL`, `- QA: PASS|FAIL`. A check is `FAIL` iff it has at least one `blocker` or `major` finding; the script's `verdict` is computed the same way, so recompute it rather than trust it (the Agent path returns none).
- **Findings**: each is `- [{severity}] {task-id}: ({check}) {text}` (`general` when `taskId` is empty), where `{check}` is `compliance`, `quality`, or `qa`, the check that produced it. Group by check in that order, most severe first within a check. Nothing else goes under `## Findings`: no sub-headings, no prose. No findings at all: the single line `- none`.
- **Subset run**: each check not rerun keeps its previous `## Checks` line and its previous finding lines (those tagged with its name) unchanged; rerun checks' lines are replaced (Subset Merge).
- **`**Verdict**`**: `REJECT` iff any Checks line is `FAIL`, else `APPROVE`. The write overwrites the previous file.

### 7.4 WIP commit detail (Phase C.2)

- `wip-base` records the pre-review HEAD once (only if absent); the ship step folds the WIP commits back to it. Never rewrite it.
- The `:(exclude).afk` pathspec keeps plan, status, and notepad files out of every commit. Nothing staged → no commit.
- Hooks run; NEVER `--no-verify`.
- **`.afk/` gitignored** (common): the `add` prints `The following paths are ignored ...` and exits 1, yet stages everything else. That message alone is not a failure: judge staging by `git diff --cached`, never chain the commit after `add` with `&&`.
- **Commit failure** (a hook reformats files, or a commit-message linter rejects the message): re-stage with the same pathspec and retry the commit once. Still failing → append the error to `.afk/{name}/issues.md`, run `git add -N -- . ':(exclude).afk'` so untracked files show in a working-tree diff, and use the two-dot `git diff {base_branch}` as this pass's diff command.

### 7.5 Review run (Phase C.3)

With the Workflow tool, first copy the checked-in script into the working tree, on every review pass, and never edit the copy: `cp {plugin-root}/workflows/review.js .afk/{name}/review.js`. The Workflow tool rejects a `scriptPath` outside the working directory and added directories, and the plugin root is outside both. Then pass `args` as a real object, with `checks` drawn from `compliance`, `quality`, `qa` (all three on the first pass; on a re-review only the checks whose line is `FAIL`):

```
Workflow({scriptPath: "{working-dir}/.afk/{name}/review.js", args: {
  plan: "{name}", planPath: "{working-dir}/.afk/plans/{name}.md", pluginRoot: "{plugin-root}",
  diffCommand: "{diff command}", checks: [{requested checks}],
  checkModel: "{balanced alias}", criticModel: "{reasoning alias}", criticFallbackModel: "{reasoning Fallback alias}"
}})
```

Substitute `{working-dir}` (absolute project root) and `{plugin-root}` (captured when `/afk:go` loaded). The `qa` check runs the app and the full test suite once, so the run is long; §7.6's no-call rule still holds.

### 7.6 Execution runs (Phase B)

- **Before the first run**: `command -v afk`. When it fails, apply Failure Handling (`phase: "failed"`) with this message: "afk is not on PATH. Install it (`make install` in the afk repo) so `afk plan check` can mark finished tasks, then run `/afk:go --session {name}` again." Every checkbox mark depends on it (engine spec §2.2.1).
- **While a run is in flight** (`execute.js` or `review.js`): make no model call and do no polling; the Workflow tool returns when the run ends (`docs/PLAN.md` §5.7 rule 7).
- **After each execute run**, before reading the plan for the Phase B gate, reconcile with the command — never by hand, never through a delegated agent — treating the result's `mark` list as authoritative over the file (contract C1; the engine spec's §2.3 says the same):
  1. For each `mark` entry: `afk plan check --project {working-dir} --reason "{reason}" {name} {id} failed`. It rewrites the task line and appends the `## (ID) FAILED` entry to issues.md. Run it even where an agent had already ticked the task `[x]`: the run's verdict wins. Skip a task whose line is already `[!]`: its first reason wins.
  2. For each id in `passed` whose line is not `[x]`: `afk plan check --project {working-dir} {name} {id} done`, and note the missed mark in `.afk/{name}/issues.md`.
  3. A non-zero exit is reported with the command's own message (1: unknown task or missing plan; 2: bad usage). Never edit the plan to work around it.
