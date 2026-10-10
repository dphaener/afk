# Autonomous Mode

This block extends the afk Conductor (`conductor/SKILL.md`). In autonomous mode a `/afk:go` run goes from invocation to a draft PR with zero user stops: the conductor never calls `AskUserQuestion`, never sets `awaiting_input: true`, and never waits for a reply. Every other conductor rule (disk gates, the status write protocol, the 2-cycle fix cap, grader independence) is unchanged. Default mode is unchanged by this block.

---

## 1. Detection and the marker file

- **ON** when `$ARGUMENTS` contains `--autonomous` OR `.afk/{name}/autonomous` exists. Strip the flag before Step 0.1's form matching.
- **Marker**: with the flag, create the empty marker (`touch .afk/{name}/autonomous`) as soon as Step 0.3 has created or adopted `.afk/{name}/`. The marker is how a resumed session (`/afk:go --session {name}`, a new Claude session, a crash restart) keeps the mode. Never delete it; a user who wants default mode back removes it by hand.
- **State lives in the marker, not in `status.json`.** Autonomy adds no status field and no phase value.
- **Announce** once, after detection: `mode: autonomous` (or `mode: default`).
- If `{name}` is not known yet (empty arguments), the planner stops with its autonomous-mode error first; there is nothing to mark.

---

## 2. Every user stop and its autonomous replacement

| Point | Default mode | Autonomous mode |
|---|---|---|
| Step 0.2, no session UUID | AskUserQuestion: abort or continue degraded | Continue degraded without asking; announce `session UUID unavailable — continuing degraded (no hook notifications)` |
| Step 0.3, branch guard | Non-interactive in both modes | Same |
| Phase A, planning | Planner asks only escalation-rule decisions | Conductor passes autonomous mode; the planner never asks and marks would-escalate decisions `(assumed, would have escalated)`; Crucible runs only on HIGH risk, with no question |
| Phase A, planner error (no request, Jira unreachable) | Planner asks | Planner stops with its error; if `status.json` exists, apply Failure Handling (`phase: "failed"`) |
| Phase B, engine choice | No question (both modes) | Same; parser gets `--autonomous` |
| Phase B, `NEEDS_INPUT` | Relay: S4 write, toast, AskUserQuestion, C2 | Conductor answers itself (§3); no S4, no toast |
| Phase C, fix-loop cap | S3 stop | Not-approved ship, then `phase: "failed"` (§4) |
| Ship | Non-interactive in both modes | Same |

---

## 3. NEEDS_INPUT in autonomous mode

Delegations state autonomous mode, so sub-agents decide for themselves and record the decision in decisions.md. Under the Workflow engine, `plan_to_args.rb --autonomous` sets `args.autonomous`, and `execute.js` counts a `needs_input` self-report as a failed attempt; `needsInput` stays empty. A `NEEDS_INPUT` can still reach the conductor from the Agent-loop fallback (or from a sub-agent that ignored the mode). Handle each one, without asking anyone:

1. **Pick the recommended option.** Use the option the sub-agent marked recommended; otherwise the reversible, least-exposure option consistent with the plan's What to do, Must NOT do, and `## Assumptions`. For an open-ended question, answer from the plan, the codebase, and the notepads with the same least-exposure rule.
2. **Record it** in `.afk/{name}/decisions.md` (append) under `## Relayed answers`:
   `- [{task-id}] {question verbatim} → {answer} — {reason} (assumed, would have asked)`
3. **Re-delegate the SAME full prompt** with the `### Relayed Answer` subsection filled in: the question verbatim and the answer, stated as decided by the conductor in autonomous mode. Under the Workflow engine, re-run the parser and start a fresh run instead (`done` tasks skip by status).
4. No `awaiting_input: true` write, no toast, no AskUserQuestion. The round-trip does not consume the task's 3-attempt retry budget.

---

## 4. Fix-loop cap (REJECT at `loop_count == 2`)

The cap itself is unchanged: never a third fix cycle. Only the stop is replaced:

1. No S3 write, no toast, no stop. `loop_count` stays `2`.
2. Hand off to the `# Ship Step` block (`conductor/prompts/ship.md`) in its **not-approved mode**: it commits the work and opens a draft PR whose title starts with `[not approved] ` and whose body opens with the open blocker and major findings from the verdict file. If no `# Ship Step` block is loaded in this session, skip shipping and say so in the report.
3. Status-write `phase: "failed"`, `awaiting_input: false` (C4), whatever the ship outcome. `failed` is terminal, so epic children never start from an unapproved parent.
4. Report: the verdict file's Checks and blocker/major Findings verbatim, the commit and PR (or the ship failure), and the reason `review still failing after 2 fix cycles`.

---

## 5. Same in both modes

These conductor rules are not autonomy rules; they apply to every run:

- **WIP commits** before every review pass (Phase C.2): `wip-base` written once, `git add -A -- . ':(exclude).afk'`, `chore(afk): WIP {name} review pass {n}`, hooks run, one retry, then the two-dot `git diff {base_branch}` fallback with `git add -N`.
- **Workflow review** (Phase C.1–C.4): Read reviewer/SKILL.md, run `review.js` (Agent path without the Workflow tool), map its result to the verdict file lines, re-review only `FAIL` checks with the Subset Merge.
- **Branch guard** (Step 0.3) and the non-blocking planning handoff.
- **Draft PRs** open without the user's approval in both modes (the Ship Step).

---

## 6. Never in autonomous mode

- Call `AskUserQuestion`, or end a turn waiting for a reply.
- Write `awaiting_input: true` (S3 or S4).
- Exceed the 2-cycle fix cap, or skip the `phase: "failed"` write at the cap.
- Hide an assumed answer: every self-answered question is in decisions.md.

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

Map review.js's `{checks: {compliance|quality|scope: {verdict, findings: [{severity, taskId, text}]}}}` (or the Agent path's per-check results) to `.afk/{name}/review-verdict.md` with the reviewer skill's Persist Verdict template, exactly:

- **Checks lines**: `- Compliance: PASS|FAIL`, `- Quality: PASS|FAIL`, `- Scope: PASS|FAIL`. A check is `FAIL` iff it has at least one `blocker` or `major` finding; ignore the returned `verdict` field.
- **Findings**: each is `- [{severity}] {task-id}: ({check}) {text}` (`general` when `taskId` is empty), where `{check}` is `compliance`, `quality`, or `scope`, the check that produced it. Group by check in that order, most severe first within a check. Nothing else goes under `## Findings`: no sub-headings, no prose. No findings at all: the single line `- none`.
- **Subset run**: each check not rerun keeps its previous `## Checks` line and its previous finding lines (those prefixed with its name) unchanged; rerun checks' lines are replaced (Subset Merge).
- **`**Verdict**`**: `REJECT` iff any Checks line is `FAIL`, else `APPROVE`. The write overwrites the previous file.

### 7.4 WIP commit detail (Phase C.2)

- `wip-base` records the pre-review HEAD once (only if absent); the ship step folds the WIP commits back to it. Never rewrite it.
- The `:(exclude).afk` pathspec keeps plan, status, and notepad files out of every commit. Nothing staged → no commit.
- Hooks run; NEVER `--no-verify`.
- **`.afk/` gitignored** (common): the `add` prints `The following paths are ignored ...` and exits 1, yet stages everything else. That message alone is not a failure: judge staging by `git diff --cached`, never chain the commit after `add` with `&&`.
- **Commit failure** (a hook reformats files, or a commit-message linter rejects the message): re-stage with the same pathspec and retry the commit once. Still failing → append the error to `.afk/{name}/issues.md`, run `git add -N -- . ':(exclude).afk'` so untracked files show in a working-tree diff, and use the two-dot `git diff {base_branch}` as this pass's diff command.

### 7.5 Review run (Phase C.3)

With the Workflow tool, first copy the checked-in script into the working tree, on every review pass, and never edit the copy: `cp {plugin-root}/workflows/review.js .afk/{name}/review.js`. The Workflow tool rejects a `scriptPath` outside the working directory and added directories, and the plugin root is outside both. Then pass `args` as a real object:

```
Workflow({scriptPath: "{working-dir}/.afk/{name}/review.js", args: {
  plan: "{name}", planPath: "{working-dir}/.afk/plans/{name}.md", pluginRoot: "{plugin-root}",
  diffCommand: "{diff command}", checks: [{requested checks}],
  checkModel: "{balanced alias}", criticModel: "{reasoning alias}", criticFallbackModel: "{reasoning Fallback alias}"
}})
```

Substitute `{working-dir}` (absolute project root) and `{plugin-root}` (captured when `/afk:go` loaded).
